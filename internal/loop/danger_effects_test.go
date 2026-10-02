package loop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/tool"
)

type effectsApprover struct {
	mockApprover
	commands []string
}

func (a *effectsApprover) PromptCommand(_ danger.RiskClass, command, _ string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.commands = append(a.commands, command)
	return errors.New("denied by test approver")
}

func TestBatchApprovalRetainsIndependentShellEffects(t *testing.T) {
	for _, name := range []string{"shell", "terminal", "bg_start"} {
		t.Run(name, func(t *testing.T) {
			command := "node -e 0 > ordinary-output"
			args, _ := json.Marshal(map[string]string{"command": command})
			batch, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{
				map[string]any{"id": "execute", "type": "function", "function": map[string]any{"name": name, "arguments": string(args)}},
				map[string]any{"id": "inspect", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"ordinary-file"}`}},
			}}, "finish_reason": "tool_calls"}}})
			srv := contractServer(t, string(batch))
			defer srv.Close()
			var executions atomic.Int32
			fixture := func(args string) (string, error) { executions.Add(1); return "done", nil }
			registry := tool.NewRegistry([]tool.Tool{&contractTool{name: name, run: fixture}, &contractTool{name: "read_file", run: fixture}})
			e := New(testChatClient(t, srv.URL), registry, 4, "sys", nil, 0)
			approver := &effectsApprover{}
			e.SetApprover(approver)
			e.SetDangerousConfig(&danger.DangerousConfig{Classes: map[danger.RiskClass]danger.Action{danger.CodeExecution: danger.Allow, danger.LocalWrite: danger.Prompt}})
			if _, err := e.Run(context.Background(), "run tools"); err != nil {
				t.Fatal(err)
			}
			if executions.Load() != 0 {
				t.Fatal("allowed execution class hid the write approval")
			}
			approver.mu.Lock()
			defer approver.mu.Unlock()
			if len(approver.commands) != 1 || !strings.Contains(approver.commands[0], command) {
				t.Fatalf("batch omitted the command needing write approval: %v", approver.commands)
			}
		})
	}
}

func TestBatchUnreadApprovalCannotWeakenDeniedEffect(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "program.sh")
	if err := os.WriteFile(file, []byte("echo harmless\n"), 0700); err != nil {
		t.Fatal(err)
	}
	command := "sh " + file + "; curl https://example.com"
	args, _ := json.Marshal(map[string]string{"command": command})
	batch, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{
		map[string]any{"id": "execute", "type": "function", "function": map[string]any{"name": "shell", "arguments": string(args)}},
		map[string]any{"id": "inspect", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"ordinary-file"}`}},
	}}, "finish_reason": "tool_calls"}}})
	srv := contractServer(t, string(batch))
	defer srv.Close()
	var ran atomic.Bool
	shell := &contractTool{name: "shell", run: func(string) (string, error) { ran.Store(true); return "done", nil }}
	read := &contractTool{name: "read_file", run: func(string) (string, error) { return "read", nil }}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{shell, read}), 4, "sys", nil, 0)
	e.SetApprover(&mockApprover{})
	e.SetDangerousConfig(&danger.DangerousConfig{Classes: map[danger.RiskClass]danger.Action{danger.CodeExecution: danger.Allow, danger.NetworkEgress: danger.Deny, danger.UnreadExec: danger.Prompt}})
	if _, err := e.Run(context.Background(), "run tools"); err != nil {
		t.Fatal(err)
	}
	if ran.Load() {
		t.Fatal("denied egress escaped batch gate")
	}
}
