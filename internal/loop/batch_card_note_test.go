package loop

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/tool"
)

// cardCapture records the batch card text handed to the approver.
type cardCapture struct {
	mu    sync.Mutex
	cards []string
}

func (a *cardCapture) PromptCommand(cls danger.RiskClass, cmd, description string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cls == danger.ToolBatchClass {
		a.cards = append(a.cards, cmd)
	}
	return nil
}

func (a *cardCapture) PromptOperation(op danger.ToolOperation) error {
	return a.PromptCommand(op.Risk, op.Resource, op.Name)
}

func runBatchForCard(t *testing.T, note string) []string {
	t.Helper()
	registry := tool.NewRegistry([]tool.Tool{&mockTool{name: "shell", result: "done"}})
	server := batchApprovalServer(t, 2, "done")
	defer server.Close()
	approver := &cardCapture{}
	engine := New(testChatClient(t, server.URL), registry, 10, "", nil, 0)
	engine.SetApprover(approver)
	engine.SetMaxToolParallel(2)
	allow := "allow"
	engine.SetDangerousConfig(&danger.DangerousConfig{
		DefaultAction: &allow,
		Classes:       map[danger.RiskClass]danger.Action{danger.Destructive: danger.Prompt},
	})
	if note != "" {
		engine.SetApprovalCardNote(note)
	}
	if _, err := engine.Run(context.Background(), "run two tools"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	approver.mu.Lock()
	defer approver.mu.Unlock()
	return append([]string(nil), approver.cards...)
}

func TestBatchCard_ShowsApprovalCardNote(t *testing.T) {
	cards := runBatchForCard(t, "Sandbox network: none; attached: app_default, db_net")
	if len(cards) != 1 {
		t.Fatalf("got %d batch cards, want 1", len(cards))
	}
	if !strings.Contains(cards[0], "app_default, db_net") {
		t.Errorf("batch card lacks the network note:\n%s", cards[0])
	}
	if !strings.Contains(cards[0], "rm -rf /etc/test0") {
		t.Errorf("batch card lost its action lines:\n%s", cards[0])
	}
}

func TestBatchCard_NoteAbsentWhenUnset(t *testing.T) {
	cards := runBatchForCard(t, "")
	if len(cards) != 1 || strings.Contains(cards[0], "Sandbox network") {
		t.Errorf("unexpected note on card: %v", cards)
	}
}

func TestBatchCard_NoteIsSanitized(t *testing.T) {
	cards := runBatchForCard(t, "net\x1b[31m\nINJECTED LINE")
	if len(cards) != 1 {
		t.Fatalf("got %d cards", len(cards))
	}
	if strings.Contains(cards[0], "\x1b") || strings.Contains(cards[0], "\nINJECTED LINE") {
		t.Errorf("note must be sanitized for display:\n%q", cards[0])
	}
}
