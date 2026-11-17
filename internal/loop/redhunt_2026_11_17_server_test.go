package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/tool"
)

// serverCardCommands answers the first model call with one shell call per
// command and every later call with a final answer.
func serverCardCommands(t *testing.T, commands ...string) *httptest.Server {
	t.Helper()
	calls := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls > 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
			return
		}
		var b strings.Builder
		b.WriteString(`{"choices":[{"message":{"content":"","tool_calls":[`)
		for i, c := range commands {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"id":"call_%d","function":{"name":"shell","arguments":"{\"command\":\"%s\"}"}}`, i, c)
		}
		b.WriteString(`]}}]}`)
		fmt.Fprint(w, b.String())
	}))
}

// serverCardsFor runs one batch of shell calls and returns the batch cards the
// approver received.
func serverCardsFor(t *testing.T, note string, commands ...string) []string {
	t.Helper()
	registry := tool.NewRegistry([]tool.Tool{&mockTool{name: "shell", result: "done"}})
	server := serverCardCommands(t, commands...)
	defer server.Close()
	approver := &cardCapture{}
	engine := New(testChatClient(t, server.URL), registry, 10, "", nil, 0)
	engine.SetApprover(approver)
	engine.SetMaxToolParallel(len(commands))
	allow := "allow"
	engine.SetDangerousConfig(&danger.DangerousConfig{
		DefaultAction: &allow,
		Classes:       map[danger.RiskClass]danger.Action{danger.Destructive: danger.Prompt},
	})
	if note != "" {
		engine.SetApprovalCardNote(note)
	}
	if _, err := engine.Run(context.Background(), "run the tools"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	approver.mu.Lock()
	defer approver.mu.Unlock()
	return append([]string(nil), approver.cards...)
}

// Every approval surface (Web UI, Telegram, terminal, denial errors) shows
// the batch card through the display sanitizer. The card's own text is
// first-party, so the sanitizer must have nothing to escape in it: an escape
// such as ️ on the card is noise the person has to read past.
func TestRED_BatchCardFirstPartyTextNeedsNoDisplayEscapes(t *testing.T) {
	for name, tc := range map[string]struct {
		note     string
		commands []string
		header   string
	}{
		"one action":      {commands: []string{"rm -rf /etc/test0", "echo hi"}, header: "The following tool action requires approval:"},
		"several actions": {commands: []string{"rm -rf /etc/test0", "rm -rf /etc/test1"}, header: "2 tool actions require approval:"},
		"with a note":     {note: "Sandbox network: none; attached: app_default", commands: []string{"rm -rf /etc/test0", "rm -rf /etc/test1"}, header: "2 tool actions require approval:"},
	} {
		t.Run(name, func(t *testing.T) {
			cards := serverCardsFor(t, tc.note, tc.commands...)
			if len(cards) != 1 {
				t.Fatalf("got %d batch cards, want 1", len(cards))
			}
			card := cards[0]
			if got := danger.SanitizeForDisplay(card); got != card {
				t.Fatalf("display sanitizer changed the batch card:\n got: %q\nwant: %q", got, card)
			}
			for _, line := range strings.Split(strings.TrimSuffix(card, "\n"), "\n") {
				if got := danger.SanitizeInline(line); got != line {
					t.Fatalf("inline sanitizer changed card line %q into %q", line, got)
				}
			}
			if first := strings.SplitN(card, "\n", 2)[0]; first != tc.header {
				t.Fatalf("card header = %q, want %q", first, tc.header)
			}
		})
	}
}
