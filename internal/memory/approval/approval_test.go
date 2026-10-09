package approval

import (
	"strings"
	"testing"
)

func TestResourceFromArgs(t *testing.T) {
	cases := []struct {
		args string
		want string
		ok   bool
	}{
		{`{"action":"consolidate","target":"env"}`, "memory consolidate env", true},
		{`{"action":"reject_pending_review","pending_id":"p2"}`, "memory reject_pending_review p2", true},
		{`{"action":"add_atom","content":"x"}`, `memory add_atom fact: "x"`, true},
		{`{"action":"add","target":"user","content":"a\tb"}`, `memory add user: "a\tb"`, true},
		{`{"action":"read"}`, "", false},
		{`not json`, "", false},
	}
	for _, tc := range cases {
		got, ok := ResourceFromArgs(tc.args)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ResourceFromArgs(%s) = %q,%v want %q,%v", tc.args, got, ok, tc.want, tc.ok)
		}
	}
	if got := Resource(Args{Action: "weird\x1b"}); strings.Contains(got, "\x1b") {
		t.Errorf("unknown action not sanitised: %q", got)
	}
}

func TestOversizedFieldIsRefusedNotElided(t *testing.T) {
	big := strings.Repeat("x", MaxTextBytes+1)
	if CheckBounds(Args{Action: "add", Content: big}) == nil {
		t.Error("oversized content passed CheckBounds")
	}
	if CheckBounds(Args{Action: "forget_atom", AtomID: big}) == nil {
		t.Error("oversized atom id passed CheckBounds")
	}
	got := Resource(Args{Action: "add", Target: "user", Content: big})
	if !strings.Contains(got, "refused") || strings.Contains(got, big) {
		t.Errorf("oversized resource = %q", got)
	}
	exact := strings.Repeat("y", MaxTextBytes)
	if got := Resource(Args{Action: "add", Target: "user", Content: exact}); !strings.Contains(got, exact) {
		t.Error("a field at the bound must be shown in full")
	}
}

func TestResourceWithEntry(t *testing.T) {
	rep := Args{Action: "replace", Target: "user", OldText: "dark", Content: "light mode"}
	if got := ResourceWithEntry(rep, "prefers dark mode"); got != `memory replace user: entry "prefers dark mode" (selected by "dark") → "light mode"` {
		t.Errorf("replace = %q", got)
	}
	rem := Args{Action: "remove", Target: "env", OldText: "dark"}
	if got := ResourceWithEntry(rem, "prefers dark\x1b[2K mode"); got != `memory remove env: entry "prefers dark\x1b[2K mode" (selected by "dark")` {
		t.Errorf("remove = %q", got)
	}
	// Other actions and refused calls fall back to Resource.
	add := Args{Action: "add", Target: "user", Content: "x"}
	if got := ResourceWithEntry(add, "ignored"); got != Resource(add) {
		t.Errorf("add = %q", got)
	}
	big := Args{Action: "remove", Target: "user", OldText: strings.Repeat("o", MaxTextBytes+1)}
	if got := ResourceWithEntry(big, "e"); !strings.Contains(got, "refused") {
		t.Errorf("oversized = %q", got)
	}
	// A long entry is cut on a rune boundary inside the excerpt.
	long := strings.Repeat("a", EntryExcerptBytes-1) + "é" + strings.Repeat("z", MaxTextBytes)
	got := ResourceWithEntry(rem, long)
	if !strings.Contains(got, "truncated") || strings.Contains(got, "é") || strings.Contains(got, "zzz") {
		t.Errorf("long entry = %q", got)
	}
	if !strings.Contains(got, "only the first 511 bytes") {
		t.Errorf("excerpt not cut on a rune boundary: %q", got)
	}
}
