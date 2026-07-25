package loop

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/tool"
)

// ── Estimators ─────────────────────────────────────────────────────────

func TestEstimateToolDefs_IncludesParameters(t *testing.T) {
	without := estimateToolDefs([]llm.ToolDef{{
		Type:     "function",
		Function: llm.FunctionDef{Name: "shell", Description: "run a command"},
	}})
	with := estimateToolDefs([]llm.ToolDef{{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "shell",
			Description: "run a command",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": strings.Repeat("the command to execute ", 20),
					},
				},
			},
		},
	}})
	if with <= without {
		t.Errorf("estimateToolDefs with schema = %d, want > %d (schema must be counted)", with, without)
	}
}

func TestEstimateMessages_CountsReasoningContent(t *testing.T) {
	plain := estimateMessages([]llm.Message{{Role: "assistant", Content: "answer"}})
	withReasoning := estimateMessages([]llm.Message{{
		Role:             "assistant",
		Content:          "answer",
		ReasoningContent: strings.Repeat("thinking step by step ", 50),
	}})
	if withReasoning <= plain {
		t.Errorf("estimateMessages with reasoning = %d, want > %d", withReasoning, plain)
	}
}

// ── Graduated truncation ───────────────────────────────────────────────

// buildToolConversation returns system + task + n groups of
// (assistant text, tool result of toolBytes bytes).
func buildToolConversation(n, toolBytes int) []llm.Message {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
	}
	for i := 0; i < n; i++ {
		msgs = append(msgs,
			llm.Message{Role: "assistant", Content: fmt.Sprintf("thinking %d", i)},
			llm.Message{Role: "tool", Content: strings.Repeat("x", toolBytes), ToolCallID: fmt.Sprintf("c%d", i)},
		)
	}
	return msgs
}

func TestTrimContext_TruncatesOldToolResults(t *testing.T) {
	msgs := buildToolConversation(6, 4000)
	origLen := len(msgs)
	// Budget: over with all results intact, under once the two oldest
	// (unprotected) tool results are truncated.
	engine := &Engine{maxContext: 7500}
	result := engine.trimContext(context.Background(), msgs, nil)

	// No group may be dropped — only the warning is added.
	if len(result) != origLen+1 {
		t.Fatalf("expected %d messages (no drops + warning), got %d", origLen+1, len(result))
	}
	// The two oldest tool results are truncated; the 4 most recent are not.
	truncated := map[string]bool{}
	for _, m := range result {
		if m.Role == "tool" && strings.Contains(m.Content, "[tool output trimmed:") {
			truncated[m.ToolCallID] = true
		}
	}
	if !truncated["c0"] || !truncated["c1"] || len(truncated) != 2 {
		t.Errorf("expected exactly c0,c1 truncated, got %v", truncated)
	}
	for _, m := range result {
		if m.Role == "tool" && !truncated[m.ToolCallID] && len(m.Content) != 4000 {
			t.Errorf("recent tool result %s changed length: %d", m.ToolCallID, len(m.Content))
		}
	}
	// Warning reports truncation, not group drops.
	var warning string
	for _, m := range result {
		if strings.HasPrefix(m.Content, "[Context trimmed:") {
			warning = m.Content
		}
	}
	if !strings.Contains(warning, "2 tool output(s) truncated") {
		t.Errorf("warning should mention 2 truncated outputs, got %q", warning)
	}
	if strings.Contains(warning, "group(s) dropped") {
		t.Errorf("warning should not mention dropped groups, got %q", warning)
	}
}

func TestTrimContext_TruncationInsufficient_DropsGroups(t *testing.T) {
	msgs := buildToolConversation(6, 4000)
	engine := &Engine{maxContext: 200} // tiny budget — truncation alone can't fit
	result := engine.trimContext(context.Background(), msgs, nil)

	if len(result) >= len(msgs) {
		t.Errorf("expected groups to be dropped, got %d >= %d messages", len(result), len(msgs))
	}
	// No orphaned tool messages: every tool message must follow an assistant.
	for i, m := range result {
		if m.Role == "tool" && i > 0 && result[i-1].Role != "assistant" && result[i-1].Role != "tool" {
			t.Errorf("orphaned tool message at index %d", i)
		}
	}
	var warning string
	for _, m := range result {
		if strings.HasPrefix(m.Content, "[Context trimmed:") {
			warning = m.Content
		}
	}
	if !strings.Contains(warning, "group(s) dropped") {
		t.Errorf("warning should mention dropped groups, got %q", warning)
	}
}

// ── Warning content / placement ────────────────────────────────────────

func TestTrimContext_WarningIncludesDroppedToolNames(t *testing.T) {
	tc := func(id, name string) []llm.ToolCall {
		return []llm.ToolCall{{ID: id, Type: "function", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: name, Arguments: "{}"}}}
	}
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", ToolCalls: tc("c1", "read_file")},
		{Role: "tool", Content: "small", ToolCallID: "c1"},
		{Role: "assistant", Content: strings.Repeat("pad ", 2000)},
		{Role: "user", Content: "latest input"},
	}
	engine := &Engine{maxContext: 400}
	result := engine.trimContext(context.Background(), msgs, nil)

	var warning string
	for _, m := range result {
		if strings.HasPrefix(m.Content, "[Context trimmed:") {
			warning = m.Content
		}
	}
	if warning == "" {
		t.Fatal("expected a trim warning")
	}
	if !strings.Contains(warning, "read_file") {
		t.Errorf("warning should name dropped tools, got %q", warning)
	}
	// Warning sits before the most recent user message, not at the head.
	for i, m := range result {
		if strings.HasPrefix(m.Content, "[Context trimmed:") {
			if i+1 >= len(result) || result[i+1].Role != "user" {
				t.Errorf("warning at index %d should be followed by a user message", i)
			}
		}
	}
}

func TestTrimContext_WarningUpdatesInPlace(t *testing.T) {
	engine := &Engine{maxContext: 600}
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: strings.Repeat("a", 3000)},
		{Role: "assistant", Content: strings.Repeat("b", 3000)},
	}
	result := engine.trimContext(context.Background(), msgs, nil)

	// Second trim on the result with a new oversized message appended.
	result = append(result, llm.Message{Role: "assistant", Content: strings.Repeat("c", 3000)})
	result = engine.trimContext(context.Background(), result, nil)

	count := 0
	var warning string
	for _, m := range result {
		if strings.HasPrefix(m.Content, "[Context trimmed:") {
			count++
			warning = m.Content
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 trim warning, got %d", count)
	}
	if !strings.Contains(warning, "3 prior message group(s) dropped") {
		t.Errorf("warning should carry cumulative drop count 3, got %q", warning)
	}
}

// ── Margin calibration ─────────────────────────────────────────────────

func TestTrimContext_MarginCalibration(t *testing.T) {
	var signals []SignalEvent
	engine := &Engine{
		maxContext:               100_000,
		lastEstimatedTotal:       10_000,
		lastReportedInputTokens:  20_000, // 2x the estimate → > 15% off
		maxConsecutiveToolErrors: map[string]int{},
		trimDroppedTools:         map[string]int{},
	}
	engine.SetSignalHandler(func(ev SignalEvent) { signals = append(signals, ev) })

	// ~68k estimated tokens: fits the default 75% margin (75k) but not the
	// tightened 65% margin (65k).
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: strings.Repeat("x", 268_000)},
	}
	result := engine.trimContext(context.Background(), msgs, nil)

	if !engine.tightMargin {
		t.Error("margin should have tightened after provider reported 2x the estimate")
	}
	calibrated := false
	for _, ev := range signals {
		if ev.Type == "context_trimmed" && ev.Detail == "margin_calibrated" {
			calibrated = true
		}
	}
	if !calibrated {
		t.Error("expected a margin_calibrated signal")
	}
	if len(result) >= len(msgs)+1 {
		t.Errorf("expected trimming under the tightened margin, got %d messages", len(result))
	}
}

// ── Post-injection budget re-check ─────────────────────────────────────

func TestTrimContext_PostInjectionBudget(t *testing.T) {
	var bodies []string
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(data))
		callCount++
		if callCount == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"c1","function":{"name":"echo","arguments":"{}"}}]}}]}`)
		} else {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
		}
	}))
	defer server.Close()

	echoTool := &fakeTool{name: "echo", description: "echo", output: "ok"}
	registry := tool.NewRegistry([]tool.Tool{echoTool})
	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	// Budget 1500 tokens — the injected skill block (~2500 tokens) must be
	// dropped before the very first API call, not one iteration later.
	engine := New(client, registry, 5, "sys", nil, 2000)
	engine.SetSkillLoader(func(string) string { return strings.Repeat("SKILLDATA ", 1000) })

	if _, err := engine.Run(context.Background(), "do it"); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(bodies) == 0 {
		t.Fatal("no requests recorded")
	}
	if strings.Contains(bodies[0], "SKILLDATA") {
		t.Error("first API call carried the oversized injected skill block — post-injection trim missing")
	}
}

// ── trimToSurvival ─────────────────────────────────────────────────────

func survivalTC(id, name string) []llm.ToolCall {
	return []llm.ToolCall{{ID: id, Type: "function", Function: struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Name: name, Arguments: "{}"}}}
}

func TestTrimToSurvival_KeepsOriginalTask(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "original task"},
		{Role: "assistant", ToolCalls: survivalTC("c1", "read_file")},
		{Role: "tool", Content: "r1", ToolCallID: "c1"},
		{Role: "user", Content: "follow-up question"},
		{Role: "assistant", ToolCalls: survivalTC("c2", "shell")},
		{Role: "tool", Content: "r2", ToolCallID: "c2"},
		{Role: "user", Content: "latest input"},
	}
	got := trimToSurvival(msgs)

	foundTask := false
	for _, m := range got {
		if m.Role == "user" && m.Content == "original task" {
			foundTask = true
		}
	}
	if !foundTask {
		t.Error("original task message must survive the survival trim")
	}
	last := got[len(got)-1]
	if last.Role != "user" || last.Content != "latest input" {
		t.Errorf("last message = %q/%q, want user/latest input", last.Role, last.Content)
	}
	// Task must appear before the last user message.
	taskIdx, latestIdx := -1, -1
	for i, m := range got {
		if m.Role == "user" && m.Content == "original task" {
			taskIdx = i
		}
		if m.Role == "user" && m.Content == "latest input" {
			latestIdx = i
		}
	}
	if taskIdx < 0 || latestIdx < 0 || taskIdx > latestIdx {
		t.Errorf("task at %d, latest at %d — bad ordering", taskIdx, latestIdx)
	}
}

func TestTrimToSurvival_NoUserMessage(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "assistant", ToolCalls: survivalTC("c1", "echo")},
		{Role: "tool", Content: "r1", ToolCallID: "c1"},
		{Role: "assistant", Content: "thinking out loud"},
	}
	got := trimToSurvival(msgs)
	for i, m := range got {
		if m.Role == "" {
			t.Errorf("message %d has empty role — zero-value message leaked", i)
		}
	}
}

func TestTrimToSurvival_PreservesDigest(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "system", Content: digestMsgPrefix + " summary of old work]\ndigest body"},
		{Role: "user", Content: "task"},
		{Role: "assistant", ToolCalls: survivalTC("c1", "echo")},
		{Role: "tool", Content: "r1", ToolCallID: "c1"},
		{Role: "user", Content: "latest"},
	}
	got := trimToSurvival(msgs)
	found := false
	for _, m := range got {
		if isDigestMessage(m) {
			found = true
		}
	}
	if !found {
		t.Error("compaction digest must survive the survival trim")
	}
}

// ── Rolling compaction ─────────────────────────────────────────────────

func TestTrimContext_CompactionCreatesDigest(t *testing.T) {
	summaryCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		summaryCalls++
		fmt.Fprint(w, `{"choices":[{"message":{"content":"SUMMARY: earlier work condensed"}}]}`)
	}))
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	engine := New(client, tool.NewRegistry(nil), 10, "", nil, 200)
	engine.SetCompaction(true)

	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: strings.Repeat("a", 3000)},
		{Role: "assistant", Content: strings.Repeat("b", 3000)},
		{Role: "assistant", Content: strings.Repeat("c", 3000)},
	}
	result := engine.trimContext(context.Background(), msgs, nil)

	if engine.compactDigest != "SUMMARY: earlier work condensed" {
		t.Errorf("compactDigest = %q, want summary", engine.compactDigest)
	}
	digestCount := 0
	for _, m := range result {
		if isDigestMessage(m) {
			digestCount++
			if !strings.Contains(m.Content, "SUMMARY: earlier work condensed") {
				t.Errorf("digest message missing summary: %q", m.Content)
			}
		}
	}
	if digestCount != 1 {
		t.Fatalf("expected exactly 1 digest message, got %d", digestCount)
	}
	if summaryCalls == 0 {
		t.Error("summarizer was never called")
	}

	// A second trim updates the existing digest in place.
	result = append(result, llm.Message{Role: "assistant", Content: strings.Repeat("d", 3000)})
	result = engine.trimContext(context.Background(), result, nil)
	digestCount = 0
	for _, m := range result {
		if isDigestMessage(m) {
			digestCount++
		}
	}
	if digestCount != 1 {
		t.Errorf("after second trim: expected 1 digest message, got %d", digestCount)
	}
}

func TestTrimContext_CompactionFailureStillTrims(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	engine := New(client, tool.NewRegistry(nil), 10, "", nil, 200)
	engine.SetCompaction(true)

	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: strings.Repeat("a", 3000)},
		{Role: "assistant", Content: strings.Repeat("b", 3000)},
	}
	result := engine.trimContext(context.Background(), msgs, nil)

	for _, m := range result {
		if isDigestMessage(m) {
			t.Error("no digest should be inserted when the summarizer fails")
		}
	}
	if len(result) >= len(msgs) {
		t.Errorf("trimming must still happen on summarizer failure, got %d messages", len(result))
	}
}

func TestTrimContext_CompactionWrapsUntrusted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"digest"}}]}`)
	}))
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	engine := New(client, tool.NewRegistry(nil), 10, "", nil, 200)
	engine.SetCompaction(true)
	engine.SetUntrustedWrapper(func(source, content string) string {
		return "<untrusted source=" + source + ">" + content + "</untrusted>"
	})

	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: strings.Repeat("a", 3000)},
	}
	result := engine.trimContext(context.Background(), msgs, nil)

	found := false
	for _, m := range result {
		if isDigestMessage(m) {
			found = true
			if !strings.Contains(m.Content, "<untrusted source=compaction>") {
				t.Errorf("digest body not wrapped as untrusted: %q", m.Content)
			}
		}
	}
	if !found {
		t.Error("expected a digest message")
	}
}

// ── Coverage: estimator fallback ───────────────────────────────────────

func TestEstimateToolDefs_UnmarshalableSchema(t *testing.T) {
	base := estimateToolDefs([]llm.ToolDef{{
		Type:     "function",
		Function: llm.FunctionDef{Name: "shell", Description: "run a command"},
	}})
	withBad := estimateToolDefs([]llm.ToolDef{{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "shell",
			Description: "run a command",
			Parameters:  map[string]any{"bad": func() {}}, // json.Marshal fails
		},
	}})
	if withBad != base+200 {
		t.Errorf("unmarshalable schema should add the 200-token fallback: got %d, want %d", withBad, base+200)
	}
}

// ── Coverage: small tool results are never truncated ───────────────────

func TestTrimContext_SmallToolResultNotTruncated(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
	}
	// 6 tool messages → the oldest 2 are unprotected: one small (skipped by
	// the truncation pass), one big (truncated).
	sizes := []int{500, 4000, 4000, 4000, 4000, 4000}
	for i, sz := range sizes {
		msgs = append(msgs,
			llm.Message{Role: "assistant", Content: fmt.Sprintf("thinking %d", i)},
			llm.Message{Role: "tool", Content: strings.Repeat("x", sz), ToolCallID: fmt.Sprintf("c%d", i)},
		)
	}
	origLen := len(msgs)
	// Budget forces exactly the big unprotected result to be truncated.
	engine := &Engine{maxContext: 7000}
	result := engine.trimContext(context.Background(), msgs, nil)

	if len(result) != origLen+1 {
		t.Fatalf("expected no drops (only warning added), got %d != %d", len(result), origLen+1)
	}
	for _, m := range result {
		if m.Role != "tool" {
			continue
		}
		switch m.ToolCallID {
		case "c0":
			if len(m.Content) != 500 {
				t.Errorf("small unprotected result c0 must stay intact, got len=%d", len(m.Content))
			}
		case "c1":
			if !strings.Contains(m.Content, "[tool output trimmed:") {
				t.Errorf("big unprotected result c1 should be truncated")
			}
		}
	}
}

// ── Coverage: warning caps dropped tool names ──────────────────────────

func TestBuildTrimWarning_CapsToolNames(t *testing.T) {
	engine := &Engine{
		trimGroupsTotal:  1,
		trimDroppedTools: map[string]int{},
	}
	for _, name := range []string{"aaa", "bbb", "ccc", "ddd", "eee", "fff", "ggg"} {
		engine.trimDroppedTools[name] = 1
	}
	warning := engine.buildTrimWarning()
	if !strings.Contains(warning, "eee") {
		t.Errorf("warning should include the first 5 sorted names, got %q", warning)
	}
	if strings.Contains(warning, "fff") || strings.Contains(warning, "ggg") {
		t.Errorf("warning should cap dropped tool names at 5, got %q", warning)
	}
}

// ── Coverage: warning upsert edge cases ────────────────────────────────

func TestUpsertTrimWarning_EdgeCases(t *testing.T) {
	// No user message — warning goes to index 1.
	msgs := []llm.Message{{Role: "assistant", Content: "a"}}
	got := upsertTrimWarning(msgs, "[Context trimmed: x]")
	if len(got) != 2 || got[1].Content != "[Context trimmed: x]" {
		t.Errorf("no-user case: got %+v", got)
	}
	// Empty slice — insertion index clamps to 0.
	got = upsertTrimWarning(nil, "[Context trimmed: x]")
	if len(got) != 1 || got[0].Content != "[Context trimmed: x]" {
		t.Errorf("empty case: got %+v", got)
	}
	// Task at index 0 (no system prompt) — warning clamps to index 1 so the
	// session still starts with the task.
	got = upsertTrimWarning([]llm.Message{{Role: "user", Content: "task"}}, "[Context trimmed: x]")
	if len(got) != 2 || got[0].Role != "user" || got[1].Content != "[Context trimmed: x]" {
		t.Errorf("task-first case: got %+v", got)
	}
}

// ── Coverage: survival keeps preceding system messages in a group ──────

func TestTrimToSurvival_IncludesPrecedingSystemMessages(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "system", Content: "correction note"},
		{Role: "assistant", ToolCalls: survivalTC("c1", "echo")},
		{Role: "tool", Content: "r1", ToolCallID: "c1"},
		{Role: "user", Content: "latest"},
	}
	got := trimToSurvival(msgs)
	found := false
	for _, m := range got {
		if m.Content == "correction note" {
			found = true
		}
	}
	if !found {
		t.Error("system message preceding the assistant tool-call group must survive")
	}
}

// ── Coverage: summarizeDropped branches ────────────────────────────────

func TestSummarizeDropped_NilClient(t *testing.T) {
	engine := &Engine{}
	if got := engine.summarizeDropped(context.Background(), []llm.Message{{Role: "assistant", Content: "x"}}); got != "" {
		t.Errorf("nil client must return empty, got %q", got)
	}
}

func TestSummarizeDropped_EmptyContent(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"choices":[{"message":{"content":"digest"}}]}`)
	}))
	defer server.Close()

	engine := New(llm.New(server.URL, "sk-test", "m", "", 0, 0), tool.NewRegistry(nil), 10, "", nil, 0)
	got := engine.summarizeDropped(context.Background(), []llm.Message{{Role: "assistant", Content: ""}})
	if got != "" {
		t.Errorf("empty dropped content must return empty, got %q", got)
	}
	if calls != 0 {
		t.Errorf("summarizer must not be called for empty input, got %d calls", calls)
	}
}

func TestSummarizeDropped_InputBuilding(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(data))
		fmt.Fprint(w, `{"choices":[{"message":{"content":"digest"}}]}`)
	}))
	defer server.Close()

	newEngine := func() *Engine {
		return New(llm.New(server.URL, "sk-test", "m", "", 0, 0), tool.NewRegistry(nil), 10, "", nil, 0)
	}

	// Assistant tool-call names are included in the summarizer input.
	e := newEngine()
	e.summarizeDropped(context.Background(), []llm.Message{{
		Role:      "assistant",
		ToolCalls: survivalTC("c1", "read_file"),
	}})
	if !strings.Contains(bodies[len(bodies)-1], "[called tools: read_file]") {
		t.Errorf("tool-call names missing from summarizer input: %.200s", bodies[len(bodies)-1])
	}

	// Long message content is snippet-truncated.
	e = newEngine()
	e.summarizeDropped(context.Background(), []llm.Message{{
		Role:    "tool",
		Content: strings.Repeat("y", 3000),
	}})
	if strings.Contains(bodies[len(bodies)-1], strings.Repeat("y", 2000)) {
		t.Error("long message content should be snippet-truncated in summarizer input")
	}

	// A previous digest is included for rolling extension.
	e = newEngine()
	e.compactDigest = "OLD DIGEST"
	e.summarizeDropped(context.Background(), []llm.Message{{Role: "assistant", Content: "new work"}})
	body := bodies[len(bodies)-1]
	if !strings.Contains(body, "Previous digest") || !strings.Contains(body, "OLD DIGEST") {
		t.Errorf("previous digest missing from summarizer input: %.200s", body)
	}

	// The raw source is capped at compactionMaxSourceBytes.
	e = newEngine()
	big := make([]llm.Message, 0, 40)
	for i := 0; i < 40; i++ {
		big = append(big, llm.Message{Role: "assistant", Content: strings.Repeat("z", 1000)})
	}
	e.summarizeDropped(context.Background(), big)
	if len(bodies[len(bodies)-1]) > compactionMaxSourceBytes+4096 {
		t.Errorf("summarizer input exceeds source cap: %d bytes", len(bodies[len(bodies)-1]))
	}
}

// ── Coverage: stale memMsgIdx invariant guard ──────────────────────────

func TestRunLoop_StaleMemMsgIdxReset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	engine := New(client, tool.NewRegistry(nil), 1, "sys", nil, 0)
	// Simulate a stale memory-message index pointing at a non-system message.
	engine.memMsgIdx = 1

	_, _, err := engine.runLoop(context.Background(), []llm.Message{
		{Role: "system", Content: "s"},
		{Role: "user", Content: "task"},
	})
	if err != nil {
		t.Fatalf("runLoop() error: %v", err)
	}
	if engine.memMsgIdx != -1 {
		t.Errorf("stale memMsgIdx should be reset to -1, got %d", engine.memMsgIdx)
	}
}
