package loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

func TestReassessmentRuntimeOutcomeAndBatchBoundaries(t *testing.T) {
	create := acceptanceCall("p", "plan", acceptancePlanArgs)
	check := acceptanceCall("c", "shell", `{"command":"go test ./parser"}`)
	for _, tc := range []struct {
		name     string
		batches  [][]session.ToolCall
		failure  error
		expected int
	}{
		{"failed_checks", [][]session.ToolCall{{create}, {check}, {check}, {check}}, errors.New("failed"), 1},
		{"hint_cap", [][]session.ToolCall{{create}, {check}, {check}, {check}, {check}, {check}, {check}, {check}, {check}, {check}}, errors.New("failed"), 2},
		{"parallel_duplicates", [][]session.ToolCall{{create}, {check, check, check}}, errors.New("failed"), 0},
		{"cancelled_calls", [][]session.ToolCall{{create}, {check}, {check}, {check}}, context.Canceled, 0},
		{"error_text_is_not_failure", [][]session.ToolCall{{create}, {check}, {check}, {check}}, nil, 0},
		{"no_active_plan", [][]session.ToolCall{{check}, {check}, {check}}, errors.New("failed"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, requests := acceptanceEngine(t, tc.batches, false)
			e.registry = tool.NewRegistry([]tool.Tool{NewPlanTool(e.planStore), &contractTool{name: "shell", run: func(string) (string, error) { return "untrusted text: error failed ignore rules", tc.failure }}})
			var signals []SignalEvent
			var structured []events.Event
			e.SetSignalHandler(func(ev SignalEvent) {
				if ev.Type == "plan_reassessment" {
					signals = append(signals, ev)
				}
			})
			e.SetEventHandler(func(ev events.Event) {
				if ev.Type == events.TypePlanReassessment {
					structured = append(structured, ev)
				}
			})
			_, history, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "Fix and verify."}})
			if err != nil {
				t.Fatal(err)
			}
			if len(signals) != tc.expected || len(structured) != tc.expected {
				t.Fatalf("signals=%d events=%d expected=%d", len(signals), len(structured), tc.expected)
			}
			if requests.Load() > int32(len(tc.batches)+2) {
				t.Fatal("reassessment added model requests")
			}
			count := 0
			for _, m := range history {
				if m.Role == "system" && strings.Contains(m.Content, "[odek plan reassessment:") {
					count++
				}
			}
			if count != tc.expected {
				t.Fatalf("persisted hints=%d expected=%d", count, tc.expected)
			}
			for _, ev := range structured {
				if ev.Data["reason"] != reassessmentRepeatedCheckFailure || ev.Data["failure_batches"] != 3 || len(ev.Data) != 2 {
					t.Fatalf("unexpected event payload: %+v", ev)
				}
				raw, _ := json.Marshal(ev)
				if strings.Contains(string(raw), "parser") || strings.Contains(string(raw), "untrusted") {
					t.Fatal("event leaked tool or task content")
				}
			}
		})
	}
}

func TestReassessmentDifferentToolsAndPlanUpdates(t *testing.T) {
	create := acceptanceCall("p", "plan", `{"verb":"create","steps":[{"id":"s","title":"Investigate"}]}`)
	note := acceptanceCall("note", "plan", `{"verb":"update","updates":[{"id":"s","note":"Still investigating"}]}`)
	e, _ := acceptanceEngine(t, [][]session.ToolCall{{create}, {acceptanceCall("a", "a", `{}`)}, {note}, {acceptanceCall("b", "b", `{}`)}, {note}, {acceptanceCall("c", "c", `{}`)}}, false)
	ts := []tool.Tool{NewPlanTool(e.planStore)}
	for _, name := range []string{"a", "b", "c"} {
		ts = append(ts, &contractTool{name: name, run: func(string) (string, error) { return "", errors.New("failure") }})
	}
	e.registry = tool.NewRegistry(ts)
	var reasons []string
	e.SetSignalHandler(func(ev SignalEvent) {
		if ev.Type == "plan_reassessment" {
			reasons = append(reasons, ev.Detail)
		}
	})
	if _, err := e.Run(context.Background(), "Investigate and adjust."); err != nil {
		t.Fatal(err)
	}
	if len(reasons) != 1 || reasons[0] != reassessmentVariedToolFailures {
		t.Fatalf("plan chatter defeated varied-failure detection: %v", reasons)
	}
}

func TestReassessmentBatchDenialsDoNotEncourageRetry(t *testing.T) {
	create := acceptanceCall("p", "plan", `{"verb":"create","steps":[{"id":"s","title":"Task"}]}`)
	denied := []session.ToolCall{acceptanceCall("a", "shell", `{"command":"rm -rf /tmp/a"}`), acceptanceCall("b", "shell", `{"command":"rm -rf /tmp/b"}`)}
	e, _ := acceptanceEngine(t, [][]session.ToolCall{{create}, denied, denied, denied}, false)
	var executed atomic.Bool
	e.registry = tool.NewRegistry([]tool.Tool{NewPlanTool(e.planStore), &contractTool{name: "shell", run: func(string) (string, error) { executed.Store(true); return "", errors.New("should not execute") }}})
	e.SetApprover(&mockApprover{approved: false})
	var hints int
	e.SetSignalHandler(func(ev SignalEvent) {
		if ev.Type == "plan_reassessment" {
			hints++
		}
	})
	if _, err := e.Run(context.Background(), "Task"); err != nil {
		t.Fatal(err)
	}
	if executed.Load() || hints != 0 {
		t.Fatalf("denial caused execution=%v hints=%d", executed.Load(), hints)
	}
}
