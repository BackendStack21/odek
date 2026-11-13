package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const opsR3BgCmd = "sleep 3917"

// opsR3LLM answers the first request with a bg_start tool call and blocks the
// follow-up until the request is cancelled, so the turn is in flight while
// the operator interrupts it.
type opsR3LLM struct {
	mu      sync.Mutex
	calls   int
	blocked chan struct{}
	once    sync.Once
}

func (m *opsR3LLM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.ReadAll(r.Body)
	m.mu.Lock()
	m.calls++
	n := m.calls
	m.mu.Unlock()
	if n == 1 {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"bg_start","arguments":"{\"command\":\"%s\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`, opsR3BgCmd)
		return
	}
	m.once.Do(func() { close(m.blocked) })
	<-r.Context().Done()
}

func opsR3BgAlive() bool {
	out, _ := exec.Command("pgrep", "-f", "^"+opsR3BgCmd+"$").Output()
	return strings.TrimSpace(string(out)) != ""
}

// TestRED_ReplRepeatedCtrlCOrphansBackgroundJobs: repeated Ctrl-C presses while
// a cancelled turn unwinds must not exit the process past replCmd's deferred
// cleanups; a bg_start job (own process group) would outlive the REPL.
func TestRED_ReplRepeatedCtrlCOrphansBackgroundJobs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signals and pgrep are not available on windows")
	}
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}
	_ = exec.Command("pkill", "-f", "^"+opsR3BgCmd+"$").Run()
	t.Cleanup(func() { _ = exec.Command("pkill", "-f", "^"+opsR3BgCmd+"$").Run() })

	llm := &opsR3LLM{blocked: make(chan struct{})}
	srv := httptest.NewServer(llm)
	defer srv.Close()
	home := t.TempDir()
	writeMockProviderFixture(t, home, srv.URL)

	cmd := exec.Command(os.Args[0], "-test.run=^TestOpsReplHelperProcess$", "-test.count=1")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), opsReplHelperEnv+"=1", "HOME="+home, "USERPROFILE="+home, "ODEK_TEST_HOME="+home, "ODEK_NO_SANDBOX=1")
	stdin, _ := cmd.StdinPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	var mu sync.Mutex
	var out strings.Builder
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			mu.Lock()
			out.WriteString(sc.Text() + "\n")
			mu.Unlock()
		}
	}()
	transcript := func() string { mu.Lock(); defer mu.Unlock(); return out.String() }

	fmt.Fprintln(stdin, "start a background sleep")
	select {
	case <-llm.blocked:
	case <-time.After(120 * time.Second):
		t.Fatalf("follow-up model call never arrived:\n%s", transcript())
	}
	for deadline := time.Now().Add(60 * time.Second); !opsR3BgAlive() && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
	if !opsR3BgAlive() {
		t.Fatalf("background job not running:\n%s", transcript())
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for i := 0; i < 200; i++ {
		_ = cmd.Process.Signal(os.Interrupt)
		time.Sleep(time.Millisecond)
	}
	// Leave through /exit; if the process already died on the interrupts the
	// write fails harmlessly and the job check below decides.
	fmt.Fprintln(stdin, "/exit")
	_ = stdin.Close()
	select {
	case <-done:
	case <-time.After(120 * time.Second):
		t.Fatalf("REPL did not exit:\n%s", transcript())
	}
	for deadline := time.Now().Add(30 * time.Second); opsR3BgAlive() && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
	if opsR3BgAlive() {
		t.Fatalf("background job %q outlived the REPL after repeated Ctrl-C", opsR3BgCmd)
	}
}
