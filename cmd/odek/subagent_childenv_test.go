package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Without a handed-off key the child keeps the parent's environment: there is
// no FD path to replace it, and unrelated variables always pass through.
func TestSubagentChildEnv_NoHandoffKeepsEnv(t *testing.T) {
	t.Setenv("ODEK_API_KEY", "sk-keep-0123456789abcdef")
	t.Setenv("ODEK_TEST_UNRELATED", "kept")

	dir := t.TempDir()
	dump := filepath.Join(dir, "env.txt")
	fake := filepath.Join(dir, "fake-odek")
	script := fmt.Sprintf("#!/bin/sh\nenv > %q\nprintf '{\"status\":\"success\",\"summary\":\"ok\",\"iterations\":1,\"tokens_used\":1}\\n'\n", dump)
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := &delegateTasksTool{odekPath: fake, maxConcurrency: 1, timeout: 30 * time.Second}
	tool.SetContext(context.Background())
	if _, err := tool.Call(`{"tasks":[{"goal":"a"}]}`); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("fake child did not run: %v", err)
	}
	if !strings.Contains(string(data), "ODEK_TEST_UNRELATED=kept") {
		t.Fatalf("unrelated variable was stripped:\n%s", data)
	}
	if !strings.Contains(string(data), "ODEK_API_KEY=") {
		t.Fatalf("env key stripped although no key was handed off:\n%s", data)
	}
}
