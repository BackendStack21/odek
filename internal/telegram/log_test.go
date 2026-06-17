package telegram

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// NewFileLogger
// ---------------------------------------------------------------------------

func TestNewFileLogger_stderr(t *testing.T) {
	l := NewFileLogger(LogDebug, "")
	if l == nil {
		t.Fatal("NewFileLogger returned nil")
	}
	fl, ok := l.(*fileLogger)
	if !ok {
		t.Fatalf("expected *fileLogger, got %T", l)
	}
	if fl.file != os.Stderr {
		t.Error("expected stderr for empty path")
	}
	if fl.level != LogDebug {
		t.Errorf("level = %d, want LogDebug", fl.level)
	}
}

func TestNewFileLogger_filePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	l := NewFileLogger(LogInfo, path)
	if l == nil {
		t.Fatal("NewFileLogger returned nil")
	}
	fl, ok := l.(*fileLogger)
	if !ok {
		t.Fatalf("expected *fileLogger, got %T", l)
	}
	if fl.file == nil || fl.file == os.Stderr {
		t.Error("expected a real file, not stderr")
	}
	fl.file.Close()

	// Verify file exists.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("log file was not created")
	}

	// Verify permissions are not group/other-readable.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat log file: %v", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Errorf("log file permissions = %04o, want no group/other bits", info.Mode().Perm())
	}
}

func TestNewFileLogger_hardensExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.log")

	// Simulate an existing world-readable log file from an earlier version.
	if err := os.WriteFile(path, []byte("old log\n"), 0644); err != nil {
		t.Fatalf("create existing file: %v", err)
	}

	l := NewFileLogger(LogInfo, path)
	fl := l.(*fileLogger)
	fl.file.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat log file: %v", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Errorf("existing log file permissions = %04o, want no group/other bits", info.Mode().Perm())
	}
}

func TestNewFileLogger_invalidPath(t *testing.T) {
	// An invalid path should fall back to stderr without panicking.
	l := NewFileLogger(LogDebug, "/nonexistent/dir/log.log")
	if l == nil {
		t.Fatal("NewFileLogger returned nil")
	}
	fl, ok := l.(*fileLogger)
	if !ok {
		t.Fatalf("expected *fileLogger, got %T", l)
	}
	if fl.file != os.Stderr {
		t.Error("expected fallback to stderr on invalid path")
	}
}

// ---------------------------------------------------------------------------
// NewNopLogger
// ---------------------------------------------------------------------------

func TestNewNopLogger(t *testing.T) {
	l := NewNopLogger()
	if l == nil {
		t.Fatal("NewNopLogger returned nil")
	}
	// Should not panic on any call.
	l.Debug("debug")
	l.Info("info")
	l.Warn("warn")
	l.Error("error")
	child := l.With("key", "val")
	child.Info("child")
}

// ---------------------------------------------------------------------------
// Level filtering
// ---------------------------------------------------------------------------

func TestFileLogger_levelFiltering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "filter.log")

	l := NewFileLogger(LogError, path)
	l.Info("should be filtered")
	l.Error("should appear")

	fl := l.(*fileLogger)
	fl.file.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	content := string(data)

	if strings.Contains(content, "should be filtered") {
		t.Error("Info message was not filtered out at LogError level")
	}
	if !strings.Contains(content, "should appear") {
		t.Error("Error message was missing at LogError level")
	}
}

func TestFileLogger_levelFiltering_Debug(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "filter_debug.log")

	l := NewFileLogger(LogDebug, path)
	l.Debug("debug msg")
	l.Info("info msg")

	fl := l.(*fileLogger)
	fl.file.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "debug msg") {
		t.Error("Debug message was filtered at LogDebug level")
	}
	if !strings.Contains(content, "info msg") {
		t.Error("Info message was filtered at LogDebug level")
	}
}

func TestFileLogger_levelFiltering_WarnOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "filter_warn.log")

	l := NewFileLogger(LogWarn, path)
	l.Info("should be filtered")
	l.Debug("should be filtered")
	l.Warn("warn msg")
	l.Error("error msg")

	fl := l.(*fileLogger)
	fl.file.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	content := string(data)

	if strings.Contains(content, "should be filtered") {
		t.Error("Info/Debug messages were not filtered at LogWarn level")
	}
	if !strings.Contains(content, "warn msg") {
		t.Error("Warn message was missing at LogWarn level")
	}
	if !strings.Contains(content, "error msg") {
		t.Error("Error message was missing at LogWarn level")
	}
}

// ---------------------------------------------------------------------------
// Field formatting
// ---------------------------------------------------------------------------

func TestFileLogger_fieldFormatting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "format.log")

	l := NewFileLogger(LogInfo, path)
	l.Info("test message", "chat_id", 42, "error", "something went wrong")

	fl := l.(*fileLogger)
	fl.file.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	content := string(data)

	// Check format: timestamp [LEVEL] telegram: msg key=value
	if !strings.Contains(content, "[INFO]") {
		t.Error("missing [INFO] level tag")
	}
	if !strings.Contains(content, "telegram: test message") {
		t.Error("missing message text")
	}
	if !strings.Contains(content, "chat_id=42") {
		t.Error("missing chat_id field")
	}
	if !strings.Contains(content, "error=something went wrong") {
		t.Error("missing error field")
	}
}

func TestFileLogger_fieldFormatting_missingValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.log")

	l := NewFileLogger(LogInfo, path)
	l.Info("odd fields", "key1", "val1", "key2") // odd number — key2 is missing

	fl := l.(*fileLogger)
	fl.file.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "key2=(missing)") {
		t.Errorf("missing value should be marked as (missing), got: %s", content)
	}
}

func TestFileLogger_fieldFormatting_nonStringKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonstring.log")

	l := NewFileLogger(LogInfo, path)
	l.Info("non-string key", 42, "value") // non-string key

	fl := l.(*fileLogger)
	fl.file.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "42=value") {
		t.Errorf("non-string key should be formatted as-is, got: %s", content)
	}
}

// ---------------------------------------------------------------------------
// Thread safety
// ---------------------------------------------------------------------------

func TestFileLogger_concurrentWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "concurrent.log")

	l := NewFileLogger(LogDebug, path)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			l.Info("concurrent", "goroutine", n)
		}(i)
	}
	wg.Wait()

	fl := l.(*fileLogger)
	fl.file.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 20 {
		t.Errorf("expected 20 log lines, got %d", len(lines))
	}
}

// ---------------------------------------------------------------------------
// With — child logger
// ---------------------------------------------------------------------------

func TestFileLogger_With(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "with.log")

	l := NewFileLogger(LogInfo, path)

	child := l.With("component", "bot")
	child.Info("child message", "chat_id", 42)

	fl := l.(*fileLogger)
	fl.file.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "component=bot") {
		t.Error("child logger missing With() fields")
	}
	if !strings.Contains(content, "chat_id=42") {
		t.Error("child logger missing call-site fields")
	}
}

func TestFileLogger_With_chaining(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "with_chain.log")

	l := NewFileLogger(LogInfo, path)

	l1 := l.With("layer1", "a")
	l2 := l1.With("layer2", "b")
	l2.Info("chained")

	fl := l.(*fileLogger)
	fl.file.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "layer1=a") {
		t.Error("missing layer1 field from chained With()")
	}
	if !strings.Contains(content, "layer2=b") {
		t.Error("missing layer2 field from chained With()")
	}
}

// ---------------------------------------------------------------------------
// ParseLogLevel
// ---------------------------------------------------------------------------

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		input string
		want  LogLevel
	}{
		{"debug", LogDebug},
		{"info", LogInfo},
		{"warn", LogWarn},
		{"error", LogError},
		{"INFO", LogInfo},
		{"WARN", LogWarn},
		{"", LogInfo},
		{"unknown", LogInfo},
	}

	for _, tt := range tests {
		got := ParseLogLevel(tt.input)
		if got != tt.want {
			t.Errorf("ParseLogLevel(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
