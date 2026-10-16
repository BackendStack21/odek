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
