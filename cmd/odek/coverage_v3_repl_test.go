package main

// Coverage v3 — repl editor residual gaps:
//   - readTildeOrBracketed: parameterized-CSI consumption (0% baseline).
//     os.Stdin is swapped for a pipe feeding the exact byte tail the real
//     terminal would deliver after \x1b[<digit>.
//   - runeWidth: range table over control / wide / emoji / latin runes.
//   - historyNext: draft-restore branch when Next() returns nil.

import (
	"io"
	"os"
	"testing"
)

// withStdinPipe replaces os.Stdin with a pipe pre-loaded with the given
// bytes (the writer end is closed so reads drain then EOF).
func withStdinPipe(t *testing.T, feed string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, feed); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		r.Close()
	})
}

func TestReadTildeOrBracketed_Branches(t *testing.T) {
	t.Run("tilde final", func(t *testing.T) {
		withStdinPipe(t, "~")
		e := newReplEditor("p", nil)
		e.line = []rune("kept")
		e.readTildeOrBracketed("2")
		if string(e.line) != "kept" {
			t.Fatalf("line = %q, want untouched", string(e.line))
		}
		if e.bracketed {
			t.Fatal("tilde sequence must not toggle bracketed paste")
		}
	})

	t.Run("bracketed start 200~", func(t *testing.T) {
		withStdinPipe(t, "00~")
		e := newReplEditor("p", nil)
		e.readTildeOrBracketed("2")
		if !e.bracketed {
			t.Fatal("want bracketed=true after \\x1b[200~")
		}
	})

	t.Run("bracketed end 201~", func(t *testing.T) {
		withStdinPipe(t, "01~")
		e := newReplEditor("p", nil)
		e.bracketed = true
		e.readTildeOrBracketed("2")
		if e.bracketed {
			t.Fatal("want bracketed=false after \\x1b[201~")
		}
	})

	t.Run("non-matching 0 tail keeps state", func(t *testing.T) {
		withStdinPipe(t, "0X") // neither 00~ nor 01~
		e := newReplEditor("p", nil)
		e.bracketed = true
		e.readTildeOrBracketed("2")
		if !e.bracketed {
			t.Fatal("unrecognized 0-tail must leave bracketed unchanged")
		}
	})

	t.Run("parameterized tilde final", func(t *testing.T) {
		withStdinPipe(t, ";5~") // \x1b[3;5~ ctrl+Delete
		e := newReplEditor("p", nil)
		e.line = []rune("kept")
		e.readTildeOrBracketed("3")
		if string(e.line) != "kept" {
			t.Fatalf("line = %q, want no tail leaked into buffer", string(e.line))
		}
	})

	t.Run("parameterized letter final", func(t *testing.T) {
		withStdinPipe(t, ";5C") // \x1b[1;5C ctrl+Right
		e := newReplEditor("p", nil)
		e.line = []rune("kept")
		e.readTildeOrBracketed("1")
		if string(e.line) != "kept" {
			t.Fatalf("line = %q, want no tail leaked into buffer", string(e.line))
		}
	})

	t.Run("immediate EOF", func(t *testing.T) {
		withStdinPipe(t, "")
		e := newReplEditor("p", nil)
		e.line = []rune("kept")
		e.readTildeOrBracketed("2") // must simply return
		if string(e.line) != "kept" {
			t.Fatalf("line = %q, want untouched", string(e.line))
		}
	})

	t.Run("EOF mid-parameters", func(t *testing.T) {
		withStdinPipe(t, ";") // first param byte consumed, then EOF
		e := newReplEditor("p", nil)
		e.line = []rune("kept")
		e.readTildeOrBracketed("3")
		if string(e.line) != "kept" {
			t.Fatalf("line = %q, want untouched", string(e.line))
		}
	})
}

func TestRuneWidth_Ranges(t *testing.T) {
	cases := []struct {
		r    rune
		want int
	}{
		{0x00, 0},   // NUL
		{0x01, 0},   // control
		{0x1f, 0},   // last control
		{0x20, 1},   // space
		{'a', 1},    // latin
		{0x7e, 1},   // '~'
		{0x7f, 0},   // DEL
		{0x9f, 0},   // C1 control range
		{0xa0, 1},   // first non-control high
		{0x115f, 2}, // Hangul Jamo in
		{0x1160, 1}, // Hangul Jamo out
		{0x2e80, 2}, // CJK radicals in
		{0x303e, 2},
		{0x303f, 1},
		{0x3041, 2}, // Hiragana
		{0x33ff, 2},
		{0x3400, 2}, // CJK ext A
		{0x4dbf, 2},
		{0x4e00, 2}, // CJK unified
		{0x9fff, 2},
		{0xa000, 2}, // Yi
		{0xa4cf, 2},
		{0xa4d0, 1},
		{0xac00, 2}, // Hangul syllables
		{0xd7a3, 2},
		{0xd7a4, 1},
		{0xf900, 2}, // CJK compat
		{0xfaff, 2},
		{0xfb00, 1},
		{0xfe30, 2},
		{0xfe4f, 2},
		{0xfe50, 1},
		{0xff00, 2}, // fullwidth
		{0xff60, 2},
		{0xff61, 1},
		{0xffe0, 2},
		{0xffe6, 2},
		{0xffe7, 1},
		{0x1f300, 2}, // emoji
		{0x1f64f, 2},
		{0x1f650, 1},
		{0x1f900, 2},
		{0x1f9ff, 2},
		{0x1fa00, 1},
		{0x20000, 2}, // CJK ext B
		{0x3fffd, 2},
		{0x3fffe, 1},
		{0x10ffff, 1},
	}
	for _, c := range cases {
		if got := runeWidth(c.r); got != c.want {
			t.Errorf("runeWidth(U+%04X) = %d, want %d", c.r, got, c.want)
		}
	}
	if w := displayWidth([]rune("aあb😀")); w != 6 {
		t.Errorf("displayWidth mixed = %d, want 6", w)
	}
}

func TestHistoryNext_RestoresDraft(t *testing.T) {
	e := newReplEditor("p", nil)
	e.history.Add("one")
	e.history.Add("two")

	// Type a draft, then navigate up twice, then down twice: moving past
	// the newest entry must restore the typed draft exactly once.
	e.line = []rune("typed draft")
	e.historyPrev() // -> "two", draft preserved
	e.historyPrev() // -> "one"
	if got := string(e.line); got != "one" {
		t.Fatalf("after 2x prev line = %q, want one", got)
	}
	e.historyNext() // -> "two"
	e.historyNext() // past newest -> draft restored
	if got := string(e.line); got != "typed draft" {
		t.Fatalf("line = %q, want restored draft", got)
	}
	if e.draft != nil {
		t.Fatal("draft must be cleared after restore")
	}

	// historyNext with no draft and no newer entry is a no-op.
	e.line = []rune("steady")
	e.historyNext()
	if got := string(e.line); got != "steady" {
		t.Fatalf("line = %q, want unchanged", got)
	}

	// Draft restoration only fires on nil entry, not on a real one.
	d := "another"
	e.draft = &d
	e.history.Prev() // position onto an entry
	e.historyNext()  // real entry loads, draft stays for the final step
	e.historyNext()
	if got := string(e.line); got != "another" {
		t.Fatalf("line = %q, want draft after moving past newest", got)
	}
}
