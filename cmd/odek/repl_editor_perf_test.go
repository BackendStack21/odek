package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type countingWriter struct {
	writes int
	buf    strings.Builder
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.writes++
	return c.buf.Write(p)
}

func TestRED_Repl_BracketedPasteRedrawsOnce(t *testing.T) {
	out := &countingWriter{}
	e := &replEditor{prompt: "> ", out: out}
	e.bracketed = true
	text := strings.Repeat("abcdefgh", 1024) // 8 KB
	for _, r := range text {
		e.insertRune(r)
	}
	if out.writes != 0 {
		t.Fatalf("%d writes during paste, want none until the paste ends", out.writes)
	}
	e.endPaste()
	if out.writes != 1 {
		t.Fatalf("%d writes at paste end, want exactly 1", out.writes)
	}
	if !strings.Contains(out.buf.String(), text) {
		t.Fatal("final redraw does not contain the pasted text")
	}
	if string(e.line) != text || e.pos != len(e.line) {
		t.Fatal("buffer or cursor wrong after paste")
	}
}

func TestPasteWithNewlinesDrawsAtEnd(t *testing.T) {
	out := &countingWriter{}
	e := &replEditor{prompt: "> ", out: out}
	e.bracketed = true
	for _, r := range "ab" {
		e.insertRune(r)
	}
	if done, err := e.handleEnter(); done || err != nil {
		t.Fatalf("enter in paste mode must not submit: %v %v", done, err)
	}
	e.insertRune('c')
	e.endPaste()
	if string(e.line) != "ab\nc" {
		t.Fatalf("line=%q", string(e.line))
	}
	if !strings.Contains(out.buf.String(), "ab\nc") {
		t.Fatalf("redraw missing pasted text: %q", out.buf.String())
	}
}

func TestTypingStillRedrawsEveryKey(t *testing.T) {
	out := &countingWriter{}
	e := &replEditor{prompt: "> ", out: out}
	for _, r := range "abc" {
		e.insertRune(r)
	}
	if out.writes != 3 {
		t.Fatalf("writes=%d, want 3", out.writes)
	}
}

func TestRED_Repl_HistoryAddAppendsInsteadOfRewriting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedHistory(t, home)
	h := newReplHistory()
	h.Load(filepath.Join(home, ".odek", historyFilename))
	before := historyFullRewrites
	for i := 0; i < 50; i++ {
		h.Add("cmd " + strings.Repeat("x", i))
	}
	if n := historyFullRewrites - before; n > 1 {
		t.Fatalf("%d full history rewrites for 50 Adds, want at most 1", n)
	}
	data, err := os.ReadFile(filepath.Join(home, ".odek", historyFilename))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 50 || lines[49] != "cmd "+strings.Repeat("x", 49) {
		t.Fatalf("history file has %d lines", len(lines))
	}
	// A fresh history loads exactly what was written.
	h2 := newReplHistory()
	h2.Load(filepath.Join(home, ".odek", historyFilename))
	if len(h2.entries) != 50 {
		t.Fatalf("reloaded %d entries", len(h2.entries))
	}
}

func TestHistoryTrimmedRewritesAndStaysBounded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedHistory(t, home)
	h := newReplHistory()
	h.max = 5
	h.Load(filepath.Join(home, ".odek", historyFilename))
	for i := 0; i < 12; i++ {
		h.Add("line " + string(rune('a'+i)))
	}
	data, _ := os.ReadFile(filepath.Join(home, ".odek", historyFilename))
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 5 || lines[0] != "line h" || lines[4] != "line l" {
		t.Fatalf("lines=%v", lines)
	}
	if info, err := os.Stat(filepath.Join(home, ".odek", historyFilename)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("history perms: %v %v", info, err)
	}
}

func seedHistory(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".odek")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, historyFilename), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}
