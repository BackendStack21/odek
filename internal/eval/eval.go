// Package eval provides a deterministic, local-only harness for exercising the
// production loop.Engine. It evaluates runtime behavior and fixture state; it
// does not measure live-model intelligence.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
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
	Values    map[string]string
	Calls     []ToolCall
	GoodCalls []ToolCall
	mu        sync.Mutex
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
	Name      string
	Task      string
	Responses []string
	Tools     []tool.Tool
	Fixture   *Fixture
	Oracle    func(*Fixture, string, error, []ToolCall) OracleResult
	Cancel    bool
	Plan      bool
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
	default:
		return "", fmt.Errorf("unknown fixture")
	}
}

func toolCall(name, id, args string) string {
	b, _ := json.Marshal(args)
	return fmt.Sprintf(`{"choices":[{"message":{"content":"","tool_calls":[{"id":%q,"type":"function","function":{"name":%q,"arguments":%s}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`, id, name, b)
}
func final(text string) string {
	b, _ := json.Marshal(text)
	return fmt.Sprintf(`{"choices":[{"message":{"content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`, b)
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
