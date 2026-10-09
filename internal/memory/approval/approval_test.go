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
