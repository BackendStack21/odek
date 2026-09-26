// Package eval provides a deterministic, local-only harness for exercising the
// production loop.Engine. It evaluates runtime behavior and fixture state; it
// does not measure live-model intelligence.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/llmclient"
	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/tool"
)

type ToolCall struct {
	Name  string
	Args  string
	Error bool
}
type Fixture struct {
	Values       map[string]string
	Calls        []ToolCall
	GoodCalls    []ToolCall
	Plan         *loop.PlanState
	Hints        []string
	HintRequests []int
	mu           sync.Mutex
}

func (f *Fixture) get(k string) string { f.mu.Lock(); defer f.mu.Unlock(); return f.Values[k] }

// Scenario describes model messages and an independent oracle. Responses are
// wire-level assistant replies, while Oracle is the only source of pass/fail.
type OracleResult struct {
	TaskSuccess     bool
	FalseCompletion bool
	Errors          []string
}

type Scenario struct {
	Name             string
	Task             string
	Responses        []string
	Tools            []tool.Tool
	Fixture          *Fixture
	Oracle           func(*Fixture, string, error, []ToolCall) OracleResult
	Cancel           bool
	Plan             bool
	RequestInspector func([]byte)
}
type ToolCallReport struct {
	Name  string `json:"name"`
	Error bool   `json:"error"`
}
type CaseReport struct {
	Name            string           `json:"name"`
	Success         bool             `json:"scenario_passed"`
	TaskSuccess     bool             `json:"task_success"`
	FalseCompletion bool             `json:"false_completion"`
	AssertionErrors []string         `json:"assertion_errors,omitempty"`
	Error           string           `json:"error,omitempty"`
	ToolCalls       []ToolCallReport `json:"tool_calls,omitempty"`
	InputTokens     int64            `json:"input_tokens"`
	OutputTokens    int64            `json:"output_tokens"`
	ElapsedMS       int64            `json:"elapsed_ms"`
}
type Report struct {
	Cases               []CaseReport `json:"cases"`
	Total               int          `json:"total"`
	Passed              int          `json:"passed"`
	Failed              int          `json:"failed"`
	FalseCompletionRate float64      `json:"false_completion_rate"`
	TokensKnown         bool         `json:"tokens_known"`
	CostKnown           bool         `json:"cost_known"`
}

// RunOptions can replace the deterministic client constructor. The scripted
// localhost provider remains the default; a caller may supply a factory for
// an already-authorized provider when comparing adapters. The harness never
// supplies live credentials. The selected client determines the endpoint;
// callers are responsible for authorizing any external provider access.
type RunOptions struct {
	ClientFactory func(baseURL string) (*llmclient.Client, error)
}

// Run executes scenarios against a fresh localhost scripted provider.
func Run(ctx context.Context, scenarios []Scenario) Report {
	return RunWithOptions(ctx, scenarios, RunOptions{})
}

// RunWithOptions is Run with an optional per-case client factory.
func RunWithOptions(ctx context.Context, scenarios []Scenario, opts RunOptions) Report {
	out := Report{Total: len(scenarios)}
	for _, s := range scenarios {
		out.Cases = append(out.Cases, runCase(ctx, s, opts))
	}
	for _, c := range out.Cases {
		if c.Success {
			out.Passed++
		} else {
			out.Failed++
		}
	}
	// This rate is deliberately narrow: cases whose oracle reports a false
	// completion, divided by cases. It is not a model quality score.
	falseCount := 0
	for _, c := range out.Cases {
		if c.FalseCompletion {
			falseCount++
		}
	}
	if out.Total > 0 {
		out.FalseCompletionRate = float64(falseCount) / float64(out.Total)
	}
	for _, c := range out.Cases {
		if c.InputTokens > 0 || c.OutputTokens > 0 {
			out.TokensKnown = true
		}
		// Cost is deliberately unavailable: this harness does not configure prices.
	}
	return out
}

func runCase(parent context.Context, s Scenario, opts RunOptions) CaseReport {
	started := time.Now()
	cr := CaseReport{Name: s.Name}
	if s.Oracle == nil {
		cr.Error = "scenario requires an independent oracle"
		return cr
	}
	parent, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	var mu sync.Mutex
	calls := []ToolCall{}
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.RequestInspector != nil {
			if body, readErr := io.ReadAll(io.LimitReader(r.Body, 2<<20)); readErr == nil {
				s.RequestInspector(body)
			}
		}
		mu.Lock()
		i := n
		n++
		mu.Unlock()
		body := `{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`
		if i < len(s.Responses) {
			body = s.Responses[i]
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	var client *llmclient.Client
	var err error
	if opts.ClientFactory != nil {
		client, err = opts.ClientFactory(srv.URL)
	} else {
		client, err = scriptedClient(srv.URL)
	}
	if err == nil && client == nil {
		err = fmt.Errorf("client factory returned no client")
	}
	if err != nil {
		cr.Error = err.Error()
		cr.ElapsedMS = time.Since(started).Milliseconds()
		return cr
	}
	registered := append([]tool.Tool(nil), s.Tools...)
	var planStore *loop.PlanStore
	if s.Plan {
		planStore = loop.NewPlanStore(12, 4000)
		registered = append(registered, loop.NewPlanTool(planStore))
	}
	e := loop.New(client, tool.NewRegistry(registered), 12, "", nil, 0)
	if planStore != nil {
		e.SetPlanStore(planStore)
	}
	e.SetInteractionMode("off")
	callIndex := map[string]int{}
	e.SetToolDetailHandler(func(ev loop.ToolDetailEvent) {
		mu.Lock()
		defer mu.Unlock()
		switch ev.Type {
		case "tool_call":
			callIndex[ev.CallID] = len(calls)
			calls = append(calls, ToolCall{Name: ev.Name, Args: ev.Data})
		case "tool_result":
			if i, ok := callIndex[ev.CallID]; ok {
				calls[i].Error = ev.Outcome == "failed"
			}
		}
	})
	if s.Cancel {
		cancelled, cancel := context.WithCancel(parent)
		cancel()
		parent = cancelled
	}
	result, runErr := e.Run(parent, s.Task)
	if planStore != nil && s.Fixture != nil {
		if state, ok := planStore.Snapshot(); ok {
			s.Fixture.Plan = &state
		}
	}
	mu.Lock()
	snapshot := append([]ToolCall(nil), calls...)
	mu.Unlock()
	for _, c := range snapshot {
		cr.ToolCalls = append(cr.ToolCalls, ToolCallReport{Name: c.Name, Error: c.Error})
	}
	var outcome OracleResult
	if s.Oracle != nil {
		outcome = s.Oracle(s.Fixture, result, runErr, snapshot)
	}
	cr.AssertionErrors = outcome.Errors
	cr.TaskSuccess = outcome.TaskSuccess
	cr.FalseCompletion = outcome.FalseCompletion
	if runErr != nil && len(cr.AssertionErrors) == 0 && !s.Cancel {
		cr.Error = runErr.Error()
	}
	cr.InputTokens = e.BudgetUsage().InputTokens
	cr.OutputTokens = e.BudgetUsage().OutputTokens
	cr.Success = len(cr.AssertionErrors) == 0 && (runErr == nil || s.Cancel)
	cr.ElapsedMS = time.Since(started).Milliseconds()
	return cr
}

func scriptedClient(baseURL string) (*llmclient.Client, error) {
	s, err := llmclient.NewSDK(llmclient.Options{Provider: "eval", Model: "eval-model", APIKey: "eval-key", BaseURL: baseURL, Providers: map[string]llmclient.ProviderOverride{"eval": {APIKey: "eval-key", BaseURL: baseURL, Format: "openai"}}})
	if err != nil {
		return nil, err
	}
	return llmclient.New(s, "eval", "eval-model")
}

// Tool returns a stateful fixture tool. kind is write, read, flaky, or check.
func Tool(f *Fixture, name, kind string) tool.Tool { return fixtureTool{f: f, name: name, kind: kind} }

type fixtureTool struct {
	f          *Fixture
	name, kind string
}

func (t fixtureTool) Name() string        { return t.name }
func (t fixtureTool) Description() string { return "deterministic evaluation fixture" }
func (t fixtureTool) Schema() any {
	return map[string]any{"type": "object", "properties": map[string]any{"key": map[string]string{"type": "string"}, "value": map[string]string{"type": "string"}}}
}
func (t fixtureTool) Call(raw string) (string, error) {
	var a struct{ Key, Value string }
	_ = json.Unmarshal([]byte(raw), &a)
	if a.Key == "" {
		a.Key = "artifact"
	}
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	t.f.Calls = append(t.f.Calls, ToolCall{Name: t.name, Args: raw})
	switch t.kind {
	case "write":
		t.f.Values[a.Key] = a.Value
		t.f.GoodCalls = append(t.f.GoodCalls, ToolCall{Name: t.name, Args: raw})
		return "artifact written", nil
	case "read":
		if v, ok := t.f.Values[a.Key]; ok {
			t.f.GoodCalls = append(t.f.GoodCalls, ToolCall{Name: t.name, Args: raw})
			return v, nil
		}
		return "", fmt.Errorf("missing artifact %q", a.Key)
	case "flaky":
		if t.f.Values["attempts"] != "1" {
			t.f.Values["attempts"] = "1"
			return "", fmt.Errorf("transient failure")
		}
		t.f.Values["attempts"] = "2"
		t.f.GoodCalls = append(t.f.GoodCalls, ToolCall{Name: t.name, Args: raw})
		return "recovered", nil
	case "check":
		if v := t.f.Values[a.Key]; v != "" {
			return v, nil
		}
		return "missing evidence", nil
	case "always_fail":
		return "fixture failure", fmt.Errorf("fixture failure")
	default:
		return "", fmt.Errorf("unknown fixture")
	}
}

func toolCall(name, id, args string) string {
	body := map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{
				"content": "",
				"tool_calls": []any{map[string]any{
					"id": id, "type": "function",
					"function": map[string]any{"name": name, "arguments": args},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]int{"prompt_tokens": 7, "completion_tokens": 3},
	}
	b, _ := json.Marshal(body)
	return string(b)
}
func final(text string) string {
	body := map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]any{"content": text},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{"prompt_tokens": 7, "completion_tokens": 3},
	}
	b, _ := json.Marshal(body)
	return string(b)
}

func baseFixture() *Fixture { return &Fixture{Values: map[string]string{}} }

// Scenarios is the standard deterministic suite shipped with the CLI.
func Scenarios() []Scenario {
	f1 := baseFixture()
	f2 := baseFixture()
	f3 := baseFixture()
	f4 := baseFixture()
	f5 := baseFixture()
	f6 := baseFixture()
	f6.Values["evidence"] = "evidence"
	f7 := baseFixture()
	f8 := baseFixture()
	f9 := baseFixture()
	f10 := baseFixture()
	f11 := baseFixture()
	f12 := baseFixture()
	f13 := baseFixture()
	f14 := baseFixture()
	return []Scenario{
		{Name: "successful_fix_verified", Task: "write and verify artifact", Fixture: f1, Tools: []tool.Tool{Tool(f1, "write_file", "write"), Tool(f1, "read_file", "read")}, Responses: []string{toolCall("write_file", "w1", `{"key":"artifact","value":"fixed"}`), toolCall("read_file", "r1", `{"key":"artifact"}`), final("verified")}, Oracle: func(f *Fixture, r string, e error, _ []ToolCall) OracleResult {
			if e != nil {
				return OracleResult{Errors: []string{e.Error()}}
			}
			if f.get("artifact") != "fixed" || !hasGoodCall(f, "read_file") {
				return OracleResult{Errors: []string{"artifact was not verified"}}
			}
			return OracleResult{TaskSuccess: true}
		}},
		{Name: "failed_tool_false_success", Task: "read missing artifact", Fixture: f2, Tools: []tool.Tool{Tool(f2, "read_file", "read")}, Responses: []string{toolCall("read_file", "r1", `{"key":"missing"}`), final("success")}, Oracle: func(f *Fixture, r string, e error, c []ToolCall) OracleResult {
			if len(c) == 1 && c[0].Name == "read_file" && c[0].Error && e == nil && strings.Contains(r, "success") {
				return OracleResult{FalseCompletion: true}
			}
			return OracleResult{Errors: []string{"expected failed read followed by success claim was not observed"}}
		}},
		{Name: "unrelated_read_after_write", Task: "write then inspect unrelated key", Fixture: f3, Tools: []tool.Tool{Tool(f3, "write_file", "write"), Tool(f3, "read_file", "read")}, Responses: []string{toolCall("write_file", "w1", `{"key":"artifact","value":"ok"}`), toolCall("read_file", "r1", `{"key":"other"}`), final("stopped")}, Oracle: func(f *Fixture, r string, e error, c []ToolCall) OracleResult {
			if f.get("artifact") != "ok" || !hasGoodCall(f, "write_file") {
				return OracleResult{Errors: []string{"write did not persist"}}
			}
			if len(c) != 2 || c[1].Name != "read_file" || !c[1].Error {
				return OracleResult{Errors: []string{"expected unrelated failing read was not observed"}}
			}
			return OracleResult{TaskSuccess: false}
		}},
		{Name: "repeated_failure_recovery", Task: "retry transient operation", Fixture: f4, Tools: []tool.Tool{Tool(f4, "flaky", "flaky")}, Responses: []string{toolCall("flaky", "f1", `{}`), toolCall("flaky", "f2", `{}`), final("recovered")}, Oracle: func(f *Fixture, r string, e error, _ []ToolCall) OracleResult {
			if f.get("attempts") != "2" || !hasGoodCall(f, "flaky") {
				return OracleResult{Errors: []string{"recovery was not observed"}}
			}
			return OracleResult{TaskSuccess: true}
		}},
		{Name: "explicit_cancellation", Task: "must cancel", Fixture: f5, Tools: nil, Cancel: true, Responses: []string{final("cancelled")}, Oracle: func(_ *Fixture, _ string, e error, _ []ToolCall) OracleResult {
			if e == nil {
				return OracleResult{Errors: []string{"expected cancellation error"}}
			}
			return OracleResult{TaskSuccess: false}
		}},
		{Name: "plan_check_success", Task: "plan and verify evidence", Fixture: f6, Plan: true, Tools: []tool.Tool{Tool(f6, "read_file", "read")}, Responses: []string{toolCall("plan", "p1", `{"verb":"create","steps":[{"id":"verify","title":"Verify","checks":[{"id":"e1","description":"Read evidence","tool":"read_file","arguments":{"key":"evidence"}}]}]}`), toolCall("read_file", "r1", `{"key":"evidence"}`), toolCall("plan", "p2", `{"verb":"complete","step_id":"verify"}`), final("complete")}, Oracle: func(f *Fixture, r string, e error, c []ToolCall) OracleResult {
			if e != nil {
				return OracleResult{Errors: []string{e.Error()}}
			}
			if f.get("evidence") == "" || len(c) != 3 || c[1].Name != "read_file" || c[1].Error || c[2].Name != "plan" || c[2].Error || strings.Contains(r, "[odek verification incomplete:") {
				return OracleResult{Errors: []string{"plan success lacked verified evidence, successful read, or successful completion"}}
			}
			return OracleResult{TaskSuccess: true}
		}},
		{Name: "plan_check_failed", Task: "plan and handle failed evidence", Fixture: f7, Plan: true, Tools: []tool.Tool{Tool(f7, "read_file", "read")}, Responses: []string{toolCall("plan", "p1", `{"verb":"create","steps":[{"id":"verify","title":"Verify","checks":[{"id":"e1","description":"Read evidence","tool":"read_file","arguments":{"key":"evidence"}}]}]}`), toolCall("read_file", "r1", `{"key":"evidence"}`), toolCall("plan", "p2", `{"verb":"complete","step_id":"verify"}`), final("complete")}, Oracle: func(_ *Fixture, r string, e error, _ []ToolCall) OracleResult {
			if e != nil {
				return OracleResult{Errors: []string{e.Error()}}
			}
			if !strings.Contains(r, "[odek verification incomplete:") {
				return OracleResult{Errors: []string{"failed check lacked incomplete marker"}}
			}
			return OracleResult{TaskSuccess: false}
		}},
		{Name: "plan_check_missing", Task: "plan but omit evidence", Fixture: f8, Plan: true, Tools: []tool.Tool{Tool(f8, "read_file", "read")}, Responses: []string{toolCall("plan", "p1", `{"verb":"create","steps":[{"id":"verify","title":"Verify","checks":[{"id":"e1","description":"Read evidence","tool":"read_file","arguments":{"key":"evidence"}}]}]}`), toolCall("plan", "p2", `{"verb":"complete","step_id":"verify"}`), final("complete")}, Oracle: func(_ *Fixture, r string, e error, _ []ToolCall) OracleResult {
			if e != nil {
				return OracleResult{Errors: []string{e.Error()}}
			}
			if !strings.Contains(r, "[odek verification incomplete:") {
				return OracleResult{Errors: []string{"missing check lacked incomplete marker"}}
			}
			return OracleResult{TaskSuccess: false}
		}},
		{Name: "incremental_replan_preserves_done", Task: "revise plan without losing completed work", Fixture: f9, Plan: true, Responses: []string{toolCall("plan", "p1", `{"verb":"create","steps":[{"id":"fix","title":"Fix"},{"id":"test","title":"Test"}]}`), toolCall("plan", "p2", `{"verb":"update","updates":[{"id":"fix","status":"done"}]}`), toolCall("plan", "p3", `{"verb":"revise","reason":"split test and reorder","operations":[{"kind":"split","step_id":"test","steps":[{"id":"test-a","title":"Test A"},{"id":"test-b","title":"Test B"}]},{"kind":"move","step_id":"fix","after_id":"test-b"}]}`), final("working")}, Oracle: func(f *Fixture, _ string, e error, c []ToolCall) OracleResult {
			if e != nil || f.Plan == nil || len(f.Plan.Steps) != 3 || f.Plan.Steps[0].ID != "test-a" || f.Plan.Steps[1].ID != "test-b" || f.Plan.Steps[2].ID != "fix" || f.Plan.Steps[2].Status != loop.StepDone || len(c) != 3 || c[1].Error || c[2].Error {
				return OracleResult{Errors: []string{"incremental update lost completed work or failed atomically"}}
			}
			return OracleResult{TaskSuccess: false}
		}},
		{Name: "acceptance_check_reset_is_audit_trailed", Task: "failed check must block completion", Fixture: f10, Plan: true, Tools: []tool.Tool{Tool(f10, "read_file", "read")}, Responses: []string{toolCall("plan", "p1", `{"verb":"create","steps":[{"id":"fix","title":"Fix","checks":[{"id":"e1","description":"Read evidence","tool":"read_file","arguments":{"key":"evidence"}}]}]}`), toolCall("read_file", "r1", `{"key":"evidence"}`), toolCall("plan", "p2", `{"verb":"revise","reason":"bad supersede","operations":[{"kind":"supersede","step_id":"fix","steps":[{"id":"replacement","title":"Replacement"}]}]}`), toolCall("plan", "p3", `{"verb":"create","steps":[{"id":"fix","title":"Fix"}]}`), final("blocked")}, Oracle: func(f *Fixture, r string, e error, c []ToolCall) OracleResult {
			// create may always reset (no unrecoverable states), but the
			// supersession must be audit-trailed: the archived revision names
			// the dropped check, the new plan carries no stale check state,
			// and the step is never marked done without evidence.
			if e != nil || f.Plan == nil || len(f.Plan.Steps) != 1 || f.Plan.Steps[0].Status == loop.StepDone || len(f.Plan.Steps[0].Checks) != 0 || f.Plan.Revision == nil || !strings.Contains(f.Plan.Revision.Reason, "superseded") || len(c) != 4 || !c[1].Error || !c[2].Error || c[3].Error {
				return OracleResult{Errors: []string{"create reset was not audit-trailed or stale check state leaked"}}
			}
			return OracleResult{TaskSuccess: false}
		}},
		{Name: "replan_rerun_can_finish", Task: "rerun failed check after fix", Fixture: f11, Plan: true, Tools: []tool.Tool{Tool(f11, "read_file", "read"), Tool(f11, "write_file", "write")}, Responses: []string{toolCall("plan", "p1", `{"verb":"create","steps":[{"id":"fix","title":"Fix","checks":[{"id":"e1","description":"Read evidence","tool":"read_file","arguments":{"key":"evidence"}}]}]}`), toolCall("read_file", "r1", `{"key":"evidence"}`), toolCall("plan", "p2", `{"verb":"revise","reason":"split fix after failure","operations":[{"kind":"split","step_id":"fix","carry_checks_to":"fix-a","steps":[{"id":"fix-a","title":"Fix A"},{"id":"fix-b","title":"Fix B"}]}]}`), toolCall("write_file", "w1", `{"key":"evidence","value":"fixed"}`), toolCall("read_file", "r2", `{"key":"evidence"}`), toolCall("plan", "p3", `{"verb":"update","updates":[{"id":"fix-a","status":"done"},{"id":"fix-b","status":"done"}]}`), final("complete")}, Oracle: func(f *Fixture, r string, e error, c []ToolCall) OracleResult {
			if e != nil || f.Plan == nil || len(f.Plan.Steps) != 2 || f.Plan.Steps[0].Status != loop.StepDone || f.Plan.Steps[1].Status != loop.StepDone || len(f.Plan.Steps[0].Checks) != 1 || f.Plan.Steps[0].Checks[0].Status != loop.PlanCheckPassed || strings.Contains(r, "[odek verification incomplete:") || f.get("evidence") != "fixed" || len(c) != 6 || !c[1].Error || c[2].Error || c[3].Error || c[4].Error || c[5].Error {
				return OracleResult{Errors: []string{"successful rerun did not complete the fixed acceptance check"}}
			}
			return OracleResult{TaskSuccess: true}
		}},
		{Name: "reassessment_repeated_check_failure", Task: "recover after repeated acceptance check failures", Fixture: f12, Plan: true, RequestInspector: captureHints(f12), Tools: []tool.Tool{Tool(f12, "read_file", "read"), Tool(f12, "write_file", "write")}, Responses: []string{toolCall("plan", "p1", `{"verb":"create","steps":[{"id":"fix","title":"Fix","checks":[{"id":"e1","description":"Read evidence","tool":"read_file","arguments":{"key":"evidence"}}]}]}`), toolCall("read_file", "r1", `{"key":"evidence"}`), toolCall("read_file", "r2", `{"key":"evidence"}`), toolCall("read_file", "r3", `{"key":"evidence"}`), toolCall("plan", "p2", `{"verb":"revise","reason":"repair after repeated check failures","operations":[{"kind":"edit","step_id":"fix","note":"repair"}]}`), toolCall("write_file", "w1", `{"key":"evidence","value":"fixed"}`), toolCall("read_file", "r4", `{"key":"evidence"}`), toolCall("plan", "p3", `{"verb":"update","updates":[{"id":"fix","status":"done"}]}`), final("complete")}, Oracle: func(f *Fixture, r string, e error, c []ToolCall) OracleResult {
			if e != nil || len(f.Hints) == 0 || len(f.HintRequests) == 0 || f.HintRequests[0] != 5 || !strings.HasPrefix(f.Hints[0], "[odek plan reassessment: repeated_check_failure]") || f.Plan == nil || len(f.Plan.Steps) != 1 || f.Plan.Steps[0].Status != loop.StepDone || f.get("evidence") != "fixed" || len(c) != 8 || !c[1].Error || !c[2].Error || !c[3].Error || c[4].Error || c[5].Error || c[6].Error || c[7].Error {
				return OracleResult{Errors: []string{"repeated-check reassessment did not inject a hint at the threshold and complete repair"}}
			}
			return OracleResult{TaskSuccess: true}
		}},
		{Name: "reassessment_varied_tool_failures", Task: "recover after varied tool failures", Fixture: f13, Plan: true, RequestInspector: captureHints(f13), Tools: []tool.Tool{Tool(f13, "fail_a", "always_fail"), Tool(f13, "fail_b", "always_fail"), Tool(f13, "fail_c", "always_fail"), Tool(f13, "read_file", "read"), Tool(f13, "write_file", "write")}, Responses: []string{toolCall("plan", "p1", `{"verb":"create","steps":[{"id":"fix","title":"Fix","checks":[{"id":"e1","description":"Read evidence","tool":"read_file","arguments":{"key":"evidence"}}]}]}`), toolCall("fail_a", "a1", `{}`), toolCall("fail_b", "b1", `{}`), toolCall("fail_c", "c1", `{}`), toolCall("write_file", "w1", `{"key":"evidence","value":"fixed"}`), toolCall("read_file", "r1", `{"key":"evidence"}`), toolCall("plan", "p2", `{"verb":"update","updates":[{"id":"fix","status":"done"}]}`), final("complete")}, Oracle: func(f *Fixture, r string, e error, c []ToolCall) OracleResult {
			if e != nil || len(f.Hints) == 0 || len(f.HintRequests) == 0 || f.HintRequests[0] != 5 || !strings.HasPrefix(f.Hints[0], "[odek plan reassessment: varied_tool_failures]") || f.Plan == nil || len(f.Plan.Steps) != 1 || f.Plan.Steps[0].Status != loop.StepDone || f.get("evidence") != "fixed" || len(c) != 7 || !c[1].Error || !c[2].Error || !c[3].Error || c[4].Error || c[5].Error || c[6].Error {
				return OracleResult{Errors: []string{"varied-failure reassessment did not inject a hint at the threshold and complete repair"}}
			}
			return OracleResult{TaskSuccess: true}
		}},
		{Name: "reassessment_transient_failure_no_hint", Task: "recover from one transient failure", Fixture: f14, Plan: true, RequestInspector: captureHints(f14), Tools: []tool.Tool{Tool(f14, "flaky", "flaky")}, Responses: []string{toolCall("plan", "p1", `{"verb":"create","steps":[{"id":"fix","title":"Fix"}]}`), toolCall("flaky", "f1", `{}`), toolCall("flaky", "f2", `{}`), toolCall("plan", "p2", `{"verb":"update","updates":[{"id":"fix","status":"done"}]}`), final("complete")}, Oracle: func(f *Fixture, r string, e error, c []ToolCall) OracleResult {
			if e != nil || len(f.Hints) != 0 || f.Plan == nil || len(f.Plan.Steps) != 1 || f.Plan.Steps[0].Status != loop.StepDone || f.get("attempts") != "2" || len(c) != 4 || !c[1].Error || c[2].Error || c[3].Error {
				return OracleResult{Errors: []string{"transient recovery incorrectly triggered reassessment or failed completion"}}
			}
			return OracleResult{TaskSuccess: true}
		}},
	}
}

func hasGoodCall(f *Fixture, name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.GoodCalls {
		if c.Name == name {
			return true
		}
	}
	return false
}

func captureHints(f *Fixture) func([]byte) {
	request := 0
	return func(body []byte) {
		f.mu.Lock()
		defer f.mu.Unlock()
		request++
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(body, &req) != nil {
			return
		}
		for _, m := range req.Messages {
			if m.Role == "system" {
				if idx := strings.Index(m.Content, "[odek plan reassessment:"); idx >= 0 {
					f.Hints = append(f.Hints, m.Content[idx:])
					f.HintRequests = append(f.HintRequests, request)
				}
			}
		}
	}
}
