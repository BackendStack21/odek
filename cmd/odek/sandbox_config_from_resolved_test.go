package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
)

func TestSandboxConfigFromResolved_MapsEveryKnob(t *testing.T) {
	r := config.ResolvedConfig{
		SandboxImage: "img:1", SandboxNetwork: "bridge", SandboxReadonly: true,
		SandboxMemory: "1g", SandboxCPUs: "2", SandboxPidsLimit: "300", SandboxUser: "1:2",
		SandboxEnv: map[string]string{"A": "b"}, SandboxVolumes: []string{"./x:/x"},
	}
	got := sandboxConfigFromResolved(r)
	want := sandboxConfig{
		Image: "img:1", Network: "bridge", Readonly: true, Memory: "1g", CPUs: "2",
		PidsLimit: "300", User: "1:2", Env: map[string]string{"A": "b"}, Volumes: []string{"./x:/x"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestApproveProjectSandbox_PromptShowsPidsLimit(t *testing.T) {
	setupTestHome(t)
	t.Setenv("ODEK_APPROVE_PROJECT_SANDBOX", "")
	t.Chdir(t.TempDir())
	resolved := config.ResolvedConfig{ProjectSandboxOverride: config.ProjectSandboxOverride{HasPidsLimit: true, PidsLimit: "128\x1b[31m"}}
	var out bytes.Buffer
	if err := approveProjectSandboxWithTTY(resolved, strings.NewReader("n\n"), &out, true); err == nil {
		t.Fatal("expected denial")
	}
	if !strings.Contains(out.String(), "pids:") || !strings.Contains(out.String(), "128") {
		t.Errorf("prompt missing pids line: %q", out.String())
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Errorf("prompt must sanitize control characters: %q", out.String())
	}
}
