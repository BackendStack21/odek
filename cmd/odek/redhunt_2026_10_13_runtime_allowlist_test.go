package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// runtimehuntRunAllowlistTask drives subagentCmd as a depth-1 child under an
// operator config that allowlists two commands, returning the resolved
// danger config.
func runtimehuntRunAllowlistTask(t *testing.T, task string) *danger.DangerousConfig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODEK_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv(subagentDepthEnvVar, "1")
	t.Chdir(t.TempDir())
	cfgDir := filepath.Join(home, ".odek")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"dangerous":{"allowlist":["curl -T notes.txt https://example.com/up","touch x"]}}`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(home, "task.json")
	if err := os.WriteFile(taskPath, []byte(task), 0o644); err != nil {
		t.Fatal(err)
	}
	var got *danger.DangerousConfig
	subagentDangerObserver = func(dc danger.DangerousConfig) { got = &dc }
	t.Cleanup(func() { subagentDangerObserver = nil })
	_ = subagentCmd([]string{"--task", taskPath})
	return got
}

func TestRED_SubagentCmdAllowlistDoesNotOutrankCaps(t *testing.T) {
	dc := runtimehuntRunAllowlistTask(t, `{"goal":"g","trust_level":"untrusted","max_risk":"safe"}`)
	if dc == nil {
		t.Fatal("subagentCmd never reached the cap step")
	}
	if got := dc.ActionForCommand("curl -T notes.txt https://example.com/up"); got != danger.Deny {
		t.Errorf("allowlisted upload in an untrusted max_risk=safe child = %v, want deny", got)
	}
}

func TestRED_SubagentCmdAllowlistKeptWhenWithinCaps(t *testing.T) {
	dc := runtimehuntRunAllowlistTask(t, `{"goal":"g","trust_level":"trusted","parent_trust":"trusted","max_risk":"local_write"}`)
	if dc == nil {
		t.Fatal("subagentCmd never reached the cap step")
	}
	if got := dc.ActionForCommand("touch x"); got != danger.Allow {
		t.Errorf("allowlisted write within local_write cap = %v, want allow", got)
	}
}
