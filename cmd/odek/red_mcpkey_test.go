package main

import (
	"testing"

	"github.com/BackendStack21/odek/internal/mcpclient"
)

func TestRED_MCPApprovalKeyArgBoundary(t *testing.T) {
	a := mcpclient.ServerConfig{Command: "node", Args: []string{"safe.js", "--flag"}}
	b := mcpclient.ServerConfig{Command: "node", Args: []string{"safe.js\x00--flag"}}
	if mcpApprovalKey("/p", "s", a) == mcpApprovalKey("/p", "s", b) {
		t.Fatal("different arg vectors share an approval key (NUL boundary collision)")
	}
}

func TestRED_MCPApprovalKeyCommandArgBoundary(t *testing.T) {
	c := mcpclient.ServerConfig{Command: "node\x00a", Args: nil}
	d := mcpclient.ServerConfig{Command: "node", Args: []string{"a"}}
	if mcpApprovalKey("/p", "s", c) == mcpApprovalKey("/p", "s", d) {
		t.Fatal("command/args boundary collision")
	}
}
