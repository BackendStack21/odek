package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/resource"
	"github.com/BackendStack21/odek/internal/session"
	golangws "golang.org/x/net/websocket"
)

// buildServeMuxPromptAll is buildServeMux with the danger policy forced to
// "prompt" for every risk class, so even a benign `echo` shell command routes
// through the wsApprover. This lets the test drive a real browser-approval
// round trip without running a genuinely dangerous command.
func buildServeMuxPromptAll(t *testing.T, store *session.Store) (net.Listener, *http.ServeMux) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	resolved := config.LoadConfig(config.CLIFlags{})
	systemMessage := resolved.System
	if systemMessage == "" {
		systemMessage = defaultSystem
	}
	prompt := "prompt"
	resolved.Dangerous.DefaultAction = &prompt

	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	resourceReg := resource.NewRegistry(
		resource.NewFileResolver(cwd),
		resource.NewSessionResolver(filepath.Join(home, ".odek", "sessions")),
	)

	wsToken, err := newServeToken()
	if err != nil {
		t.Fatalf("CSRF token: %v", err)
	}
	testTokenMu.Lock()
	testLastToken = wsToken
	testTokenMu.Unlock()

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleStatic(wsToken))
	mux.Handle("/ws", &golangws.Server{
		Handshake: func(cfg *golangws.Config, req *http.Request) error {
			return wsHandshakeWithLimits(cfg, req, wsToken)
		},
		Handler: func(conn *golangws.Conn) {
			handleWS(store, resourceReg, resolved, systemMessage, conn)
		},
	})
	mux.HandleFunc("/api/cancel", handleCancel(store))

	return ln, mux
}

// TestServe_E2E_ApprovalRoundTrip is the regression test for the serve-mode
// approval deadlock. The agent calls a tool that requires approval and blocks
// in PromptCommand; the browser's approval_response must be read and delivered
// while that prompt is in flight. Before the fix the socket reader and the
// agent loop were the same goroutine, so the response could never be read and
// every approval hit the 60s timeout. The 10s read deadline here is well under
// that timeout: a regression would blow the deadline, the fix completes in ms.
func TestServe_E2E_ApprovalRoundTrip(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		if callCount <= 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"Running.","tool_calls":[{"id":"c_1","function":{"name":"shell","arguments":"{\"command\":\"echo approved-ok\"}"}}]}}]}`)
		} else {
			w.Write([]byte(`{"choices":[{"message":{"content":"All done."}}]}`))
		}
	})
	defer llmSrv.Close()

	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()

	store := newTestSessionStore(t)
	ln, mux := buildServeMuxPromptAll(t, store)
	defer ln.Close()

	go func() { _ = serveOnListener(ln, mux) }()
	waitForHTTP(t, ln.Addr().String())

	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()

	prompt := map[string]string{"type": "prompt", "content": "run a command"}
	payload, _ := json.Marshal(prompt)
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// 10s ceiling — far below the 60s approval timeout a deadlock would hit.
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	var sawApprovalRequest, sawToolResult, sawDone bool
	for i := 0; i < 30; i++ {
		var raw []byte
		if err := golangws.Message.Receive(conn, &raw); err != nil {
			t.Fatalf("Receive event %d: %v (approval_request=%v tool_result=%v)", i, err, sawApprovalRequest, sawToolResult)
		}
		var evt map[string]any
		if err := json.Unmarshal(raw, &evt); err != nil {
			continue
		}
		switch evt["type"] {
		case "approval_request":
			sawApprovalRequest = true
			id, _ := evt["id"].(string)
			resp, _ := json.Marshal(map[string]string{
				"type":   "approval_response",
				"id":     id,
				"action": "approve",
			})
			if err := golangws.Message.Send(conn, string(resp)); err != nil {
				t.Fatalf("send approval_response: %v", err)
			}
		case "tool_result":
			sawToolResult = true
		case "done":
			sawDone = true
			goto finished
		case "error":
			t.Fatalf("unexpected error event: %v", evt["message"])
		}
	}
finished:
	if !sawApprovalRequest {
		t.Fatal("never received an approval_request — the tool did not prompt")
	}
	if !sawToolResult {
		t.Error("approval was processed but the tool never produced a result")
	}
	if !sawDone {
		t.Fatal("never reached done — approval round trip did not complete (deadlock regression?)")
	}
}

// TestWSApprover_ConcurrentTrust_NoRace drives many PromptCommand calls
// concurrently, each answered with "trust", so the trust-cache write
// (approveAll[cls]=true) races against the trust-cache read at the top of
// PromptCommand. Before the fix these map accesses were unsynchronised: this
// test crashes with "concurrent map writes" (and is flagged under -race).
func TestWSApprover_ConcurrentTrust_NoRace(t *testing.T) {
	a := newWSApprover(nil)
	// Answer every prompt with an immediate "trust" from a separate goroutine,
	// forcing the approveAll write path under concurrency.
	a.sendFn = func(v any) error {
		if req, ok := v.(approvalRequest); ok {
			go a.HandleResponse(req.ID, "trust")
		}
		return nil
	}

	classes := []danger.RiskClass{
		danger.Safe, danger.LocalWrite, danger.SystemWrite,
		danger.NetworkEgress, danger.CodeExecution, danger.Install,
	}

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		for _, cls := range classes {
			wg.Add(1)
			go func(c danger.RiskClass) {
				defer wg.Done()
				_ = a.PromptCommand(c, "cmd", "desc")
			}(cls)
		}
		// Interleave SetTrustAll to also exercise the trustAll field race.
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.SetTrustAll(false)
		}()
	}
	wg.Wait()
}
