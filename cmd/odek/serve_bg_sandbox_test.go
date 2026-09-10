package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/bgproc"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

func TestServeSandboxLeaseLifetime(t *testing.T) {
	var cleaned atomic.Int32
	l := &serveSandboxLease{container: "isolated-a", cleanup: func() error { cleaned.Add(1); return nil }}
	a, err := l.acquire()
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.acquire()
	if err != nil {
		t.Fatal(err)
	}
	argv, _, err := a.Wrap("echo marker")
	if err != nil || argv[0] != "docker" || !strings.Contains(strings.Join(argv, " "), "isolated-a") {
		t.Fatalf("route: %v %v", argv, err)
	}
	if err = l.close(); err != nil {
		t.Fatal(err)
	}
	if cleaned.Load() != 0 {
		t.Fatal("container removed with active jobs")
	}
	if _, err = l.acquire(); err == nil {
		t.Fatal("closed owner accepted spawn")
	}
	a.Release()
	a.Release()
	if cleaned.Load() != 0 {
		t.Fatal("container removed before final release")
	}
	b.Release()
	_ = l.close()
	if cleaned.Load() != 1 {
		t.Fatalf("cleanup count %d", cleaned.Load())
	}
}

func TestServeSandboxLeaseConcurrentClose(t *testing.T) {
	var cleaned atomic.Int32
	l := &serveSandboxLease{container: "isolated", cleanup: func() error { cleaned.Add(1); return nil }}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		opts, err := l.acquire()
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() { defer wg.Done(); opts.Release() }()
	}
	_ = l.close()
	wg.Wait()
	if cleaned.Load() != 1 {
		t.Fatalf("cleanup count %d", cleaned.Load())
	}
}

func TestServeSandboxManagerRequiresRouting(t *testing.T) {
	var cfg config.ResolvedConfig
	cfg.Sandbox = true
	cfg.Background.Enabled = true
	m := newServeBGManager(cfg)
	if m == nil {
		t.Fatal("sandbox background manager disabled")
	}
	defer m.Shutdown()
	if _, err := m.Start("s", "echo forbidden", "", 0); err == nil {
		t.Fatal("unrouted host launch accepted")
	}
}

// Opt-in real-container test: no provider credentials or network required.
func TestServeSandboxBackgroundDocker(t *testing.T) {
	image := os.Getenv("ODEK_BG_SANDBOX_TEST_IMAGE")
	if image == "" {
		t.Skip("set ODEK_BG_SANDBOX_TEST_IMAGE to a locally available sandbox image")
	}
	var cfg config.ResolvedConfig
	cfg.Sandbox = true
	cfg.Background.Enabled = true
	m := newServeBGManager(cfg)
	defer m.Shutdown()
	names := []string{fmt.Sprintf("odek-bg-test-%d-a", os.Getpid()), fmt.Sprintf("odek-bg-test-%d-b", os.Getpid())}
	leases := make([]*serveSandboxLease, 2)
	jobs := make([]*bgproc.Job, 2)
	for i, name := range names {
		if out, err := exec.Command("docker", "run", "-d", "--network", "none", "--workdir", "/workspace", "--name", name, "--entrypoint", "sh", image, "-c", "sleep 120").CombinedOutput(); err != nil {
			t.Fatalf("container: %v %s", err, out)
		}
		name := name
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
		leases[i] = &serveSandboxLease{container: name, cleanup: func() error { return exec.Command("docker", "rm", "-f", name).Run() }}

		command := "hostname; sleep 30"
		rt := newServeBGRuntime(m, false)
		bindBGRuntime(rt, name)
		rt.serveSandbox = leases[i]
		tool := &bgStartTool{rt: rt, shell: &shellTool{dangerousConfig: danger.DangerousConfig{Allowlist: []string{command}}}}
		args, _ := json.Marshal(map[string]string{"command": command})
		result, err := tool.Call(string(args))
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			JobID string `json:"job_id"`
		}
		if err = json.Unmarshal([]byte(result), &response); err != nil {
			t.Fatal(err)
		}
		job, ok := m.Get(name, response.JobID)
		if !ok {
			t.Fatal("tool job missing")
		}
		jobs[i] = &job

	}

	// Stop must kill descendants even while the owner keeps the container.
	opts, err := leases[0].acquire()
	if err != nil {
		t.Fatal(err)
	}
	child, err := m.StartWithOptions(names[0], "(sleep 1; touch /tmp/odek-child-survived) & echo ready; wait", "", 0, opts)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, _, _ := m.Output(names[0], child.ID, 0, 0)
		if strings.Contains(out, "ready") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child not ready: %s", out)
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.Stop(names[0], child.ID)
	if out, err := exec.Command("docker", "exec", names[0], "sh", "-c", "sleep 1.2; test ! -e /tmp/odek-child-survived").CombinedOutput(); err != nil {
		t.Fatalf("stopped descendant survived: %v %s", err, out)
	}
	for i, name := range names {
		deadline := time.Now().Add(10 * time.Second)
		for {
			out, _, _ := m.Output(name, jobs[i].ID, 0, 0)
			if strings.TrimSpace(out) != "" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("no container output")
			}
			time.Sleep(20 * time.Millisecond)
		}
		expected, err := exec.Command("docker", "exec", name, "hostname").Output()
		if err != nil {
			t.Fatal(err)
		}
		out, _, _ := m.Output(name, jobs[i].ID, 0, 0)
		if strings.TrimSpace(out) != strings.TrimSpace(string(expected)) {
			t.Fatalf("wrong container: %q vs %q", out, expected)
		}
		if _, ok := m.Get(names[1-i], jobs[i].ID); ok {
			t.Fatal("foreign job exposed")
		}
		if err := leases[i].close(); err != nil {
			t.Fatal(err)
		}
		if err := exec.Command("docker", "inspect", name).Run(); err != nil {
			t.Fatal("owner disconnect removed running container")
		}
	}
	m.Stop(names[0], jobs[0].ID)
	if err := exec.Command("docker", "inspect", names[0]).Run(); err == nil {
		t.Fatal("stopped job leaked container")
	}
	if err := exec.Command("docker", "inspect", names[1]).Run(); err != nil {
		t.Fatal("stopping first job removed second container")
	}
	m.Shutdown()
	if err := exec.Command("docker", "inspect", names[1]).Run(); err == nil {
		t.Fatal("shutdown leaked container")
	}
}

func TestServeSandboxCleanupRetriesAndRetainsFailure(t *testing.T) {
	attempts := 0
	fail := true
	l := &serveSandboxLease{container: "retry", cleanup: func() error {
		attempts++
		if fail {
			return fmt.Errorf("temporary Docker failure")
		}
		return nil
	}}
	if err := l.close(); err == nil {
		t.Fatal("expected cleanup failure")
	}
	if attempts != 3 {
		t.Fatalf("attempts: %d", attempts)
	}
	if _, ok := pendingSandboxCleanup.Load(l); !ok {
		t.Fatal("lost failed cleanup ownership")
	}
	fail = false
	if err := l.close(); err != nil {
		t.Fatal(err)
	}
	if attempts != 4 {
		t.Fatalf("no retry: %d", attempts)
	}
	if _, ok := pendingSandboxCleanup.Load(l); ok {
		t.Fatal("completed cleanup retained")
	}
}

func TestSandboxCleanupRegisteredBeforeRemovalCompletes(t *testing.T) {
	entered, unblock := make(chan struct{}), make(chan struct{})
	l := &serveSandboxLease{container: "delayed", cleanup: func() error { close(entered); <-unblock; return nil }}
	done := make(chan struct{})
	go func() { defer close(done); _ = l.close() }()
	<-entered
	_, registered := pendingSandboxCleanup.Load(l)
	close(unblock)
	<-done
	if !registered {
		t.Fatal("in-flight removal invisible to shutdown")
	}
	if _, exists := pendingSandboxCleanup.Load(l); exists {
		t.Fatal("successful removal retained")
	}
}
