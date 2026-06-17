package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeServerPath compiles the fake MCP server from source on-the-fly
// and returns the path to the compiled binary. The binary is placed in
// t.TempDir() and cleaned up automatically when the test completes.
func fakeServerPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	testdataDir := filepath.Join(filepath.Dir(thisFile), "testdata")
	exePath := filepath.Join(t.TempDir(), "fakeserver")
	cmd := exec.Command("go", "build", "-o", exePath, testdataDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakeserver: %v\n%s", err, out)
	}
	return exePath
}

func TestNew_NonExistentCommand(t *testing.T) {
	_, err := New("ghost", ServerConfig{Command: "nonexistent-binary-xyzzy-12345"})
	if err == nil {
		t.Fatal("expected error for non-existent command")
	}
}

func TestDiscover_EmptyTools(t *testing.T) {
	client, err := New("empty", ServerConfig{
		Command: fakeServerPath(t),
		Env:     map[string]string{"FAKE_TOOLS": "[]"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	tools, err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestDiscover_MultipleTools(t *testing.T) {
	client, err := New("multi", ServerConfig{
		Command: fakeServerPath(t),
		Env: map[string]string{
			"FAKE_TOOLS": `[{"name":"fetch","description":"Fetch a URL"},{"name":"db_query","description":"Run SQL"}]`,
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	tools, err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "fetch" {
		t.Errorf("tool[0].Name = %q, want %q", tools[0].Name, "fetch")
	}
	if tools[1].Name != "db_query" {
		t.Errorf("tool[1].Name = %q, want %q", tools[1].Name, "db_query")
	}
}

func TestCallTool_Basic(t *testing.T) {
	client, err := New("echo", ServerConfig{
		Command: fakeServerPath(t),
		Env:     map[string]string{"FAKE_TOOLS": `[{"name":"echo","description":"Echo"}]`},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	// Discover first (required by protocol)
	if _, err := client.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	result, err := client.CallTool(context.Background(), "echo", `{"input":"hello"}`)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want %q", result, "ok")
	}
}

func TestCallTool_ServerError(t *testing.T) {
	client, err := New("err", ServerConfig{
		Command: fakeServerPath(t),
		Env: map[string]string{
			"FAKE_TOOLS":         `[{"name":"borked","description":"Always errors"}]`,
			"FAKE_ERROR_ON_CALL": "something broke",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	if _, err := client.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	_, err = client.CallTool(context.Background(), "borked", `{}`)
	if err == nil {
		t.Fatal("expected error for error-returning tool")
	}
	if !contains(err.Error(), "MCP error -32000") {
		t.Errorf("error message = %q, want substring 'MCP error -32000'", err.Error())
	}
}

func TestContextTimeout(t *testing.T) {
	client, err := New("timeout", ServerConfig{
		Command: fakeServerPath(t),
		Env:     map[string]string{"FAKE_TOOLS": `[]`},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond) // ensure context is expired

	_, err = client.Discover(ctx)
	if err == nil {
		t.Error("expected context deadline exceeded error")
	}
}

func TestClose_Idempotent(t *testing.T) {
	client, err := New("close-test", ServerConfig{
		Command: fakeServerPath(t),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Close twice — should not panic or error
	if err := client.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestReadLoop_OversizedResponse(t *testing.T) {
	dir := t.TempDir()
	hugePath := filepath.Join(dir, "huge.txt")
	scriptPath := filepath.Join(dir, "print.sh")

	// One line that exceeds the 10 MiB cap, followed by a newline, so the
	// scanner sees a single oversized token.
	data := bytes.Repeat([]byte{'x'}, maxMCPResponseLine+1)
	data = append(data, '\n')
	if err := os.WriteFile(hugePath, data, 0644); err != nil {
		t.Fatalf("write huge file: %v", err)
	}
	script := "#!/bin/sh\ncat \"" + hugePath + "\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	client, err := New("oversized", ServerConfig{
		Command: "sh",
		Args:    []string{scriptPath},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	_, err = client.Discover(context.Background())
	if err == nil {
		t.Fatal("expected error for oversized response")
	}
	if !strings.Contains(err.Error(), "response line exceeded") {
		t.Errorf("error = %v, want oversized response error", err)
	}
}

func TestValidateToolName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"fetch", false},
		{"db_query", false},
		{"read-file", false},
		{"tool_1", false},
		{"", true},
		{"read file", true},
		{"read/file", true},
		{"read\\file", true},
		{"read.file", true},
		{"tool;rm", true},
		{"a" + strings.Repeat("a", 64), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateToolName(tt.name)
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateToolName(%q) expected error", tt.name)
				}
				return
			}
			if err != nil {
				t.Errorf("validateToolName(%q) = %v, want nil", tt.name, err)
			}
		})
	}
}

func TestDiscover_InvalidToolName(t *testing.T) {
	client, err := New("invalid", ServerConfig{
		Command: fakeServerPath(t),
		Env:     map[string]string{"FAKE_TOOLS": `[{"name":"read file","description":"bad name"}]`},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	_, err = client.Discover(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid tool name")
	}
	if !strings.Contains(err.Error(), "invalid character") {
		t.Errorf("error = %v, want invalid character", err)
	}
}

func TestServerConfig_JSON(t *testing.T) {
	// Verify ServerConfig round-trips through JSON
	cfg := ServerConfig{
		Command: "node",
		Args:    []string{"server.js"},
		Env:     map[string]string{"FOO": "bar", "PATH": "/usr/bin"},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded ServerConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Command != "node" {
		t.Errorf("Command = %q, want %q", decoded.Command, "node")
	}
	if len(decoded.Args) != 1 || decoded.Args[0] != "server.js" {
		t.Errorf("Args = %v, want [server.js]", decoded.Args)
	}
	if decoded.Env["FOO"] != "bar" {
		t.Errorf("Env['FOO'] = %q, want %q", decoded.Env["FOO"], "bar")
	}
}

func TestServerConfig_JSONWithoutEnv(t *testing.T) {
	cfg := ServerConfig{
		Command: "python3",
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded ServerConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Env != nil {
		t.Errorf("expected nil Env for empty config, got %v", decoded.Env)
	}
}

func TestToolAdapter(t *testing.T) {
	client, err := New("adapter-test", ServerConfig{
		Command: fakeServerPath(t),
		Env:     map[string]string{"FAKE_TOOLS": `[{"name":"my_tool","description":"My tool"}]`},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	if _, err := client.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	adapter := &ToolAdapter{
		Client:   client,
		ToolName: "my_tool",
		Desc:     "My tool",
		ParamSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{"type": "string"},
			},
		},
	}

	if adapter.Name() != "adapter-test__my_tool" {
		t.Errorf("Name = %q, want %q", adapter.Name(), "adapter-test__my_tool")
	}
	if adapter.Description() != "My tool" {
		t.Errorf("Description = %q, want %q", adapter.Description(), "My tool")
	}

	// Test call
	result, err := adapter.Call(`{"input":"hello"}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want %q", result, "ok")
	}
}

func TestBuildEnv_Overrides(t *testing.T) {
	result := buildEnv(map[string]string{
		"PATH":    "/custom/bin",
		"NEW_VAR": "hello",
	})

	// Check PATH was overridden
	foundPath := false
	foundNew := false
	for _, e := range result {
		if e == "PATH=/custom/bin" {
			foundPath = true
		}
		if e == "NEW_VAR=hello" {
			foundNew = true
		}
	}
	if !foundPath {
		t.Error("expected PATH override in env")
	}
	if !foundNew {
		t.Error("expected NEW_VAR in env")
	}
}

func TestBuildEnv_RemovesEmptyValue(t *testing.T) {
	result := buildEnv(map[string]string{
		"PATH": "", // empty = remove from env
	})

	for _, e := range result {
		if strings.HasPrefix(e, "PATH=") {
			t.Errorf("expected PATH to be removed, but found: %s", e)
		}
	}
}

func TestBuildEnv_AllowlistBlocksSecrets(t *testing.T) {
	orig := osEnviron
	osEnviron = func() []string {
		return []string{
			"PATH=/usr/bin",
			"HOME=/home/user",
			"ODEK_API_KEY=sk-odek",
			"GITHUB_TOKEN=ghp-secret",
			"SOME_SECRET=shh",
			"MY_PASSWORD=hunter2",
		}
	}
	defer func() { osEnviron = orig }()

	result := buildEnv(nil)
	m := envToMap(result)

	if m["PATH"] != "/usr/bin" {
		t.Errorf("PATH = %q, want /usr/bin", m["PATH"])
	}
	if m["HOME"] != "/home/user" {
		t.Errorf("HOME = %q, want /home/user", m["HOME"])
	}
	for _, k := range []string{"ODEK_API_KEY", "GITHUB_TOKEN", "SOME_SECRET", "MY_PASSWORD"} {
		if _, ok := m[k]; ok {
			t.Errorf("sensitive key %q should not be forwarded", k)
		}
	}
}

func TestBuildEnv_OverridesCannotInjectSecrets(t *testing.T) {
	orig := osEnviron
	osEnviron = func() []string { return []string{"PATH=/usr/bin"} }
	defer func() { osEnviron = orig }()

	result := buildEnv(map[string]string{
		"LEGIT_VAR":   "ok",
		"EVIL_API_KEY": "sk-stolen",
		"BOT_TOKEN":    "tok-stolen",
	})
	m := envToMap(result)

	if m["LEGIT_VAR"] != "ok" {
		t.Errorf("LEGIT_VAR = %q, want ok", m["LEGIT_VAR"])
	}
	for _, k := range []string{"EVIL_API_KEY", "BOT_TOKEN"} {
		if _, ok := m[k]; ok {
			t.Errorf("sensitive override %q should be dropped", k)
		}
	}
}

func envToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			m[k] = v
		}
	}
	return m
}

func TestDiscover_FailsOnDeadProcess(t *testing.T) {
	client, err := New("dead", ServerConfig{
		Command: fakeServerPath(t),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Kill the process
	client.cmd.Process.Kill()
	time.Sleep(50 * time.Millisecond)

	_, err = client.Discover(context.Background())
	if err == nil {
		t.Error("expected error when process is dead")
	}
}

// ── Integration: Name() ───────────────────────────────────────────────

func TestClient_Name(t *testing.T) {
	client, err := New("my-server", ServerConfig{
		Command: fakeServerPath(t),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	if client.Name() != "my-server" {
		t.Errorf("Name = %q, want %q", client.Name(), "my-server")
	}
}

// ── Helper to run tests that need verbose info ─────────────────────────

func TestToolAdapter_ImplementsTool(t *testing.T) {
	// Verify ToolAdapter satisfies the Tool interface at compile time
	var _ interface {
		Name() string
		Description() string
		Schema() any
		Call(args string) (string, error)
	} = &ToolAdapter{}

	// If this compiles, the assertion passes
	fmt.Println("ToolAdapter satisfies interface")
}

func TestClient_CloseWithoutDiscover(t *testing.T) {
	// Closing a client without calling Discover should work
	client, err := New("no-discover", ServerConfig{
		Command: fakeServerPath(t),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ── Helpers ────────────────────────────────────────────────────────────

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
