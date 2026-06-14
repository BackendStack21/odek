package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSendMessageTool_Name(t *testing.T) {
	tool := &SendMessageTool{}
	if tool.Name() != "send_message" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "send_message")
	}
}

func TestSendMessageTool_Description(t *testing.T) {
	tool := &SendMessageTool{}
	desc := tool.Description()
	if !strings.Contains(desc, "send_message") && !strings.Contains(desc, "intermediate") {
		t.Errorf("Description should mention send_message, got: %q", desc)
	}
}

func TestSendMessageTool_Schema(t *testing.T) {
	tool := &SendMessageTool{}
	schema := tool.Schema().(map[string]any)
	if schema["type"] != "object" {
		t.Error("schema type must be object")
	}
	props := schema["properties"].(map[string]any)
	for _, key := range []string{"text", "file", "buttons"} {
		if _, ok := props[key]; !ok {
			t.Errorf("schema missing property: %s", key)
		}
	}
	req := toStringSlice(schema["required"])
	if !contains(req, "text") {
		t.Error("schema missing required: text")
	}
}

func TestSendMessageTool_Call_TextOnly(t *testing.T) {
	var sentText, sentFile string
	tool := &SendMessageTool{
		Sender: func(text, file string, buttons [][]map[string]string) error {
			sentText = text
			sentFile = file
			return nil
		},
	}

	result, err := tool.Call(`{"text": "Hello world"}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if !strings.Contains(result, "message sent") {
		t.Errorf("expected 'message sent' in result, got: %q", result)
	}
	if sentText != "Hello world" {
		t.Errorf("text = %q, want 'Hello world'", sentText)
	}
	if sentFile != "" {
		t.Errorf("file should be empty, got %q", sentFile)
	}
}

func TestSendMessageTool_Call_WithFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.png")
	os.WriteFile(f, []byte("fake png"), 0644)

	var sentFile string
	tool := &SendMessageTool{
		Sender: func(text, file string, buttons [][]map[string]string) error {
			sentFile = file
			return nil
		},
	}

	result, err := tool.Call(fmt.Sprintf(`{"text": "check this", "file": %q}`, f))
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if !strings.Contains(result, "file sent") {
		t.Errorf("expected 'file sent' in result, got: %q", result)
	}
	want, err := filepath.EvalSymlinks(f)
	if err != nil {
		t.Fatalf("EvalSymlinks failed: %v", err)
	}
	if sentFile != want {
		t.Errorf("file = %q, want %q", sentFile, want)
	}
}

func TestSendMessageTool_Call_FileNotFound(t *testing.T) {
	tool := &SendMessageTool{
		Sender: func(text, file string, buttons [][]map[string]string) error {
			return nil
		},
	}

	_, err := tool.Call(`{"text": "x", "file": "/nonexistent/file.png"}`)
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	if !strings.Contains(err.Error(), "file not found") {
		t.Errorf("expected 'file not found', got: %v", err)
	}
}

func TestSendMessageTool_Call_RelativePath(t *testing.T) {
	tool := &SendMessageTool{
		Sender: func(text, file string, buttons [][]map[string]string) error {
			return nil
		},
	}

	_, err := tool.Call(`{"text": "x", "file": "relative/path.png"}`)
	if err == nil {
		t.Fatal("expected error for relative path")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("expected 'absolute', got: %v", err)
	}
}

func TestSendMessageTool_Call_WithButtons(t *testing.T) {
	var sentButtons [][]map[string]string
	tool := &SendMessageTool{
		Sender: func(text, file string, buttons [][]map[string]string) error {
			sentButtons = buttons
			return nil
		},
	}

	result, err := tool.Call(`{
		"text": "Choose:",
		"buttons": [
			[{"text": "Yes", "callback_data": "cb:yes"}],
			[{"text": "No", "callback_data": "cb:no"}]
		]
	}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if !strings.Contains(result, "buttons sent") {
		t.Errorf("expected 'buttons sent' in result, got: %q", result)
	}
	if len(sentButtons) != 2 {
		t.Fatalf("expected 2 button rows, got %d", len(sentButtons))
	}
	if sentButtons[0][0]["text"] != "Yes" {
		t.Errorf("button[0][0].text = %q", sentButtons[0][0]["text"])
	}
}

func TestSendMessageTool_Call_ButtonCallbackPrefix(t *testing.T) {
	// Buttons without the cb: prefix should get it auto-added.
	var sentButtons [][]map[string]string
	tool := &SendMessageTool{
		Sender: func(text, file string, buttons [][]map[string]string) error {
			sentButtons = buttons
			return nil
		},
	}

	_, err := tool.Call(`{
		"text": "Pick:",
		"buttons": [[{"text": "Go", "callback_data": "go"}]]
	}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	cd := sentButtons[0][0]["callback_data"]
	if !strings.HasPrefix(cd, "cb:") {
		t.Errorf("expected cb: prefix on callback_data, got: %q", cd)
	}
}

func TestSendMessageTool_Call_ReservedCallbackPrefixRejected(t *testing.T) {
	tool := &SendMessageTool{
		Sender: func(text, file string, buttons [][]map[string]string) error {
			return nil
		},
	}

	for _, prefix := range ReservedCallbackPrefixes {
		args := fmt.Sprintf(`{"text": "x", "buttons": [[{"text": "Bad", "callback_data": "%sfoo"}]]}`, prefix)
		_, err := tool.Call(args)
		if err == nil {
			t.Errorf("expected error for reserved prefix %q, got nil", prefix)
			continue
		}
		if !strings.Contains(err.Error(), "reserved internal prefix") {
			t.Errorf("expected 'reserved internal prefix' error for %q, got: %v", prefix, err)
		}
	}
}

func TestSendMessageTool_Call_NoSender(t *testing.T) {
	tool := &SendMessageTool{}
	_, err := tool.Call(`{"text": "hi"}`)
	if err == nil || !strings.Contains(err.Error(), "Sender not configured") {
		t.Errorf("expected 'Sender not configured' error, got: %v", err)
	}
}

func TestSendMessageTool_Call_SenderError(t *testing.T) {
	tool := &SendMessageTool{
		Sender: func(text, file string, buttons [][]map[string]string) error {
			return fmt.Errorf("network down")
		},
	}
	_, err := tool.Call(`{"text": "hi"}`)
	if err == nil || !strings.Contains(err.Error(), "network down") {
		t.Errorf("expected sender error to propagate, got: %v", err)
	}
}

func TestSendMessageTool_Call_InvalidJSON(t *testing.T) {
	tool := &SendMessageTool{
		Sender: func(text, file string, buttons [][]map[string]string) error {
			return nil
		},
	}
	_, err := tool.Call(`not json`)
	if err == nil || !strings.Contains(err.Error(), "parse args") {
		t.Errorf("expected parse error, got: %v", err)
	}
}

func TestNewSendMessageTool(t *testing.T) {
	sender := func(text, file string, buttons [][]map[string]string) error { return nil }
	tool := NewSendMessageTool(sender)
	if tool == nil {
		t.Fatal("NewSendMessageTool returned nil")
	}
	if tool.Sender == nil {
		t.Error("Sender should be set")
	}
}

// ── Helpers ────────────────────────────────────────────────────────────

func toStringSlice(v any) []string {
	switch vs := v.(type) {
	case []string:
		return vs
	case []any:
		s := make([]string, len(vs))
		for i, x := range vs {
			s[i] = x.(string)
		}
		return s
	}
	return nil
}

func contains(s []string, item string) bool {
	for _, v := range s {
		if v == item {
			return true
		}
	}
	return false
}
