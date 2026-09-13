package loop

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/llmclient"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

type contractTool struct {
	name string
	run  func(string) (string, error)
}

func (t *contractTool) Name() string                  { return t.name }
func (t *contractTool) Description() string           { return "contract fixture" }
func (t *contractTool) Schema() any                   { return map[string]any{"type": "object"} }
func (t *contractTool) Call(a string) (string, error) { return t.run(a) }

func contractServer(t *testing.T, batch string) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			fmt.Fprint(w, batch)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}]}`)
	}))
}

func TestConflictingReadWaitsForWrite(t *testing.T) {
	srv := contractServer(t, `{"choices":[{"message":{"tool_calls":[{"id":"w","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"same.go\"}"}},{"id":"r","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"same.go\"}"}}]},"finish_reason":"tool_calls"}]}`)
	defer srv.Close()
	started, release, read := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var state, observed atomic.Int32
	w := &contractTool{name: "write_file", run: func(string) (string, error) {
		close(started)
		<-release
		state.Store(1)
		return `{"success":true}`, nil
	}}
	r := &contractTool{name: "read_file", run: func(string) (string, error) { observed.Store(state.Load()); close(read); return "new contents", nil }}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{w, r}), 4, "sys", nil, 0)
	done := make(chan error, 1)
	go func() { _, err := e.Run(context.Background(), "write and verify"); done <- err }()
	<-started
	select {
	case <-read:
		t.Error("read raced ahead of the pending write")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if observed.Load() != 1 || !e.sawReadAfterMutation || e.completionNudged {
		t.Fatalf("observed=%d verified=%v nudged=%v", observed.Load(), e.sawReadAfterMutation, e.completionNudged)
	}
}

func TestIndependentReadsRemainParallel(t *testing.T) {
	srv := contractServer(t, `{"choices":[{"message":{"tool_calls":[{"id":"a","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}},{"id":"b","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"b.go\"}"}}]},"finish_reason":"tool_calls"}]}`)
	defer srv.Close()
	arrivals := make(chan struct{}, 2)
	release := make(chan struct{})
	r := &contractTool{name: "read_file", run: func(string) (string, error) { arrivals <- struct{}{}; <-release; return "contents", nil }}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{r}), 4, "sys", nil, 0)
	done := make(chan error, 1)
	go func() { _, err := e.Run(context.Background(), "inspect both"); done <- err }()
	for range 2 {
		select {
		case <-arrivals:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("independent reads serialized")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestUnrelatedReadCannotVerifyMutation(t *testing.T) {
	e := &Engine{}
	e.recordReadCheck("write_file", `{"path":"changed.go"}`, "", false)
	e.recordReadCheck("read_file", `{"path":"other.go"}`, "", false)
	if e.sawReadAfterMutation {
		t.Fatal("unrelated read verified mutation")
	}
	e.recordReadCheck("read_file", `{"path":"changed.go"}`, "", false)
	if !e.sawReadAfterMutation {
		t.Fatal("matching read did not verify mutation")
	}
	e.recordReadCheck("write_file", `{"path":"changed.go"}`, "", false)
	if e.sawReadAfterMutation {
		t.Fatal("later write retained stale verification")
	}
}

func TestTypedNativeFailureAndErrorLogSuccess(t *testing.T) {
	for _, failure := range []bool{true, false} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			srv := contractServer(t, `{"choices":[{"message":{"tool_calls":[{"id":"a","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"missing\"}"}}]},"finish_reason":"tool_calls"}]}`)
			defer srv.Close()
			r := &contractTool{name: "read_file", run: func(string) (string, error) {
				if failure {
					return `{"error":"file not found"}`, tool.NewPermanentError("file not found")
				}
				return `{"error":"log file ordinary data"}`, nil
			}}
			e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{r}), 4, "sys", nil, 0)
			var event events.Event
			e.SetEventHandler(func(ev events.Event) {
				if ev.Type == events.TypeToolCallFailed || ev.Type == events.TypeToolCallCompleted {
					event = ev
				}
			})
			_, messages, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "read file"}})
			if err != nil {
				t.Fatal(err)
			}
			want := "completed"
			if failure {
				want = "failed"
			}
			for _, m := range messages {
				if m.Role == "tool" {
					if m.ToolOutcome != want {
						t.Fatalf("outcome=%s want %s", m.ToolOutcome, want)
					}
					if !strings.Contains(m.Content, `"error"`) {
						t.Fatal("error envelope content lost")
					}
				}
			}
			if failure && (event.Type != events.TypeToolCallFailed || event.Data["error_class"] != "permanent" || e.maxConsecutiveToolErrors["read_file"] != 1) {
				t.Fatalf("failure contract: %+v", event)
			}
			if !failure && event.Type != events.TypeToolCallCompleted {
				t.Fatalf("ordinary content flagged failed: %+v", event)
			}
		})
	}
}

func TestTruncatedProviderResponseIsPartial(t *testing.T) {
	for _, withTools := range []bool{false, true} {
		t.Run(fmt.Sprint(withTools), func(t *testing.T) {
			msg := `{"content":"Here is the first half of"}`
			if withTools {
				msg = `{"tool_calls":[{"id":"w","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"partial"}}]}`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"choices":[{"message":%s,"finish_reason":"length"}]}`, msg)
			}))
			defer srv.Close()
			var executed atomic.Bool
			w := &contractTool{name: "write_file", run: func(string) (string, error) { executed.Store(true); return "written", nil }}
			e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{w}), 4, "sys", nil, 0)
			answer, messages, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "complete work"}})
			var partial *PartialResponseError
			if !errors.As(err, &partial) || partial.Reason != llmclient.TerminationTruncated || !strings.Contains(answer, "Partial response") {
				t.Fatalf("answer=%q err=%v", answer, err)
			}
			if executed.Load() {
				t.Fatal("truncated tool arguments executed")
			}
			for _, m := range messages {
				if len(m.ToolCalls) > 0 {
					t.Fatal("dangling partial tool call persisted")
				}
			}
		})
	}
}

func TestRuntimeDeadlineCapsThinkAndStreaming(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
					w.(http.Flusher).Flush()
				}
				select {
				case <-r.Context().Done():
				case <-time.After(1300 * time.Millisecond):
					fmt.Fprint(w, budgetFinalResponse("done", 1, 1))
				}
			}))
			defer srv.Close()
			e := New(testChatClient(t, srv.URL), tool.NewRegistry(nil), 2, "sys", nil, 0)
			e.SetLimits(budget.Limits{MaxRuntimeSeconds: 1}, "test-model")
			e.SetStream(stream)
			e.SetDeltaHandler(func(llmclient.Delta) error { return nil })
			start := time.Now()
			answer, err := e.Run(context.Background(), "local check")
			if b, ok := budget.As(err); !ok || b.Limit != budget.LimitRuntime {
				t.Fatalf("answer=%q err=%v", answer, err)
			}
			if time.Since(start) > 1250*time.Millisecond {
				t.Fatalf("runtime exceeded: %v", time.Since(start))
			}
		})
	}
}

func TestRuntimeDeadlineIncludesLegacyRetrieval(t *testing.T) {
	e := New(nil, tool.NewRegistry(nil), 2, "sys", nil, 0)
	e.SetLimits(budget.Limits{MaxRuntimeSeconds: 1}, "test-model")
	release := make(chan struct{})
	finished := make(chan struct{})
	e.SetEpisodeContextFunc(func(string) string { defer close(finished); <-release; return "context" })
	start := time.Now()
	_, err := e.Run(context.Background(), "local check")
	close(release)
	<-finished
	if b, ok := budget.As(err); !ok || b.Limit != budget.LimitRuntime {
		t.Fatalf("err=%v", err)
	}
	if time.Since(start) > 1250*time.Millisecond {
		t.Fatalf("runtime exceeded: %v", time.Since(start))
	}
}

func TestStreamWithoutTerminationIsPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"unfinished\"}}]}\n\n")
	}))
	defer srv.Close()
	e := New(testChatClient(t, srv.URL), tool.NewRegistry(nil), 2, "sys", nil, 0)
	e.SetStream(true)
	e.SetDeltaHandler(func(llmclient.Delta) error { return nil })
	answer, err := e.Run(context.Background(), "finish")
	var partial *PartialResponseError
	if !errors.As(err, &partial) || !strings.Contains(answer, "unfinished") {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
}

func TestBudgetPollingDuringThinkAndReset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, budgetFinalResponse("done", 5, 2)) }))
	defer srv.Close()
	e := New(testChatClient(t, srv.URL), tool.NewRegistry(nil), 2, "sys", nil, 0)
	e.SetLimits(budget.Limits{MaxInputTokens: 100}, "test-model")
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				e.BudgetSnapshot()
				e.BudgetUsage()
			}
		}
	}()
	for range 20 {
		if _, err := e.Run(context.Background(), "check"); err != nil {
			close(stop)
			<-done
			t.Fatal(err)
		}
	}
	close(stop)
	<-done
}
