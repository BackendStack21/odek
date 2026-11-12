package loop

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// The bounded completed-effects evidence still lets the trimmed history fit
// with compaction on (digest and trim warning reserved) or off, a small
// window and large tool results between the mutating calls.
func TestEffectEvidence_FitsWithCompaction(t *testing.T) {
	for _, maxContext := range []int{8192, 32768} {
		for _, compaction := range []bool{true, false} {
			e := New(nil, tool.NewRegistry(nil), 90, "sys", nil, maxContext)
			e.SetCompaction(compaction)
			body := strings.Repeat("fmt.Println(\"generated line of code\")\n", 50)
			messages := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "scaffold the project"}}
			for i := 0; i < 120; i++ {
				cmd := fmt.Sprintf("cat > pkg/file_%03d.go <<'EOF'\npackage pkg\n%sEOF", i, body)
				id := fmt.Sprintf("c%d", i)
				call := covCall(id, "shell", map[string]string{"command": cmd})
				e.recordMutation("shell", call.Function.Arguments, "(no output)")
				out := "(no output)"
				if i%3 == 0 {
					out = strings.Repeat("build log line with details\n", 120)
				}
				messages = append(messages,
					session.Message{Role: "assistant", ToolCalls: []session.ToolCall{call}},
					session.Message{Role: "tool", ToolCallID: id, Content: out})
				if i%40 == 39 {
					messages = append(messages, session.Message{Role: "user", Content: "keep going"})
				}
			}
			got := e.trimContext(context.Background(), messages, nil)
			budget := contextBudget(maxContext)
			if total := estimateMessages(got); total > budget {
				t.Errorf("window %d compaction %v: trimmed history %d tokens over a %d budget", maxContext, compaction, total-budget, budget)
			}
		}
	}
}
