package main

import (
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/bgproc"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

func TestRED_BackgroundSettingsStripSecretsByDefault(t *testing.T) {
	var resolved config.ResolvedConfig
	if !backgroundSettingsFromResolved(resolved).StripChildSecretEnv {
		t.Fatal("background jobs inherit secrets.env names when strip_secrets_env_children is unset")
	}
	off := false
	resolved.Dangerous = danger.DangerousConfig{StripSecretsEnvChildren: &off}
	if backgroundSettingsFromResolved(resolved).StripChildSecretEnv {
		t.Fatal("explicit strip_secrets_env_children: false must keep inheritance")
	}
}

// The runtime always installs a wrap; in host mode (no container) that
// wrap must still run the job with secrets.env names stripped.
func TestRED_BackgroundRuntimeHostModeStripsSecrets(t *testing.T) {
	prev := secretsEnvNames
	secretsEnvNames = func() []string { return []string{"ODEK_SEC16_BG_SECRET"} }
	defer func() { secretsEnvNames = prev }()
	t.Setenv("ODEK_SEC16_BG_SECRET", "leaked-value")

	rt := newBackgroundRuntime(BackgroundSettings{Enabled: true, StripChildSecretEnv: true}, "sec16-host", "", nil)
	defer rt.Shutdown()
	job, err := rt.mgr.Start("sec16-host", "echo v=$ODEK_SEC16_BG_SECRET", "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := rt.mgr.Get("sec16-host", job.ID); ok && j.Status != bgproc.StatusRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	out, _, err := rt.mgr.Output("sec16-host", job.ID, 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "leaked-value") || !strings.Contains(out, "v=") {
		t.Fatalf("host-mode bg job output %q: secrets.env name not stripped", out)
	}
}
