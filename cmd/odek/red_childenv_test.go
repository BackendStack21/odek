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

// The parent hands its API key to the child over FD 3 precisely so the key is
// not in the child's environment. When the operator supplied that key through
// the process environment (not secrets.env), the child still inherits it.
func TestRED_SubagentChildEnvDoesNotCarryHandedOffKey(t *testing.T) {
	const key = "sk-red-handoff-0123456789abcdef"
	t.Setenv("ODEK_API_KEY", key)
	t.Setenv("OPENAI_API_KEY", key)

	dir := t.TempDir()
	dump := filepath.Join(dir, "env.txt")
	fake := filepath.Join(dir, "fake-odek")
	script := fmt.Sprintf("#!/bin/sh\nenv > %q\nprintf '{\"status\":\"success\",\"summary\":\"ok\",\"iterations\":1,\"tokens_used\":1}\\n'\n", dump)
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := &delegateTasksTool{
		odekPath:       fake,
		maxConcurrency: 1,
		timeout:        30 * time.Second,
		apiKey:         key,
	}
	tool.SetContext(context.Background())
	if _, err := tool.Call(`{"tasks":[{"goal":"a"}]}`); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("fake child did not run: %v", err)
	}
	if strings.Contains(string(data), key) {
		t.Fatalf("child environment still carries the API key that was handed off over FD 3:\n%s",
			grepLines(string(data), key))
	}
}

func grepLines(s, sub string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			name, _, _ := strings.Cut(l, "=")
			out = append(out, name+"=<key>")
		}
	}
	return strings.Join(out, "\n")
}
