package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The tool face of the MCP view redacts only values that follow a
// credential-named FLAG. Credentials passed positionally (connection-string
// URLs, `-e NAME=value`, header values) reach the model verbatim through
// list_tools.
func TestRED_ListToolsLeaksPositionalMCPArgCredentials(t *testing.T) {
	in := []mcpEntry{
		{Name: "pg", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-postgres", "postgresql://app:hunter2pw@db.internal:5432/prod"}},
		{Name: "dock", Command: "docker", Args: []string{"run", "-i", "-e", "GITHUB_PERSONAL_ACCESS_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123456789", "img"}},
		{Name: "remote", Command: "npx", Args: []string{"mcp-remote", "https://x.example/sse", "--header", "Authorization: Bearer sk-live-9f8e7d6c5b4a"}},
	}
	out, _ := json.Marshal(redactMCPServersView(in))
	for _, secret := range []string{"hunter2pw", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "sk-live-9f8e7d6c5b4a"} {
		if strings.Contains(string(out), secret) {
			t.Errorf("tool face leaks credential %q: %s", secret, out)
		}
	}
}

func TestRedactCredentialArgsPositional(t *testing.T) {
	got := redactCredentialArgs([]string{
		"run", "-e", "DATABASE_URL=postgres://u:pw@h/db", "-e", "PLAIN=1",
		"--env", "TOKEN_X=abc", "LOG_LEVEL=debug", "API_KEY=zzz",
		"--header", "X-Api-Key: abc123", "Cookie: sid=1",
		"--url=https://user:pw@host/p", "https://host/public",
		"curl -H 'Authorization: Bearer abcdef'",
		"--api-key", "sekret", "--token=tok",
	})
	want := []string{
		"run", "-e", "DATABASE_URL=[redacted]", "-e", "PLAIN=[redacted]",
		"--env", "TOKEN_X=[redacted]", "LOG_LEVEL=debug", "API_KEY=[redacted]",
		"--header", "X-Api-Key: [redacted]", "Cookie: [redacted]",
		"--url=https://[redacted]@host/p", "https://host/public",
		"curl -H 'Authorization: Bearer [redacted]'",
		"--api-key", "[redacted]", "--token=[redacted]",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg %d: got %q want %q", i, got[i], want[i])
		}
	}
}
