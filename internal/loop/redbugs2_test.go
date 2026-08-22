package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/tool"
)

// RED #B7 (L4): Skill context is documented to be injected "right before
// the user message", but the insertion scan skips index 0 and falls back
// to appending — so with the common [system, user] history the block lands
// AFTER the user message.
func TestRED_SkillContextInjectedBeforeUserMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	engine := New(client, tool.NewRegistry(nil), 10, "sys", nil, 0)
	engine.SetSkillLoader(func(string) string { return "SKILLDATA" })

	_, msgs, err := engine.RunWithMessages(context.Background(), []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	skillIdx, userIdx := -1, -1
	for i, m := range msgs {
		switch {
		case strings.Contains(m.Content, "SKILLDATA"):
			skillIdx = i
		case m.Role == "user":
			userIdx = i
		}
	}
	if skillIdx < 0 || userIdx < 0 {
		t.Fatalf("fixture broken: skillIdx=%d userIdx=%d msgs=%+v", skillIdx, userIdx, msgs)
	}
	if skillIdx > userIdx {
		t.Fatalf("skill context injected AFTER the user message (skill@%d user@%d)", skillIdx, userIdx)
	}
}

// slowTool sleeps on every call so heartbeats fire while it runs.
type slowTool struct{ dur time.Duration }

func (s *slowTool) Name() string        { return "slow" }
func (s *slowTool) Description() string { return "slow tool" }
func (s *slowTool) Schema() any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (s *slowTool) Call(args string) (string, error) {
	time.Sleep(s.dur)
	return "done", nil
}

// RED #B8 (L3): Each parallel tool spawns its own heartbeat watchdog that
// invokes SignalHandler from a separate goroutine with no serialization —
// concurrent handler calls race non-thread-safe consumers (the production
// renderer holds no mutex). Validate under `go test -race`: the
// unsynchronized counter below is a race canary.
func TestRED_HeartbeatSignalHandlerNotInvokedConcurrently(t *testing.T) {
	oldInterval := toolHeartbeatInterval
	toolHeartbeatInterval = 15 * time.Millisecond
	t.Cleanup(func() { toolHeartbeatInterval = oldInterval })

	var inHandler, maxConcurrent, total atomic.Int32
	detect := func() {
		cur := inHandler.Add(1)
		defer inHandler.Add(-1)
		for {
			max := maxConcurrent.Load()
			if cur <= max || maxConcurrent.CompareAndSwap(max, cur) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond) // widen the overlap window
		total.Add(1)
	}

	llmCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmCalls++
		if llmCalls == 1 {
			fmt.Fprint(w, `{
				"choices":[{
					"message":{
						"content":"checking",
						"tool_calls":[
							{"id":"c1","function":{"name":"slow","arguments":"{}"}},
							{"id":"c2","function":{"name":"slow","arguments":"{}"}}
						]
					}
				}]
			}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	engine := New(client, tool.NewRegistry([]tool.Tool{&slowTool{dur: 120 * time.Millisecond}}), 10, "", nil, 0)
	engine.SetMaxToolParallel(2)
	engine.SetSignalHandler(func(ev SignalEvent) { detect() })

	if _, err := engine.Run(context.Background(), "run slow tools"); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if total.Load() < 1 {
		t.Error("expected at least one tool_running heartbeat")
	}
	if maxConcurrent.Load() > 1 {
		t.Errorf("SignalHandler invoked concurrently by parallel heartbeat watchdogs (max overlap: %d)", maxConcurrent.Load())
	}
}
