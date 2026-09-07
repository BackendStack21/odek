package main

import (
	"path/filepath"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

func TestAppendTTYClarify_MissingDeviceDoesNotRegister(t *testing.T) {
	danger.SetTTYPathForTest(filepath.Join(t.TempDir(), "no-such-tty"))
	t.Cleanup(func() { danger.SetTTYPathForTest("") })

	got := appendTTYClarify(nil)
	if len(got) != 0 {
		t.Fatalf("clarify registered without a TTY, got %d tools", len(got))
	}
}

func TestBuiltinTools_OmitsClarify(t *testing.T) {
	for _, tl := range builtinTools(danger.DangerousConfig{}, nil, nil, 1, "", toolConfig{}, nil) {
		if tl.Name() == "clarify" {
			t.Fatal("builtinTools must not register clarify — only interactive surfaces append it")
		}
	}
}

func TestReservedBuiltinToolNames_IncludesClarify(t *testing.T) {
	reserved := reservedBuiltinToolNames()
	for _, name := range []string{"clarify", "send_message"} {
		if !reserved[name] {
			t.Errorf("reservedBuiltinToolNames missing %q — an MCP server could shadow it", name)
		}
	}
}
