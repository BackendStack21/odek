package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// loopHuntShellArgs encodes a shell tool call's arguments.
func loopHuntShellArgs(t *testing.T, cmd string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"command": cmd})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The completed-effects ledger is installed as a system message that every
// trim mode protects. Each completed mutating shell command is recorded with
// its full command text, so a long run that writes files through heredocs
// grows the protected message without bound: once it alone exceeds the
// context budget, trimming drops everything else and still cannot fit, and
// the provider rejects every further request (survival trimming keeps the
// ledger too). The protected evidence must stay within a bounded share of
// the budget, like the compaction digest.
func TestRED_Loop_EffectEvidenceBoundedWithinContextBudget(t *testing.T) {
	const maxContext = 32768 // a common local-model window
	e := New(nil, tool.NewRegistry(nil), 90, "sys", nil, maxContext)
	e.SetCompaction(false)

	// 60 completed mutating shell calls, each writing a ~2 KB file through a
	// heredoc — an ordinary way for a model to create files with the shell.
	body := strings.Repeat("fmt.Println(\"generated line of code\")\n", 50)
	messages := []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "scaffold the project"},
	}
	for i := 0; i < 60; i++ {
		cmd := fmt.Sprintf("cat > pkg/file_%02d.go <<'EOF'\npackage pkg\n%sEOF", i, body)
		id := fmt.Sprintf("c%d", i)
		call := session.ToolCall{ID: id, Type: "function"}
		call.Function.Name = "shell"
		call.Function.Arguments = loopHuntShellArgs(t, cmd)
		e.recordMutation("shell", call.Function.Arguments, "(no output)")
		messages = append(messages,
			session.Message{Role: "assistant", ToolCalls: []session.ToolCall{call}},
			session.Message{Role: "tool", ToolCallID: id, Content: "(no output)"},
		)
	}

	got := e.trimContext(context.Background(), messages, nil)

	budget := contextBudget(maxContext)
	total := estimateMessages(got)
	if total > budget {
		evidence := 0
		for _, m := range got {
			if isEffectEvidence(m) {
				evidence = estimateTokens(m.Content)
			}
		}
		t.Fatalf("trimmed history is still %d tokens over a %d-token budget: the protected completed-effects message alone is %d tokens (%d ledger entries) and grows with every mutating call", total-budget, budget, evidence, len(e.runMutations))
	}
}
