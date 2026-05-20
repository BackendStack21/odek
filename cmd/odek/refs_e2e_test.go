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
// E2E Tests: odek run with --ctx and @ref file attachments
// ─────────────────────────────────────────────────────────────────────
//
// These tests spawn the real odek binary with `odek run` and verify
// that file content is correctly attached and sent to the LLM.
//
// Gated by ODEK_E2E=true.
//
// Uses the same e2eBinary built by TestMain in subagent_e2e_test.go.
// ─────────────────────────────────────────────────────────────────────

// captureLLMHandler returns an HTTP handler that records the last
// request body and returns a simple text response. The recorded body
// can be inspected after the test run.
func captureLLMHandler(recorded *[]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		*recorded = body
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"Test analysis result."}}],"usage":{"prompt_tokens":50,"completion_tokens":10}}`)
	}
}

// TestE2E_RunWithCtxFile verifies that --ctx attaches file content
// and it appears in the LLM request body.
func TestE2E_RunWithCtxFile(t *testing.T) {
	skipIfNoE2E(t)

	// Start a mock LLM server that captures the request
	var recordedBody []byte
	llmSrv := httptest.NewServer(captureLLMHandler(&recordedBody))
	defer llmSrv.Close()

	// Set up env
	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origOAI := os.Getenv("OPENAI_API_KEY")
	origHome := os.Getenv("HOME")
	odekBaseURL := os.Getenv("ODEK_BASE_URL")

	t.Cleanup(func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("HOME", origHome)
		os.Setenv("ODEK_BASE_URL", odekBaseURL)
	})

	os.Setenv("DEEPSEEK_API_KEY", "sk-mock-refs")
	os.Unsetenv("OPENAI_API_KEY")
	os.Setenv("ODEK_BASE_URL", llmSrv.URL)
	homeDir := t.TempDir()
	os.Setenv("HOME", homeDir)
	os.Unsetenv("ODEK_SYSTEM")

	// Create a temp working directory with a test file
	workDir := t.TempDir()
	testFile := filepath.Join(workDir, "data.txt")
	if err := os.WriteFile(testFile, []byte("Hello, E2E test content!"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// Create session dir (needed for config load)
	sessionDir := filepath.Join(homeDir, ".odek", "sessions")
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	// Ensure binary exists
	if e2eBinary == "" {
		t.Skip("e2eBinary not set — run TestMain first")
	}

	// Run: odek run --ctx data.txt "analyze this file"
	cmd := exec.Command(e2eBinary, "run", "--ctx", "data.txt", "--model", "deepseek-chat", "analyze this file")
	cmd.Dir = workDir

	stderr := &strings.Builder{}
	cmd.Stderr = stderr

	// Run with timeout
	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Logf("odek run exited with: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for odek run to finish")
	}

	stderrOutput := stderr.String()
	t.Logf("stderr:\n%s", stderrOutput)

	// Verify "attached N file(s)" message
	if !strings.Contains(stderrOutput, "odek: attached 1 file(s)") {
		t.Error("missing 'attached 1 file(s)' message")
	}

	// Verify LLM received the file content
	if len(recordedBody) == 0 {
		t.Fatal("mock LLM server received no request")
	}

	// Parse the request body to find the file content
	bodyStr := string(recordedBody)
	if !strings.Contains(bodyStr, "Hello, E2E test content!") {
		t.Errorf("LLM request does not contain file content.\nBody:\n%s", bodyStr)
	}
	if !strings.Contains(bodyStr, "--- data.txt ---") {
		t.Errorf("LLM request missing ctx file header.\nBody:\n%s", bodyStr)
	}
	if !strings.Contains(bodyStr, "analyze this file") {
		t.Errorf("LLM request missing user task.\nBody:\n%s", bodyStr)
	}
}

// TestE2E_RunWithAtRef verifies that @ref inline resolution works
// and file content appears in the LLM request body.
func TestE2E_RunWithAtRef(t *testing.T) {
	skipIfNoE2E(t)

	var recordedBody []byte
	llmSrv := httptest.NewServer(captureLLMHandler(&recordedBody))
	defer llmSrv.Close()

	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origOAI := os.Getenv("OPENAI_API_KEY")
	origHome := os.Getenv("HOME")
	odekBaseURL := os.Getenv("ODEK_BASE_URL")

	t.Cleanup(func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("HOME", origHome)
		os.Setenv("ODEK_BASE_URL", odekBaseURL)
	})

	os.Setenv("DEEPSEEK_API_KEY", "sk-mock-refs")
	os.Unsetenv("OPENAI_API_KEY")
	os.Setenv("ODEK_BASE_URL", llmSrv.URL)
	homeDir := t.TempDir()
	os.Setenv("HOME", homeDir)
	os.Unsetenv("ODEK_SYSTEM")

	workDir := t.TempDir()
	testFile := filepath.Join(workDir, "notes.txt")
	if err := os.WriteFile(testFile, []byte("Important project notes."), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	sessionDir := filepath.Join(homeDir, ".odek", "sessions")
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	if e2eBinary == "" {
		t.Skip("e2eBinary not set — run TestMain first")
	}

	// Run: odek run "@notes.txt summarize this"
	cmd := exec.Command(e2eBinary, "run", "--model", "deepseek-chat", "@notes.txt summarize this")
	cmd.Dir = workDir

	stderr := &strings.Builder{}
	cmd.Stderr = stderr

	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Logf("odek run exited with: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for odek run to finish")
	}

	stderrOutput := stderr.String()
	t.Logf("stderr:\n%s", stderrOutput)

	if len(recordedBody) == 0 {
		t.Fatal("mock LLM server received no request")
	}

	bodyStr := string(recordedBody)
	if !strings.Contains(bodyStr, "Important project notes.") {
		t.Errorf("LLM request does not contain @ref file content.\nBody:\n%s", bodyStr)
	}
	if !strings.Contains(bodyStr, "summarize this") {
		t.Errorf("LLM request missing user task.\nBody:\n%s", bodyStr)
	}
}

// TestE2E_RunWithBothCtxAndAtRef verifies that --ctx and @ref
// work together: ctx files prepend, @ref files resolve inline.
func TestE2E_RunWithBothCtxAndAtRef(t *testing.T) {
	skipIfNoE2E(t)

	var recordedBody []byte
	llmSrv := httptest.NewServer(captureLLMHandler(&recordedBody))
	defer llmSrv.Close()

	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origOAI := os.Getenv("OPENAI_API_KEY")
	origHome := os.Getenv("HOME")
	odekBaseURL := os.Getenv("ODEK_BASE_URL")

	t.Cleanup(func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("HOME", origHome)
		os.Setenv("ODEK_BASE_URL", odekBaseURL)
	})

	os.Setenv("DEEPSEEK_API_KEY", "sk-mock-refs")
	os.Unsetenv("OPENAI_API_KEY")
	os.Setenv("ODEK_BASE_URL", llmSrv.URL)
	homeDir := t.TempDir()
	os.Setenv("HOME", homeDir)
	os.Unsetenv("ODEK_SYSTEM")

	workDir := t.TempDir()
	os.WriteFile(filepath.Join(workDir, "main.txt"), []byte("main context"), 0644)
	os.WriteFile(filepath.Join(workDir, "lib.txt"), []byte("library context"), 0644)

	sessionDir := filepath.Join(homeDir, ".odek", "sessions")
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	if e2eBinary == "" {
		t.Skip("e2eBinary not set — run TestMain first")
	}

	// Run: odek run --ctx main.txt "@lib.txt compare both"
	cmd := exec.Command(e2eBinary, "run", "--ctx", "main.txt", "--model", "deepseek-chat", "@lib.txt compare both")
	cmd.Dir = workDir

	stderr := &strings.Builder{}
	cmd.Stderr = stderr

	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Logf("odek run exited with: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for odek run to finish")
	}

	if len(recordedBody) == 0 {
		t.Fatal("mock LLM server received no request")
	}

	bodyStr := string(recordedBody)

	// --ctx content should appear first (prepended)
	if !strings.Contains(bodyStr, "--- main.txt ---") {
		t.Errorf("LLM request missing --ctx file header.\nBody:\n%s", bodyStr)
	}
	if !strings.Contains(bodyStr, "main context") {
		t.Errorf("LLM request missing --ctx file content.\nBody:\n%s", bodyStr)
	}

	// @ref content should appear as resolved inline
	if !strings.Contains(bodyStr, "library context") {
		t.Errorf("LLM request missing @ref file content.\nBody:\n%s", bodyStr)
	}
	if !strings.Contains(bodyStr, "compare both") {
		t.Errorf("LLM request missing user task.\nBody:\n%s", bodyStr)
	}
}

// TestE2E_RunWithCtxMissingFile verifies that a missing --ctx file
// causes an error exit.
func TestE2E_RunWithCtxMissingFile(t *testing.T) {
	skipIfNoE2E(t)

	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer llmSrv.Close()

	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origHome := os.Getenv("HOME")
	odekBaseURL := os.Getenv("ODEK_BASE_URL")

	t.Cleanup(func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("HOME", origHome)
		os.Setenv("ODEK_BASE_URL", odekBaseURL)
	})

	os.Setenv("DEEPSEEK_API_KEY", "sk-mock-refs")
	os.Setenv("ODEK_BASE_URL", llmSrv.URL)
	homeDir := t.TempDir()
	os.Setenv("HOME", homeDir)
	os.Unsetenv("ODEK_SYSTEM")

	sessionDir := filepath.Join(homeDir, ".odek", "sessions")
	os.MkdirAll(sessionDir, 0700)

	if e2eBinary == "" {
		t.Skip("e2eBinary not set — run TestMain first")
	}

	workDir := t.TempDir()
	cmd := exec.Command(e2eBinary, "run", "--ctx", "nonexistent.txt", "--model", "deepseek-chat", "analyze")
	cmd.Dir = workDir

	// Should fail with non-zero exit
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Error("expected error for missing ctx file, got exit 0")
	}
	if !strings.Contains(string(output), "nonexistent.txt") {
		t.Errorf("expected error message mentioning missing file, got:\n%s", output)
	}
}

// TestE2E_RunWithCtxShortFlag verifies -c short flag works.
func TestE2E_RunWithCtxShortFlag(t *testing.T) {
	skipIfNoE2E(t)

	var recordedBody []byte
	llmSrv := httptest.NewServer(captureLLMHandler(&recordedBody))
	defer llmSrv.Close()

	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origHome := os.Getenv("HOME")
	odekBaseURL := os.Getenv("ODEK_BASE_URL")

	t.Cleanup(func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("HOME", origHome)
		os.Setenv("ODEK_BASE_URL", odekBaseURL)
	})

	os.Setenv("DEEPSEEK_API_KEY", "sk-mock-refs")
	os.Setenv("ODEK_BASE_URL", llmSrv.URL)
	homeDir := t.TempDir()
	os.Setenv("HOME", homeDir)
	os.Unsetenv("ODEK_SYSTEM")

	workDir := t.TempDir()
	os.WriteFile(filepath.Join(workDir, "short.txt"), []byte("-c flag content"), 0644)

	sessionDir := filepath.Join(homeDir, ".odek", "sessions")
	os.MkdirAll(sessionDir, 0700)

	if e2eBinary == "" {
		t.Skip("e2eBinary not set — run TestMain first")
	}

	cmd := exec.Command(e2eBinary, "run", "-c", "short.txt", "--model", "deepseek-chat", "analyze with short flag")
	cmd.Dir = workDir

	stderr := &strings.Builder{}
	cmd.Stderr = stderr

	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out")
	}

	if !strings.Contains(stderr.String(), "odek: attached 1 file(s)") {
		t.Error("missing attached message for -c flag")
	}
	if len(recordedBody) == 0 {
		t.Fatal("no LLM request received")
	}
	if !strings.Contains(string(recordedBody), "-c flag content") {
		t.Errorf("-c file content missing from LLM request")
	}
}

// TestE2E_RunWithMultipleCtxFiles verifies multiple --ctx files.
func TestE2E_RunWithMultipleCtxFiles(t *testing.T) {
	skipIfNoE2E(t)

	var recordedBody []byte
	llmSrv := httptest.NewServer(captureLLMHandler(&recordedBody))
	defer llmSrv.Close()

	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origHome := os.Getenv("HOME")
	odekBaseURL := os.Getenv("ODEK_BASE_URL")

	t.Cleanup(func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("HOME", origHome)
		os.Setenv("ODEK_BASE_URL", odekBaseURL)
	})

	os.Setenv("DEEPSEEK_API_KEY", "sk-mock-refs")
	os.Setenv("ODEK_BASE_URL", llmSrv.URL)
	homeDir := t.TempDir()
	os.Setenv("HOME", homeDir)
	os.Unsetenv("ODEK_SYSTEM")

	workDir := t.TempDir()
	os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("file A"), 0644)
	os.WriteFile(filepath.Join(workDir, "b.txt"), []byte("file B"), 0644)

	sessionDir := filepath.Join(homeDir, ".odek", "sessions")
	os.MkdirAll(sessionDir, 0700)

	if e2eBinary == "" {
		t.Skip("e2eBinary not set — run TestMain first")
	}

	cmd := exec.Command(e2eBinary, "run", "--ctx", "a.txt,b.txt", "--model", "deepseek-chat", "compare both files")
	cmd.Dir = workDir

	stderr := &strings.Builder{}
	cmd.Stderr = stderr

	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out")
	}

	if !strings.Contains(stderr.String(), "odek: attached 2 file(s)") {
		t.Error("missing attached 2 file(s) message")
	}
	if len(recordedBody) == 0 {
		t.Fatal("no LLM request received")
	}
	bodyStr := string(recordedBody)
	if !strings.Contains(bodyStr, "file A") || !strings.Contains(bodyStr, "file B") {
		t.Errorf("both file contents expected in LLM request:\n%s", bodyStr)
	}
}

// Ensure json import is used
var _ = json.Marshal
