package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llmclient"
	"github.com/BackendStack21/odek/internal/redact"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// A command that opens a subshell or group with an assignment leaks the
// assignment value (often a credential) into args_summary.argv0.
func TestRED_Argv0SubshellAssignmentLeaksValue(t *testing.T) {
	for _, cmd := range []string{
		`(PGPASSWORD=hunter2xyz psql -h db)`,
		`(MYKEY=hunter2xyz; curl https://example.com)`,
		`$(MYKEY=hunter2xyz curl -s x)`,
		"`MYKEY=hunter2xyz curl -s x`",
	} {
		got := redact.RedactSecrets(argv0(cmd))
		if strings.Contains(got, "hunter2xyz") {
			t.Errorf("argv0(%q) = %q: leaks the assignment value", cmd, got)
		}
	}
}

func loopHuntAnthropicClient(t *testing.T, baseURL string) *llmclient.Client {
	t.Helper()
	s, err := llmclient.NewSDK(llmclient.Options{
		Provider: "anth",
		Model:    "claude-test",
		APIKey:   "sk-test",
		BaseURL:  baseURL,
		Providers: map[string]llmclient.ProviderOverride{
			"anth": {APIKey: "sk-test", BaseURL: baseURL, Format: "anthropic"},
		},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := llmclient.New(s, "anth", "claude-test")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Each resumed run in a new process (odek continue, a rebuilt engine) keeps
// the previous run's memory block — with its persisted cache_control marker —
// and inserts a fresh one. Stale memory copies accumulate in the history and
// every one of them is sent as an Anthropic cache breakpoint; Anthropic
// rejects requests with more than 4 cache_control blocks.
func TestRED_ResumedMemoryBlocksAccumulateCacheBreakpoints(t *testing.T) {
	var lastBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		lastBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()

	mintedBoundaries.Lock()
	saved := mintedBoundaries.m
	mintedBoundaries.Unlock()
	t.Cleanup(func() {
		mintedBoundaries.Lock()
		mintedBoundaries.m = saved
		mintedBoundaries.Unlock()
	})

	msgs := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "turn 1"}}
	for turn := 1; turn <= 4; turn++ {
		// A new process: nothing in the history was minted here.
		mintedBoundaries.Lock()
		mintedBoundaries.m = make(map[[32]byte]uint64)
		mintedBoundaries.Unlock()
		eng := New(loopHuntAnthropicClient(t, server.URL), tool.NewRegistry(nil), 10, "sys", nil, 0)
		eng.PromptCaching = true
		eng.SetMemoryPromptFunc(func() string { return "MEM" })
		if turn > 1 {
			msgs = append(msgs, session.Message{Role: "user", Content: fmt.Sprintf("turn %d", turn)})
		}
		var err error
		if _, msgs, err = eng.RunWithMessages(context.Background(), msgs); err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
	}
	memCopies := 0
	for _, m := range msgs {
		if m.Role == "system" && strings.Contains(m.Content, "MEM") {
			memCopies++
		}
	}
	if memCopies != 1 {
		t.Errorf("history carries %d memory blocks after 4 resumed runs, want 1", memCopies)
	}
	if n := strings.Count(lastBody, "cache_control"); n > 4 {
		t.Errorf("request carries %d cache_control blocks; Anthropic allows at most 4", n)
	}
}

// The iteration-budget progress summarizer byte-slices each message snippet
// at budgetSummarySnippetBytes, splitting a multibyte rune — the sibling of
// the fixed compaction-sketch cut. The side-call prompt then carries invalid
// UTF-8 (sent as U+FFFD replacement characters).
func TestRED_ProgressSummarySnippetCutsMidRune(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"summary"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	eng := New(testChatClient(t, server.URL), tool.NewRegistry(nil), 10, "sys", nil, 0)
	// 'x' + 'é'*N: byte 2000 falls inside a two-byte rune.
	content := "x" + strings.Repeat("é", budgetSummarySnippetBytes)
	if got := eng.summarizeProgress(context.Background(), []session.Message{{Role: "user", Content: content}}); got == "" {
		t.Fatal("summary side call did not run")
	}
	var req struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	for _, m := range req.Messages {
		if strings.ContainsRune(m.Content, '�') {
			t.Fatalf("progress-summary prompt carries a replacement character from a mid-rune cut")
		}
	}
}

// The 4 KiB tool-output compression keeps a 3 KiB head and 1 KiB tail by
// raw byte slicing, so a multibyte rune at the head cut is split and the
// model receives a replacement character in place of real output.
func TestRED_ToolOutputCompressionCutsMidRune(t *testing.T) {
	var calls int
	var second []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls++
		if calls == 1 {
			fmt.Fprint(w, budgetToolCallResponse("c1", "big", 1, 1))
			return
		}
		second = b
		fmt.Fprint(w, budgetFinalResponse("done", 1, 1))
	}))
	defer server.Close()

	// 'x' + 'é'*3000: the 3072-byte head cut lands inside a rune.
	big := &fakeTool{name: "big", output: "x" + strings.Repeat("é", 3000)}
	eng := New(testChatClient(t, server.URL), tool.NewRegistry([]tool.Tool{big}), 10, "sys", nil, 0)
	if _, err := eng.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(second, &req); err != nil {
		t.Fatal(err)
	}
	for _, m := range req.Messages {
		if m.Role == "tool" && strings.ContainsRune(m.Content, '�') {
			t.Fatalf("compressed tool result carries a replacement character from a mid-rune cut")
		}
	}
}

// On Anthropic-format providers every system message is hoisted into the
// top-level system field, so the corrective message the loop appends after a
// superseded draft (verify retry, completion nudge) leaves the superseded
// assistant draft as the LAST message of the next request. Anthropic treats a
// trailing assistant message as a prefill: the model continues the rejected
// draft instead of answering the correction, and requests with extended
// thinking enabled are rejected outright.
func TestRED_SupersededDraftSentAsAnthropicPrefill(t *testing.T) {
	var calls int
	var thirdBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls++
		text := "draft answer"
		switch calls {
		case 2:
			text = `{"verdict":"fail","reasons":["incomplete"],"missing":["x"]}`
		case 3:
			thirdBody = b
			text = "corrected answer"
		case 4:
			text = `{"verdict":"pass","reasons":[],"missing":[]}`
		}
		w.Header().Set("Content-Type", "application/json")
		out, _ := json.Marshal(text)
		fmt.Fprintf(w, `{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":%s}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, out)
	}))
	defer server.Close()

	eng := New(loopHuntAnthropicClient(t, server.URL), tool.NewRegistry(nil), 10, "sys", nil, 0)
	eng.SetVerify(VerifyConfig{Enabled: true, Mode: VerifyModeHint, MaxCycles: 1})
	if _, _, err := eng.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "do the task"}}); err != nil {
		t.Fatal(err)
	}
	if thirdBody == nil {
		t.Fatalf("corrective main call never happened (calls=%d)", calls)
	}
	var req struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(thirdBody, &req); err != nil {
		t.Fatal(err)
	}
	if n := len(req.Messages); n == 0 || req.Messages[n-1].Role != "user" {
		t.Fatalf("corrective request ends with an assistant message (roles=%v): the superseded draft is sent as an assistant prefill", req.Messages)
	}
}
