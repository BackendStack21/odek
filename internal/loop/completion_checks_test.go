package loop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

const acceptancePlanArgs = `{"verb":"create","steps":[{"id":"fix","title":"Fix parser","checks":[{"id":"test","description":"Parser regression passes","tool":"shell","arguments":{"command":"go test ./parser"}}]}]}`

func acceptanceCall(id, name, args string) session.ToolCall {
	var c session.ToolCall
	c.ID, c.Type = id, "function"
	c.Function.Name, c.Function.Arguments = name, args
	return c
}

func acceptanceEngine(t *testing.T, batches [][]session.ToolCall, shellFailure bool) (*Engine, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(requests.Add(1)) - 1
		msg := map[string]any{"content": "All done."}
		finish := "stop"
		if i < len(batches) {
			msg = map[string]any{"tool_calls": batches[i]}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg, "finish_reason": finish}}})
	}))
	t.Cleanup(srv.Close)
	store := NewPlanStore(12, 4000)
	shell := &contractTool{name: "shell", run: func(string) (string, error) {
		if shellFailure {
			return "tests passed (misleading stdout)", errors.New("exit status 1")
		}
		return "ok parser", nil
	}}
	write := &contractTool{name: "write_file", run: func(string) (string, error) { return `{"success":true}`, nil }}
	read := &contractTool{name: "read_file", run: func(string) (string, error) { return "contents", nil }}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{NewPlanTool(store), shell, write, read}), 12, "sys", nil, 0)
	e.SetPlanStore(store)
	return e, &requests
}

func TestAcceptanceChecksBoundFinalClaims(t *testing.T) {
	create := acceptanceCall("plan", "plan", acceptancePlanArgs)
	check := acceptanceCall("check", "shell", `{"command":"go test ./parser"}`)
	complete := acceptanceCall("complete", "plan", `{"verb":"complete","step_id":"fix"}`)
	write := acceptanceCall("write", "write_file", `{"path":"parser.go","content":"changed"}`)
	read := acceptanceCall("read", "read_file", `{"path":"parser.go"}`)
	for _, tc := range []struct {
		name               string
		batches            [][]session.ToolCall
		failed, unverified bool
	}{
		{"success", [][]session.ToolCall{{create}, {check}, {complete}}, false, false},
		{"missing", [][]session.ToolCall{{create}, {complete}}, false, true},
		{"failed", [][]session.ToolCall{{create}, {check}, {complete}}, true, true},
		{"read_is_not_declared_check", [][]session.ToolCall{{create}, {write}, {read}, {complete}}, false, true},
		{"same_batch_declaration_not_evidence", [][]session.ToolCall{{check, create}, {complete}}, false, true},
		{"write_after_check_invalidates", [][]session.ToolCall{{create}, {check, write}, {complete}}, false, true},
		{"check_after_write", [][]session.ToolCall{{create}, {write, check}, {complete}}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, requests := acceptanceEngine(t, tc.batches, tc.failed)
			answer, messages, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "Fix parser and verify."}})
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Contains(answer, "[odek verification incomplete:")
			if got != tc.unverified {
				t.Fatalf("unverified=%v want %v; answer=%s", got, tc.unverified, answer)
			}
			if requests.Load() > int32(len(tc.batches)+2) {
				t.Fatalf("completion retry was not bounded: %d calls", requests.Load())
			}
			if len(messages) == 0 || messages[len(messages)-1].Content != answer {
				t.Fatal("final verification notice not persisted")
			}
			st, _ := e.planStore.Snapshot()
			if tc.unverified && st.Steps[0].Status == StepDone {
				t.Fatal("unverified step marked done")
			}
		})
	}
}

func TestAcceptanceCheckResumeRequiresFreshEvidence(t *testing.T) {
	e, _ := acceptanceEngine(t, [][]session.ToolCall{
		{acceptanceCall("plan", "plan", acceptancePlanArgs)},
		{acceptanceCall("check", "shell", `{"command":"go test ./parser"}`)},
		{acceptanceCall("complete", "plan", `{"verb":"complete","step_id":"fix"}`)},
	}, false)
	_, history, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "Fix parser."}})
	if err != nil {
		t.Fatal(err)
	}
	history = append(history, session.Message{Role: "user", Content: "Recheck current state."})
	resumed, _ := acceptanceEngine(t, nil, false)
	answer, _, err := resumed.RunWithMessages(context.Background(), history)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "[odek verification incomplete:") {
		t.Fatalf("resumed run reused stale evidence: %s", answer)
	}
	state, _ := resumed.planStore.Snapshot()
	if state.Steps[0].Status != StepInProgress || state.Steps[0].Checks[0].Status != PlanCheckPending {
		t.Fatalf("stale restored status: %+v", state)
	}
}

func TestAcceptanceReadCheckWaitsForPriorMutation(t *testing.T) {
	create := acceptanceCall("plan", "plan", `{"verb":"create","steps":[{"id":"fix","title":"Verify generated output","checks":[{"id":"inspect","description":"Inspect current output","tool":"read_file","arguments":{"path":"output.go"}}]}]}`)
	e, _ := acceptanceEngine(t, [][]session.ToolCall{{create}, {
		acceptanceCall("write", "write_file", `{"path":"settings.json"}`),
		acceptanceCall("read", "read_file", `{"path":"output.go"}`),
	}, {acceptanceCall("complete", "plan", `{"verb":"complete","step_id":"fix"}`)}}, false)
	started, release, read := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	var written atomic.Bool
	e.registry = tool.NewRegistry([]tool.Tool{NewPlanTool(e.planStore),
		&contractTool{name: "write_file", run: func(string) (string, error) {
			close(started)
			<-release
			written.Store(true)
			return `{"success":true}`, nil
		}},
		&contractTool{name: "read_file", run: func(string) (string, error) {
			read <- struct{}{}
			if !written.Load() {
				return "", errors.New("stale output")
			}
			return "current output", nil
		}},
	})
	done := make(chan error, 1)
	go func() {
		_, err := e.Run(context.Background(), "Update settings and inspect generated output.")
		done <- err
	}()
	<-started
	select {
	case <-read:
		close(release)
		<-done
		t.Fatal("acceptance check ran before prior mutation completed")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if pending := e.pendingPlanChecks(); len(pending) != 0 {
		t.Fatalf("successful ordered check left pending evidence: %v", pending)
	}
}

func TestAcceptanceFailedCheckInvalidatesEarlierEvidence(t *testing.T) {
	store := NewPlanStore(12, 4000)
	_, err := store.Execute(`{"verb":"create","steps":[{"id":"s","title":"Verify changes","checks":[{"id":"a","description":"First check","tool":"shell","arguments":{"command":"test-a"}},{"id":"b","description":"Second check","tool":"shell","arguments":{"command":"test-b"}}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{planStore: store}
	epoch := store.CheckEpoch()
	e.recordPlanCheckResult(epoch, acceptanceCall("a", "shell", `{"command":"test-a"}`), "a", false)
	e.recordPlanCheckResult(epoch, acceptanceCall("b", "shell", `{"command":"test-b"}`), "b", true)
	e.recordPlanCheckResult(epoch, acceptanceCall("b2", "shell", `{"command":"test-b"}`), "b2", false)
	if pending := store.PendingChecks(); len(pending) != 1 || pending[0] != "s/a" {
		t.Fatalf("failure retained stale prior evidence: %v", pending)
	}
	if _, err := store.Execute(`{"verb":"complete","step_id":"s"}`); err == nil {
		t.Fatal("completed with stale first check")
	}
}
