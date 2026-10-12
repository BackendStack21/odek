package session

import "testing"

func TestMemoryFix_MemoryCallTaintRule(t *testing.T) {
	cases := map[string]bool{
		`{"action":"list_quarantine"}`:          true,
		`{"action":"list_pending_review"}`:      true,
		`{"action":"search_atoms","query":"x"}`: false,
		`{"action":"add","content":"x"}`:        false,
		`not json`:                              true,
	}
	for args, want := range cases {
		if got := ToolCallTaintsEpisode("memory", args); got != want {
			t.Errorf("memory %s: taint=%v want %v", args, got, want)
		}
	}
}
