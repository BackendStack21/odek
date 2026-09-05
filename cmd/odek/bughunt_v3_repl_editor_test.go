package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// swapStdin replaces os.Stdin with r for the duration of the test and
// restores it afterwards. captureStderr is provided by sandbox_test.go.
func swapStdin(t *testing.T, r io.Reader) {
	t.Helper()
	old := os.Stdin
	os.Stdin = r.(*os.File)
	t.Cleanup(func() { os.Stdin = old })
}

// Bug 1: parameterized CSI escapes (e.g. \x1b[3;5~ ctrl+Delete) leak their
// parameter tail ('5', '~') into the edit buffer as printable text.
func TestEditorCSIParamTailNotInserted(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	swapStdin(t, r)
	go func() {
		w.Write([]byte("\x1b[3;5~"))
		w.Close()
	}()
	e := newReplEditor("> ", nil)
	// handleByte(0x1b) triggers handleEscape, which pulls the rest of the
	// sequence from stdin itself.
	e.handleByte(0x1b)
	if len(e.line) != 0 {
		t.Fatalf("expected empty line after \\x1b[3;5~, got %q", string(e.line))
	}
	// also the ctrl+Right form \x1b[1;5C
	r2, w2, _ := os.Pipe()
	defer r2.Close()
	swapStdin(t, r2)
	go func() {
		w2.Write([]byte("\x1b[1;5C"))
		w2.Close()
	}()
	e2 := newReplEditor("> ", nil)
	e2.handleByte(0x1b)
	if len(e2.line) != 0 {
		t.Fatalf("expected empty line after \\x1b[1;5C, got %q", string(e2.line))
	}
}

// Bug 2: cursor math must use display columns (wide runes = 2), not rune counts.
func TestEditorDisplayWidth(t *testing.T) {
	if got := displayWidth([]rune("你")); got != 2 {
		t.Fatalf("displayWidth(\"你\") = %d, want 2", got)
	}
	if got := displayWidth([]rune("ab")); got != 2 {
		t.Fatalf("displayWidth(\"ab\") = %d, want 2", got)
	}
	if got := displayWidth(nil); got != 0 {
		t.Fatalf("displayWidth(empty) = %d, want 0", got)
	}
	if got := displayWidth([]rune("a你b")); got != 4 {
		t.Fatalf("displayWidth(\"a你b\") = %d, want 4", got)
	}
}

// Bug 3: the typed-but-unsent draft is lost after historyPrev then historyNext.
func TestEditorHistoryDraftPreserved(t *testing.T) {
	e := newReplEditor("> ", nil)
	e.history.Add("first")
	e.history.Add("second")
	e.line = []rune("draft")
	e.pos = len(e.line)

	e.historyPrev() // -> "second"
	if string(e.line) != "second" {
		t.Fatalf("after historyPrev: %q", string(e.line))
	}
	e.historyNext() // past newest -> draft restored
	if string(e.line) != "draft" {
		t.Fatalf("draft not restored after historyPrev+historyNext, got %q", string(e.line))
	}
}

// Bug 4: Ctrl+C must disable bracketed paste mode before bailing. Pipes
// cannot enter raw mode (ReadLine falls back to scanner), so drive the ^C
// byte handler directly and inspect stderr.
func TestReadLineCtrlCDisablesBracketedPaste(t *testing.T) {
	readStderr := captureStderr(t)
	e := newReplEditor("> ", nil)
	e.bracketed = true
	_, _ = e.handleByte(0x03)
	out := readStderr()
	if !strings.Contains(out, "\x1b[?2004l") {
		t.Fatalf("stderr missing bracketed-paste disable after ^C: %q", out)
	}
	// same for Ctrl+D
	readStderr2 := captureStderr(t)
	e2 := newReplEditor("> ", nil)
	e2.bracketed = true
	e2.handleByte(0x04)
	if out := readStderr2(); !strings.Contains(out, "\x1b[?2004l") {
		t.Fatalf("stderr missing bracketed-paste disable after ^D: %q", out)
	}
}

// keep bytes import used (buffered writer for future tests)
var _ = bytes.MinRead
