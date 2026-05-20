package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────
// E2E Tests: odek repl (real subprocess)
// ─────────────────────────────────────────────────────────────────────
//
// These tests spawn the real odek binary in REPL mode and pipe input.
// Since the REPL detects a non-TTY stdin, it falls back to the simple
// bufio.Scanner mode (no raw terminal). This tests the full pipeline:
//
//   repl start → prompt → user input → agent loop → output → next prompt
//
// Gated by ODEK_E2E=true.
//
// Uses the same e2eBinary built by TestMain in subagent_e2e_test.go.
// ─────────────────────────────────────────────────────────────────────

// echoLLMHandler returns an HTTP handler that simulates a simple LLM.
// The first call returns a tool call, subsequent calls echo the user's
// message back as text. The callCount pointer tracks how many requests
// have been made for multi-step assertions.
func echoLLMHandler(callCount *int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*callCount++
		w.Header().Set("Content-Type", "application/json")
		if *callCount <= 1 {
			// First call: tool call (shell echo)
			fmt.Fprint(w, `{"choices":[{"message":{"content":"Checking.","tool_calls":[{"id":"c_1","function":{"name":"shell","arguments":"{\"command\":\"echo Hello from REPL\"}"}}]}}],"usage":{"prompt_tokens":50,"completion_tokens":10}}`)
		} else {
			// Subsequent: text response
			fmt.Fprint(w, `{"choices":[{"message":{"content":"Hello from the agent."}}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`)
		}
	}
}

// TestE2E_REPL_BasicPrompt verifies the REPL starts, shows a prompt,
// accepts a simple command, runs the agent loop, and exits cleanly.
func TestE2E_REPL_BasicPrompt(t *testing.T) {
	if os.Getenv("ODEK_E2E") != "true" {
		t.Skip("ODEK_E2E not set — skipping REPL E2E test")
	}

	// Start a mock LLM server
	callCount := 0
	llmSrv := httptest.NewServer(echoLLMHandler(&callCount))
	defer llmSrv.Close()

	// Set up env: mock LLM + isolate home dir
	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origOAI := os.Getenv("OPENAI_API_KEY")
	origHome := os.Getenv("HOME")
	odekBaseURL := os.Getenv("ODEK_BASE_URL")

	os.Setenv("DEEPSEEK_API_KEY", "sk-mock-repl")
	os.Unsetenv("OPENAI_API_KEY")
	os.Setenv("ODEK_BASE_URL", llmSrv.URL)
	homeDir := t.TempDir()
	os.Setenv("HOME", homeDir)
	os.Unsetenv("ODEK_SYSTEM")

	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("HOME", origHome)
		os.Setenv("ODEK_BASE_URL", odekBaseURL)
	}()

	// Ensure binary exists
	if e2eBinary == "" {
		t.Skip("e2eBinary not set — run TestMain first")
	}

	// Create a temp session dir so REPL can persist
	sessionDir := filepath.Join(homeDir, ".odek", "sessions")
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	// Spawn odek repl
	cmd := exec.Command(e2eBinary, "repl", "--model", "deepseek-chat")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}

	// Read stderr in background
	stderrDone := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(stderr)
		stderrDone <- string(data)
	}()

	// Give REPL time to initialize
	time.Sleep(500 * time.Millisecond)

	// Send a prompt command
	fmt.Fprintln(stdin, "say hello")
	time.Sleep(2 * time.Second)

	// Send exit
	fmt.Fprintln(stdin, "/exit")
	time.Sleep(500 * time.Millisecond)

	// Close stdin
	stdin.Close()

	// Wait for process to finish
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Logf("odek repl exited with error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for repl to exit")
	}

	// Collect output
	stderrOutput := <-stderrDone

	// Check for key REPL elements
	checks := []struct {
		name   string
		target string
	}{
		{"turn header", "─── Turn 1 ───"},
		{"prompt", "odek 1>"},
		{"session created", "odek ⚡"},
		{"agent response", "Hello"},
		{"exit message", "Session"},
	}

	for _, c := range checks {
		if !strings.Contains(stderrOutput, c.target) {
			t.Errorf("missing %s: expected %q in output", c.name, c.target)
		}
	}
}

// TestE2E_REPL_MultiTurn verifies the REPL handles multiple turns,
// showing incrementing turn numbers and persisting session state.
func TestE2E_REPL_MultiTurn(t *testing.T) {
	if os.Getenv("ODEK_E2E") != "true" {
		t.Skip("ODEK_E2E not set — skipping REPL E2E test")
	}

	callCount := 0
	llmSrv := httptest.NewServer(echoLLMHandler(&callCount))
	defer llmSrv.Close()

	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origOAI := os.Getenv("OPENAI_API_KEY")
	origHome := os.Getenv("HOME")
	odekBaseURL := os.Getenv("ODEK_BASE_URL")

	os.Setenv("DEEPSEEK_API_KEY", "sk-mock-repl")
	os.Unsetenv("OPENAI_API_KEY")
	os.Setenv("ODEK_BASE_URL", llmSrv.URL)
	homeDir := t.TempDir()
	os.Setenv("HOME", homeDir)
	os.Unsetenv("ODEK_SYSTEM")

	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("HOME", origHome)
		os.Setenv("ODEK_BASE_URL", odekBaseURL)
	}()

	if e2eBinary == "" {
		t.Skip("e2eBinary not set")
	}

	sessionDir := filepath.Join(homeDir, ".odek", "sessions")
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	cmd := exec.Command(e2eBinary, "repl", "--model", "deepseek-chat")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}

	stderrDone := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(stderr)
		stderrDone <- string(data)
	}()

	time.Sleep(500 * time.Millisecond)

	// Turn 1
	fmt.Fprintln(stdin, "first command")
	time.Sleep(2 * time.Second)

	// Turn 2
	fmt.Fprintln(stdin, "second command")
	time.Sleep(2 * time.Second)

	// Turn 3 — use a slash command
	fmt.Fprintln(stdin, "/info")
	time.Sleep(500 * time.Millisecond)

	// Exit
	fmt.Fprintln(stdin, "/exit")
	time.Sleep(500 * time.Millisecond)

	stdin.Close()

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Logf("exited: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out")
	}

	output := <-stderrDone

	// Verify multi-turn progression
	if !strings.Contains(output, "─── Turn 2 ───") {
		t.Error("missing Turn 2 header")
	}
	if !strings.Contains(output, "─── Turn 3 ───") {
		t.Error("missing Turn 3 header")
	}
	if !strings.Contains(output, "Session:") {
		t.Error("missing /info output")
	}
	if !strings.Contains(output, "Turns:") {
		t.Error("missing turn count from /info")
	}
}

// TestE2E_REPL_SlashHelp verifies the /help command works.
func TestE2E_REPL_SlashHelp(t *testing.T) {
	if os.Getenv("ODEK_E2E") != "true" {
		t.Skip("ODEK_E2E not set — skipping REPL E2E test")
	}

	llmSrv := httptest.NewServer(echoLLMHandler(new(int)))
	defer llmSrv.Close()

	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origHome := os.Getenv("HOME")
	odekBaseURL := os.Getenv("ODEK_BASE_URL")

	os.Setenv("DEEPSEEK_API_KEY", "sk-mock-repl")
	os.Setenv("ODEK_BASE_URL", llmSrv.URL)
	homeDir := t.TempDir()
	os.Setenv("HOME", homeDir)
	os.Unsetenv("ODEK_SYSTEM")

	defer func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("HOME", origHome)
		os.Setenv("ODEK_BASE_URL", odekBaseURL)
	}()

	if e2eBinary == "" {
		t.Skip("e2eBinary not set")
	}

	cmd := exec.Command(e2eBinary, "repl", "--model", "deepseek-chat")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}

	stderrDone := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(stderr)
		stderrDone <- string(data)
	}()

	time.Sleep(500 * time.Millisecond)

	fmt.Fprintln(stdin, "/help")
	time.Sleep(500 * time.Millisecond)
	fmt.Fprintln(stdin, "/exit")
	time.Sleep(500 * time.Millisecond)
	stdin.Close()

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out")
	}

	output := <-stderrDone
	if !strings.Contains(output, "/exit") {
		t.Error("missing /exit in help output")
	}
	if !strings.Contains(output, "/info") {
		t.Error("missing /info in help output")
	}
}

func init() {
	// Ensure json import is used
	_ = json.Marshal
}
