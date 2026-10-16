package bgproc

import (
	"strings"
	"testing"
	"time"
)

// The sandbox wrapper's argv runs the host docker client, not the job: the
// container never sees host secrets, and the client may need DOCKER_* or
// proxy settings listed in secrets.env, so the strip applies to host-mode
// jobs only (as for the shell tool).
func TestRED_SandboxClientKeepsSecretsEnvNames(t *testing.T) {
	t.Setenv("BGPROC_SEC16_DOCKER_HOST", "unix:///sec16.sock")
	m := newTestManager(t, func(c *Config) {
		c.StripEnvNames = []string{"BGPROC_SEC16_DOCKER_HOST"}
		c.SandboxWrap = func(command string) ([]string, func(), error) {
			return []string{"sh", "-c", command}, nil, nil
		}
	})
	job, err := m.Start("sec16-sbx", "echo client=$BGPROC_SEC16_DOCKER_HOST", "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, m, "sec16-sbx", job.ID, 10*time.Second)
	out, _, err := m.Output("sec16-sbx", job.ID, 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "client=unix:///sec16.sock") {
		t.Fatalf("sandbox client process lost a secrets.env name it may need: %q", out)
	}
}

func TestRED_WrapRunOnHostAppliesStrip(t *testing.T) {
	t.Setenv("BGPROC_SEC16_HOST_SECRET", "leak-me")
	m := newTestManager(t, func(c *Config) {
		c.StripEnvNames = []string{"BGPROC_SEC16_HOST_SECRET"}
		c.SandboxWrap = func(string) ([]string, func(), error) { return nil, nil, ErrRunOnHost }
	})
	job, err := m.Start("sec16-host", "echo v=$BGPROC_SEC16_HOST_SECRET", "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, m, "sec16-host", job.ID, 10*time.Second)
	out, _, err := m.Output("sec16-host", job.ID, 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "leak-me") || !strings.Contains(out, "v=") {
		t.Fatalf("host-mode spawn via ErrRunOnHost kept a stripped name: %q", out)
	}
}

func TestRED_WrapRunOnHostRefusedWhenSandboxRequired(t *testing.T) {
	m := newTestManager(t, func(c *Config) {
		c.RequireSandbox = true
		c.SandboxWrap = func(string) ([]string, func(), error) { return nil, nil, ErrRunOnHost }
	})
	if _, err := m.Start("sec16-req", "echo hi", "", 10*time.Second); err == nil {
		t.Fatal("RequireSandbox accepted a wrapper that routed the spawn to the host")
	}
}
