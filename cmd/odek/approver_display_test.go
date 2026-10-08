package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/mcpclient"
)

const hostileApprovalText = "echo ok\x1b[2K\r\x1b]0;safe\x07 \u202egnirts\u202c \u2066x\u2069 \u200bz"

func assertNoRawControl(t *testing.T, label, s string) {
	t.Helper()
	for _, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == 0x200b ||
			(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			t.Errorf("%s holds raw control or bidi character U+%04X: %q", label, r, s)
			return
		}
	}
}

// The WebSocket approval frame carries sanitized command and description text:
// the UI renders these fields as text, so control and bidi characters must
// already be visible escapes.
func TestWSApprover_FramesCarrySanitizedText(t *testing.T) {
	frames := make(chan approvalRequest, 1)
	a := newWSApprover(func(v any) error {
		if req, ok := v.(approvalRequest); ok {
			frames <- req
		}
		return nil
	})
	done := make(chan error, 1)
	go func() {
		done <- a.PromptCommand(danger.NetworkEgress, hostileApprovalText, "why "+hostileApprovalText)
	}()
	select {
	case req := <-frames:
		assertNoRawControl(t, "command", req.Command)
		assertNoRawControl(t, "description", req.Description)
		if !strings.Contains(req.Command, `\x1b`) || !strings.Contains(req.Command, `\u202e`) {
			t.Errorf("command escapes not visible: %q", req.Command)
		}
		a.HandleResponse(req.ID, "deny")
	case <-time.After(5 * time.Second):
		t.Fatal("no approval frame")
	}
	<-done
}

func TestWSApprover_OperationFramesAreSanitized(t *testing.T) {
	frames := make(chan approvalRequest, 1)
	a := newWSApprover(func(v any) error {
		if req, ok := v.(approvalRequest); ok {
			frames <- req
		}
		return nil
	})
	done := make(chan error, 1)
	go func() {
		done <- a.PromptOperation(danger.ToolOperation{Name: "write_file\x1b[2K", Resource: "/tmp/\u202eexe.txt", Risk: danger.LocalWrite})
	}()
	select {
	case req := <-frames:
		assertNoRawControl(t, "command", req.Command)
		assertNoRawControl(t, "description", req.Description)
		a.HandleResponse(req.ID, "deny")
	case <-time.After(5 * time.Second):
		t.Fatal("no approval frame")
	}
	<-done
}

// The project MCP approval prompt prints repo-controlled command, args and
// env values; none of them may carry raw control or bidi characters.
func TestMCPApprovalPrompt_SanitizesProjectControlledText(t *testing.T) {
	setupTestHome(t)
	t.Setenv("ODEK_APPROVE_MCP", "")
	nonce := fmt.Sprintf("srv-%d", time.Now().UnixNano())
	resolved := config.ResolvedConfig{
		MCPServers: map[string]mcpclient.ServerConfig{
			"project": {
				Command: "node\x1b[2K" + nonce,
				Args:    []string{"\u202egnirts", "a\rb"},
				Env:     map[string]string{"K\x07": "v\x1b]0;x\x07"},
			},
		},
		ProjectMCPServerNames: []string{"project"},
	}
	var out bytes.Buffer
	_ = approveMCPServersWithTTY(resolved, strings.NewReader("\n"), &out, true)
	assertNoRawControl(t, "MCP approval prompt", out.String())
	if !strings.Contains(out.String(), `\x1b[2K`) {
		t.Errorf("escape not visible in prompt: %q", out.String())
	}
}
