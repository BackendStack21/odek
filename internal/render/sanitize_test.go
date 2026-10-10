package render

import (
	"bytes"
	"errors"
	"fmt"
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

// Subdivision flags are emoji tag sequences: U+1F3F4, lowercase/digit tag
// characters and a cancel tag. They print unchanged; tag characters anywhere
// else are an invisible-text channel and stay escaped.
func TestRED_EscapeTerminalKeepsEmojiTagSequences(t *testing.T) {
	england := "\U0001F3F4\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F"
	scotland := "\U0001F3F4\U000E0067\U000E0062\U000E0073\U000E0063\U000E0074\U000E007F"
	in := "go " + england + " and " + scotland + "!"
	if got := escapeTerminal(in, true); got != in {
		t.Fatalf("flag tag sequence escaped: %+q", got)
	}
	// Streaming rune by rune gives the same result.
	var buf bytes.Buffer
	r := New(&buf, false)
	for _, c := range in {
		r.StreamContent(string(c))
	}
	if buf.String() != in {
		t.Fatalf("streamed flag escaped: %+q", buf.String())
	}

	cases := map[string]string{
		// Bare tag characters smuggle invisible ASCII.
		"hi\U000E0069\U000E0067\U000E006E": `hi\U000e0069\U000e0067\U000e006e`,
		// A cancel tag with no spec, and after a closed sequence.
		"\U0001F3F4\U000E007F": "\U0001F3F4" + `\U000e007f`,
		england + "\U000E0061": england + `\U000e0061`,
		// Uppercase and space tags are not subdivision codes.
		"\U0001F3F4\U000E0041\U000E0020": "\U0001F3F4" + `\U000e0041\U000e0020`,
		// After another emoji the tags do not continue a flag.
		"✅\U000E0067\U000E0062": "✅" + `\U000e0067\U000e0062`,
		// A spec longer than any subdivision code is escaped in full.
		"\U0001F3F4" + strings.Repeat("\U000E0061", 9): "\U0001F3F4" + strings.Repeat(`\U000e0061`, 9),
	}
	for in, want := range cases {
		if got := escapeTerminal(in, true); got != want {
			t.Errorf("escapeTerminal(%+q) = %+q, want %+q", in, got, want)
		}
	}
}

// tagged spells ascii in tag characters.
func tagged(ascii string) string {
	var b strings.Builder
	for _, c := range ascii {
		b.WriteRune(0xE0000 + c)
	}
	return b.String()
}

// escapedTags is what escapeTerminal prints for tagged(ascii).
func escapedTags(ascii string) string {
	var b strings.Builder
	for _, c := range ascii {
		fmt.Fprintf(&b, `\U%08x`, 0xE0000+c)
	}
	return b.String()
}

// Only the RGI subdivision flags (England, Scotland, Wales) print raw. Any
// other tag run after a black flag — terminated or not — is escaped whole, so
// repeated flags cannot carry hidden text a few characters at a time.
func TestRED_EscapeTerminalTagRunsOutsideRGIEscaped(t *testing.T) {
	const flag = "\U0001F3F4"
	cancel := "\U000E007F"
	cases := map[string]string{
		flag + tagged("abc") + cancel:                                    flag + escapedTags("abc") + `\U000e007f`,
		flag + tagged("hidden") + cancel + flag + tagged("txt") + cancel: flag + escapedTags("hidden") + `\U000e007f` + flag + escapedTags("txt") + `\U000e007f`,
		flag + tagged("gbeng") + "x":                                     flag + escapedTags("gbeng") + "x",
		flag + tagged("gbeng"):                                           flag + escapedTags("gbeng"),
		flag + tagged("gbsct") + cancel + "!":                            flag + tagged("gbsct") + cancel + "!",
		flag + tagged("gbwls") + cancel:                                  flag + tagged("gbwls") + cancel,
	}
	for in, want := range cases {
		if got := escapeTerminal(in, true); got != want {
			t.Errorf("escapeTerminal(%+q) = %+q, want %+q", in, got, want)
		}
	}
	// Streamed rune by rune: a held run is flushed escaped at the boundary,
	// and a valid flag still arrives raw.
	for in, want := range map[string]string{
		flag + tagged("gbeng"):          flag + escapedTags("gbeng"),
		flag + tagged("gbeng") + cancel: flag + tagged("gbeng") + cancel,
		flag + tagged("usca") + cancel:  flag + escapedTags("usca") + `\U000e007f`,
	} {
		var buf bytes.Buffer
		r := New(&buf, false)
		for _, c := range in {
			r.StreamContent(string(c))
		}
		r.SetStreamedOutput(false)
		if buf.String() != want {
			t.Errorf("streamed %+q = %+q, want %+q", in, buf.String(), want)
		}
	}
}
