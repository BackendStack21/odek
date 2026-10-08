package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/loop"
)

// The answer-event handler and verification outcome are wired through
// Config to the engine.
func TestAgent_AnswerEventsAndVerifyOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		content := "the answer"
		if strings.Contains(string(body), "You are a strict verifier") {
			content = `{"verdict":"fail","reasons":["unsupported"]}`
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%q},"finish_reason":"stop"}]}`, content)
	}))
	defer server.Close()

	var mu sync.Mutex
	var got []string
	agent, err := New(Config{
		APIKey:        "sk-test",
		BaseURL:       server.URL,
		Model:         "test-model",
		NoProjectFile: true,
		MemoryDir:     t.TempDir(),
		Verify:        &loop.VerifyConfig{Enabled: true, Mode: loop.VerifyModeStrict},
		AnswerEventHandler: func(ev loop.AnswerEvent) {
			mu.Lock()
			got = append(got, ev.Type+":"+ev.Verdict)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer agent.Close()

	answer, err := agent.Run(context.Background(), "explain")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(answer, loop.VerifyFailedMarker) {
		t.Fatalf("answer = %q", answer)
	}
	if agent.VerifyOutcome() != loop.VerifyOutcomeFail {
		t.Fatalf("VerifyOutcome = %q, want fail", agent.VerifyOutcome())
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(got) != "[verification_started: verification_completed:fail]" {
		t.Fatalf("answer events = %v", got)
	}
}
