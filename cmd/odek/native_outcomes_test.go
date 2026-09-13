package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/tool"
)

func TestNativeOutcomeDistinguishesErrorLogsFromFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "errors.log")
	if err := os.WriteFile(path, []byte(`{"error":"ordinary log content"}`), 0600); err != nil {
		t.Fatal(err)
	}
	read := &readFileTool{}
	result, err := read.Call(securityProbeArgs(map[string]string{"path": path}))
	if err != nil || !strings.Contains(result, "ordinary log content") {
		t.Fatalf("log read: %s / %v", result, err)
	}
	result, err = read.Call(securityProbeArgs(map[string]string{"path": path + ".missing"}))
	var failure *tool.PermanentError
	if !errors.As(err, &failure) || !strings.Contains(result, "file not found") {
		t.Fatalf("missing read: %s / %v", result, err)
	}
	batch := &batchReadTool{}
	result, err = batch.Call(securityProbeArgs(map[string]any{"files": []map[string]string{{"path": path}, {"path": path + ".missing"}}}))
	if !errors.As(err, &failure) || !strings.Contains(result, "ordinary log content") || !strings.Contains(result, "file not found") {
		t.Fatalf("partial batch: %s / %v", result, err)
	}
}
