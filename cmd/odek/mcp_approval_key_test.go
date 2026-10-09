package main

import (
	"testing"

	"github.com/BackendStack21/odek/internal/mcpclient"
)

func TestMCPApprovalKey_FieldBoundaries(t *testing.T) {
	base := mcpclient.ServerConfig{Command: "node", Args: []string{"a", "b"}}
	seen := map[string]string{}
	add := func(label, key string) {
		t.Helper()
		if prev, ok := seen[key]; ok {
			t.Fatalf("%s collides with %s", label, prev)
		}
		seen[key] = label
	}
	add("base", mcpApprovalKey("/p", "s", base))
	add("dir/name shift", mcpApprovalKey("/p\x00s", "", base))
	add("name/command shift", mcpApprovalKey("/p", "snode", mcpclient.ServerConfig{Args: base.Args}))
	add("args merged", mcpApprovalKey("/p", "s", mcpclient.ServerConfig{Command: "node", Args: []string{"a\x00b"}}))
	add("extra empty arg", mcpApprovalKey("/p", "s", mcpclient.ServerConfig{Command: "node", Args: []string{"a", "b", ""}}))
	add("no args", mcpApprovalKey("/p", "s", mcpclient.ServerConfig{Command: "node"}))

	if mcpApprovalKey("/p", "s", base) != mcpApprovalKey("/p", "s", base) {
		t.Fatal("key must be deterministic")
	}
}
