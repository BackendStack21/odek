package render

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// hostile carries the sequences a fetched page or a model can use to rewrite
// what the operator sees: CSI cursor/erase, OSC title/hyperlink, an 8-bit
// CSI, a carriage return overwrite, backspace, a bidi override and a tag
// character (invisible ASCII smuggling).
const hostile = "ok\x1b[2K\x1b[1Arm -rf /\x1b]0;pwned\x07\x1b]8;;http://evil/\x1b\\link\u009b31m\rfake\bX‮evil\U000E0041"

func assertNoRawControls(t *testing.T, where, out string) {
	t.Helper()
	for _, bad := range []string{"\x1b", "\x07", "\r", "\b", "\u009b", "‮", "\U000E0041"} {
		if strings.Contains(out, bad) {
			t.Errorf("%s: output contains raw %+q: %+q", where, bad, out)
		}
	}
}

// Model- and tool-sourced text must reach the terminal with control, bidi
// and invisible characters escaped, so an ANSI/OSC sequence in a fetched page
// cannot rewrite the transcript.
func TestRED_RendererEscapesTerminalControls(t *testing.T) {
	cases := map[string]func(r *Renderer){
		"ToolResult":      func(r *Renderer) { r.ToolResult(hostile) },
		"ToolCall":        func(r *Renderer) { r.ToolCall("shell", hostile) },
		"Thinking":        func(r *Renderer) { r.Thinking(hostile) },
		"FinalAnswer":     func(r *Renderer) { r.FinalAnswer(hostile) },
		"Note":            func(r *Renderer) { r.Note(hostile) },
		"StreamContent":   func(r *Renderer) { r.StreamContent(hostile) },
		"StreamReasoning": func(r *Renderer) { r.StreamReasoning(hostile) },
		"NarratorMessage": func(r *Renderer) { r.NarratorMessage(hostile) },
		"Error":           func(r *Renderer) { r.Error(errors.New(hostile)) },
		"MemoryFact":      func(r *Renderer) { r.memoryVerbose = true; r.MemoryFact("added", "user", hostile) },
		"MemoryEpisode":   func(r *Renderer) { r.memoryVerbose = true; r.MemoryEpisode("stored", hostile) },
		"ToolRecovery":    func(r *Renderer) { r.memoryVerbose = true; r.ToolRecovery(hostile, hostile) },
		"SkillLoaded":     func(r *Renderer) { r.skillVerbose = true; r.SkillLoaded([]string{hostile}) },
	}
	for name, fn := range cases {
		var buf bytes.Buffer
		fn(New(&buf, false))
		out := buf.String()
		if out == "" {
			t.Errorf("%s: printed nothing", name)
			continue
		}
		assertNoRawControls(t, name, out)
		if !strings.Contains(out, `\x1b`) && name != "ToolRecovery" && name != "MemoryFact" {
			t.Errorf("%s: ESC not shown as a visible escape: %+q", name, out)
		}
	}
}

// An ESC (or a multibyte C1 control) split across two stream fragments must
// not leak, and an ordinary multibyte character split across fragments must
// arrive intact.
func TestStreamSplitAcrossChunks(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, false)
	r.StreamContent("a\x1b")
	r.StreamContent("[2Kb\xc2")
	r.StreamContent("\x9bc h\xc3")
	r.StreamContent("\xa9llo 👍")
	out := buf.String()
	assertNoRawControls(t, "stream", out)
	if want := `a\x1b[2Kb\u009bc héllo 👍`; out != want {
		t.Fatalf("stream = %+q, want %+q", out, want)
	}
}

// A fragment held back at a kind switch or iteration boundary is flushed as
// an escape, never dropped and never emitted raw.
func TestStreamCarryFlushedAtBoundaries(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, false)
	r.StreamReasoning("think\xe2\x80")
	r.StreamContent("answer")
	out := buf.String()
	if !strings.Contains(out, `think\xe2\x80`) || !strings.Contains(out, "answer") {
		t.Fatalf("carry not flushed at kind switch: %+q", out)
	}
	buf.Reset()
	r.StreamContent("tail\xf0\x9f")
	r.SetStreamedOutput(false)
	if got := buf.String(); got != `tail\xf0\x9f` {
		t.Fatalf("carry not flushed at boundary: %+q", got)
	}
}

// Layout and ordinary text survive: newlines and tabs in multi-line output,
// emoji (including ZWJ sequences), accents and non-ASCII spaces.
func TestEscapeTerminalKeepsOrdinaryText(t *testing.T) {
	in := "line one\n\tindented — café 👨‍👩‍👧 ok fine ✅"
	if got := escapeTerminal(in, true); got != in {
		t.Fatalf("escapeTerminal(layout) = %+q, want unchanged", got)
	}
	if got := escapeTerminal("a\nb\tc", false); got != `a\nb\tc` {
		t.Fatalf("escapeTerminal(inline) = %+q", got)
	}
	// A zero-width joiner outside an emoji sequence is escaped.
	if got := escapeTerminal("ad\u200dmin", true); got != `ad\u200dmin` {
		t.Fatalf("bare ZWJ = %+q", got)
	}
	if got := escapeTerminal("bad\xffbyte\x7f", true); got != `bad\xffbyte\x7f` {
		t.Fatalf("invalid/DEL = %+q", got)
	}
}

func BenchmarkEscapeTerminal(b *testing.B) {
	s := strings.Repeat("plain ascii text with some ünïcode and \x1b[31m colour ", 200)
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = escapeTerminal(s, true)
	}
}
