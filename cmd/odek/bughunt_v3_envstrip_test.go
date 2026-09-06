package main

// Bug-hunt v3 residual fix — secrets.env child-strip knob coverage.
//
// dangerous.strip_secrets_env_children: host-mode shell children get the
// configured secrets.env names removed from their environment; default off
// preserves credential-using workflows (gh, curl).

import (
	"context"
	"strings"
	"testing"
)

func TestShellBuildCmd_StripsSecretsEnvWhenOptedIn(t *testing.T) {
	prev := secretsEnvNames
	secretsEnvNames = func() []string { return []string{"ODEK_V3_FAKE_SECRET"} }
	defer func() { secretsEnvNames = prev }()

	t.Setenv("ODEK_V3_FAKE_SECRET", "leak-me")

	st := &shellTool{stripChildSecretEnv: true}
	cmd, _ := st.buildCmd(context.Background(), "echo hi")
	if cmd.Env == nil {
		t.Fatal("strip enabled but child env inherited unchanged (nil Env)")
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "ODEK_V3_FAKE_SECRET=") {
			t.Fatalf("secrets.env name leaked into stripped child env: %q", kv)
		}
	}
	// Sanity: the filter is targeted, not scorched-earth.
	foundPath := false
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "PATH=") {
			foundPath = true
		}
	}
	if !foundPath {
		t.Fatal("stripped env lost PATH — the filter must remove only the listed names")
	}

	// Default (off): inherit unchanged.
	st2 := &shellTool{}
	cmd2, _ := st2.buildCmd(context.Background(), "echo hi")
	if cmd2.Env != nil {
		t.Fatal("strip disabled must leave Env nil (full inheritance)")
	}
}
