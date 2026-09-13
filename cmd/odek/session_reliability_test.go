package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/session"
)

type checkpointEffectTool struct {
	path  string
	calls atomic.Int32
}

func (*checkpointEffectTool) Name() string        { return "checkpoint_effect" }
func (*checkpointEffectTool) Description() string { return "Record a completed test effect." }
func (*checkpointEffectTool) Schema() any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *checkpointEffectTool) Call(string) (string, error) {
	t.calls.Add(1)
	if err := os.WriteFile(t.path, []byte("completed"), 0600); err != nil {
		return "", err
	}
	return strings.Repeat("evidence ", 700) + "MIDDLE_EFFECT_EVIDENCE" + strings.Repeat(" evidence", 700), nil
}

func TestServeCompactedMutationSurvivesCancelAndRestart(t *testing.T) {
	afterCheckpoint := make(chan struct{})
	releaseProvider := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(releaseProvider) }) }
	llm := mockLLM(t, func(w http.ResponseWriter, count int) {
		w.Header().Set("Content-Type", "application/json")
		switch count {
		case 1:
			fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"completed-effect","type":"function","function":{"name":"checkpoint_effect","arguments":"{}"}}]}}]}`)
		case 2:
			close(afterCheckpoint)
			<-releaseProvider
			fmt.Fprint(w, `{"choices":[{"message":{"content":"cancelled request"}}]}`)
		default:
			fmt.Fprint(w, `{"choices":[{"message":{"content":"RESUMED_FINAL"}}]}`)
		}
	})
	defer llm.Close()
	defer unblock()
	env := newRestRunEnv(t, llm.URL, nil)
	effect := &checkpointEffectTool{path: filepath.Join(t.TempDir(), "effect")}
	compaction := false
	newAgent := func() *odek.Agent {
		a, err := odek.New(odek.Config{Provider: "deepseek", BaseURL: llm.URL, APIKey: "test-key", Model: "test-model", SystemMessage: "Help the user.", NoProjectFile: true, ContextWindow: 4096, Compaction: compaction, MemoryDir: t.TempDir(), MaxIterations: 3, Tools: []odek.Tool{effect}})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	messages := []session.Message{{Role: "system"}, {Role: "user", Content: "old task"}}
	for i := 0; i < 50; i++ {
		messages = append(messages, session.Message{Role: "assistant", Content: strings.Repeat("old history ", 100)})
	}
	sess, err := env.store.Create(messages, "test-model", "old task")
	if err != nil {
		t.Fatal(err)
	}
	a := newAgent()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan *session.Session, 1)
	go func() {
		var in, out int
		finished <- handlePrompt(ctx, func(map[string]any) {}, env.store, env.resources, env.resolved, a, nil, sess, wsClientMsg{Type: "prompt", SessionID: sess.ID, Content: "make exactly one effect"}, &in, &out, cancel, nil, nil, nil)
	}()
	select {
	case <-afterCheckpoint:
	case <-time.After(5 * time.Second):
		t.Fatal("next model call did not observe the checkpoint")
	}
	checkpoint, err := env.store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertEvidence := func(messages []session.Message) {
		t.Helper()
		for _, m := range messages {
			if m.Role == "tool" && m.ToolCallID == "completed-effect" && strings.Contains(m.Content, "MIDDLE_EFFECT_EVIDENCE") {
				return
			}
		}
		t.Fatal("completed tool evidence was clipped or lost")
	}
	assertEvidence(checkpoint.Messages)
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled run did not return")
	}
	unblock()
	_ = a.Close()
	restarted, err := env.store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertEvidence(restarted.Messages)
	if !strings.Contains(restarted.Messages[len(restarted.Messages)-1].Content, "aborted") {
		t.Fatal("cancelled turn lacks final status")
	}
	a = newAgent()
	defer a.Close()
	var in, out int
	result := handlePrompt(context.Background(), func(map[string]any) {}, env.store, env.resources, env.resolved, a, nil, restarted, wsClientMsg{Type: "prompt", SessionID: sess.ID, Content: "continue without repeating the effect"}, &in, &out, nil, nil, nil, nil)
	if result == nil {
		t.Fatal("resume lost session")
	}
	stored, err := env.store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertEvidence(stored.Messages)
	if effect.calls.Load() != 1 || stored.Messages[len(stored.Messages)-1].Content != "RESUMED_FINAL" {
		t.Fatal("restart repeated the effect or lost its response")
	}
}

func TestServeCompactionPreservesCompletedTurn(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, count int) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"NEW_FINAL_ANSWER"}}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`)
	})
	defer llmSrv.Close()
	env := newRestRunEnv(t, llmSrv.URL, nil)
	agent, err := odek.New(odek.Config{Provider: "deepseek", BaseURL: llmSrv.URL, APIKey: "test-key", Model: "test-model", SystemMessage: "Help the user.", NoProjectFile: true, ContextWindow: 4096, MemoryDir: t.TempDir(), MaxIterations: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	msgs := []session.Message{{Role: "system", Content: ""}, {Role: "user", Content: "original task"}}
	for i := 0; i < 50; i++ {
		msgs = append(msgs, session.Message{Role: "assistant", Content: strings.Repeat("old history ", 100)})
	}
	sess, err := env.store.Create(msgs, "test-model", "old task")
	if err != nil {
		t.Fatal(err)
	}
	var in, out int
	var frames []map[string]any
	defer func() {
		if v := recover(); v != nil {
			t.Errorf("handlePrompt panicked after compaction: %v", v)
		}
		persisted, e := env.store.Load(sess.ID)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, m := range persisted.Messages {
			if strings.Contains(m.Content, "NEW_FINAL_ANSWER") {
				found = true
			}
		}
		if !found {
			t.Errorf("successful final answer missing from persisted session (%d messages)", len(persisted.Messages))
		}
		answers := 0
		for _, frame := range frames {
			if frame["type"] == "token" && frame["content"] == "NEW_FINAL_ANSWER" {
				answers++
			}
		}
		if answers != 1 {
			t.Errorf("final response delivered %d times", answers)
		}
	}()
	handlePrompt(context.Background(), func(m map[string]any) { frames = append(frames, m) }, env.store, env.resources, env.resolved, agent, nil, sess, wsClientMsg{Type: "prompt", SessionID: sess.ID, AuthToken: sess.AuthToken, Content: "continue now"}, &in, &out, nil, nil, nil, nil)
}

func TestServeSessionOwnerSerializesSocketAndWakeWithREST(t *testing.T) {
	for _, surface := range []string{"prompt", "bg_wake"} {
		t.Run(surface, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			llm := mockLLM(t, func(w http.ResponseWriter, count int) {
				if count == 1 {
					close(entered)
					<-release
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"choices":[{"message":{"content":"answer %d"}}]}`, count)
			})
			defer llm.Close()
			defer unblock()
			env := newRestRunEnv(t, llm.URL, nil)
			sess, err := env.store.Create([]session.Message{{Role: "system"}}, "test-model", "seed")
			if err != nil {
				t.Fatal(err)
			}
			first, err := startServeRun(env.resolved, env.system, env.store, env.resources, promptRequest{Content: "REST_A", SessionID: sess.ID, AuthToken: sess.AuthToken})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("first run did not dispatch")
			}
			agent, err := odek.New(odek.Config{Provider: "deepseek", BaseURL: llm.URL, APIKey: "test-key", Model: "test-model", NoProjectFile: true, MemoryDir: t.TempDir(), MaxIterations: 2})
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close()
			finished := make(chan *session.Session, 1)
			go func() {
				var in, out int
				finished <- handlePrompt(context.Background(), func(map[string]any) {}, env.store, env.resources, env.resolved, agent, nil, sess, wsClientMsg{Type: surface, SessionID: sess.ID, Content: "SOCKET_B"}, &in, &out, nil, nil, nil, nil)
			}()
			// A third, unrelated session must dispatch while A owns its session.
			other, err := startServeRun(env.resolved, env.system, env.store, env.resources, promptRequest{Content: "UNRELATED"})
			if err != nil {
				t.Fatal(err)
			}
			if state := waitRunStatus(t, other.ID, 5*time.Second); state["status"] != "completed" {
				t.Fatalf("unrelated run blocked: %v", state["status"])
			}
			select {
			case <-finished:
				t.Fatal("second owner finished before the first released")
			default:
			}
			unblock()
			if state := waitRunStatus(t, first.ID, 5*time.Second); state["status"] != "completed" {
				t.Fatalf("first run: %v", state["status"])
			}
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("socket/wake run did not finish")
			}
			stored, err := env.store.Load(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"REST_A", "SOCKET_B", "answer 1", "answer 3"} {
				found := false
				for _, m := range stored.Messages {
					if m.Content == want {
						found = true
					}
				}
				if !found {
					t.Errorf("lost message %q on %s", want, surface)
				}
			}
		})
	}
}

func TestSessionCancelCancelsActiveAndQueuedPrompts(t *testing.T) {
	a, cancelA := context.WithCancel(context.Background())
	b, cancelB := context.WithCancel(context.Background())
	defer cancelA()
	defer cancelB()
	removeA := registerPromptCancel("queued-test", cancelA)
	defer removeA()
	removeB := registerPromptCancel("queued-test", cancelB)
	defer removeB()
	if !cancelPrompt("queued-test") || a.Err() == nil || b.Err() == nil {
		t.Fatal("session cancellation must include every registered run")
	}
}

func TestServeConflictingExternalSaveFailsRunWithoutOverwriting(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	llm := mockLLM(t, func(w http.ResponseWriter, _ int) {
		close(entered)
		<-release
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"local answer"}}]}`)
	})
	defer llm.Close()
	defer unblock()
	env := newRestRunEnv(t, llm.URL, nil)
	sess, err := env.store.Create([]session.Message{{Role: "user", Content: "seed"}}, "test-model", "seed")
	if err != nil {
		t.Fatal(err)
	}
	run, err := startServeRun(env.resolved, env.system, env.store, env.resources, promptRequest{Content: "local prompt", SessionID: sess.ID, AuthToken: sess.AuthToken})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not dispatch")
	}
	external, err := env.store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	external.Messages = append(external.Messages, session.Message{Role: "assistant", Content: "EXTERNAL_COMMITTED"})
	if err := env.store.Save(external); err != nil {
		t.Fatal(err)
	}
	unblock()
	if state := waitRunStatus(t, run.ID, 5*time.Second); state["status"] != "failed" {
		t.Fatalf("conflicting run reported %v", state["status"])
	}
	stored, err := env.store.Load(sess.ID)
	if err != nil || stored.Messages[len(stored.Messages)-1].Content != "EXTERNAL_COMMITTED" {
		t.Fatal("stale run replaced external checkpoint")
	}
}

func TestServePromptPanicIsContained(t *testing.T) {
	env := newRestRunEnv(t, "http://127.0.0.1:1", nil)
	sess, err := env.store.Create([]session.Message{{Role: "user", Content: "preserved"}}, "test-model", "seed")
	if err != nil {
		t.Fatal(err)
	}
	var in, out int
	failed := false
	// A nil agent simulates a daemon dependency panic before dispatch.
	result := handlePrompt(context.Background(), func(frame map[string]any) {
		if frame["type"] == "error" {
			failed = true
		}
	}, env.store, env.resources, env.resolved, nil, nil, sess, wsClientMsg{SessionID: sess.ID, Content: "next"}, &in, &out, nil, nil, nil, nil)
	if !failed || result == nil || result.ID != sess.ID {
		t.Fatal("panic escaped without a failed turn and session")
	}
	stored, err := env.store.Load(sess.ID)
	if err != nil || stored.Messages[0].Content != "preserved" {
		t.Fatal("panic discarded checkpoint")
	}
}

func TestServeConcurrentRunsRetainBothTurns(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	llmSrv := mockLLM(t, func(w http.ResponseWriter, count int) {
		if count == 1 {
			close(entered)
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"answer %d"}}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`, count)
	})
	defer llmSrv.Close()
	env := newRestRunEnv(t, llmSrv.URL, nil)
	sess, err := env.store.Create([]session.Message{{Role: "system", Content: ""}, {Role: "user", Content: "seed"}, {Role: "assistant", Content: "seed answer"}}, "test-model", "seed")
	if err != nil {
		t.Fatal(err)
	}
	// Create the session token in advance so both requests use the same existing session.
	token := sess.AuthToken
	first, err := startServeRun(env.resolved, env.system, env.store, env.resources, promptRequest{Content: "PROMPT_A", SessionID: sess.ID, AuthToken: token})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("first did not reach provider")
	}
	second, err := startServeRun(env.resolved, env.system, env.store, env.resources, promptRequest{Content: "PROMPT_B", SessionID: sess.ID, AuthToken: token})
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	firstState := waitRunStatus(t, first.ID, 10*time.Second)
	secondState := waitRunStatus(t, second.ID, 10*time.Second)
	if firstState["status"] != "completed" || secondState["status"] != "completed" {
		t.Fatalf("run failure: first=%v second=%v", firstState["status"], secondState["status"])
	}
	persisted, err := env.store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PROMPT_A", "PROMPT_B", "answer 1", "answer 2"} {
		found := false
		for _, m := range persisted.Messages {
			if m.Content == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("completed message %q disappeared from session", want)
		}
	}
}
