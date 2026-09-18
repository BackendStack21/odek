package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/resource"
	"github.com/BackendStack21/odek/internal/session"
)

func TestHandlePrompt_ReferencedSessionRequiresItsToken(t *testing.T) {
	store := newTestSessionStore(t)
	foreign, err := store.Create([]session.Message{{Role: "user", Content: "secret"}}, "test", "foreign")
	if err != nil {
		t.Fatal(err)
	}
	foreign.AuthToken = "foreign-token"
	if err := store.Save(foreign); err != nil {
		t.Fatal(err)
	}
	caller, err := store.Create(nil, "test", "caller")
	if err != nil {
		t.Fatal(err)
	}
	resources := resource.NewRegistry(resource.NewSessionResolver(store.Dir()))
	var frames []map[string]any
	msg := wsClientMsg{Type: "prompt", SessionID: caller.ID, Content: "@sess:" + foreign.ID, ReferenceTokens: map[string]string{foreign.ID: "wrong"}}
	got := handlePrompt(context.Background(), func(m map[string]any) { frames = append(frames, m) }, store, resources, loadJSONMockResolved(), nil, nil, nil, msg, new(int), new(int), nil, nil, nil, nil)
	if got != nil {
		t.Fatalf("unauthorized reference returned session: %#v", got)
	}
	if len(frames) != 1 || !strings.Contains(frames[0]["message"].(string), "invalid token") {
		t.Fatalf("frames=%v", frames)
	}
}

func TestHandlePrompt_ReferencedSessionIsSanitizedBeforeLLM(t *testing.T) {
	var calls atomic.Int32
	var received atomic.Value
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		received.Store(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer llm.Close()
	store := newTestSessionStore(t)
	foreign, err := store.Create([]session.Message{{Role: "user", Content: "visible transcript"}}, "test", "foreign")
	if err != nil {
		t.Fatal(err)
	}
	foreign.AuthToken = "foreign-token"
	if err := store.Save(foreign); err != nil {
		t.Fatal(err)
	}
	caller, err := store.Create(nil, "test", "caller")
	if err != nil {
		t.Fatal(err)
	}
	resolved := loadJSONMockResolved()
	resolved.BaseURL, resolved.APIKey, resolved.Provider = llm.URL, "test-key", "deepseek"
	resources := resource.NewRegistry(resource.NewSessionResolver(store.Dir()))
	a, err := odek.New(odek.Config{Provider: "deepseek", BaseURL: llm.URL, APIKey: "test-key", Model: "test-model", SystemMessage: "Help", NoProjectFile: true, MemoryDir: t.TempDir(), MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	msg := wsClientMsg{Type: "prompt", SessionID: caller.ID, AuthToken: caller.AuthToken, Content: "read @sess:" + foreign.ID, ReferenceTokens: map[string]string{foreign.ID: foreign.AuthToken}}
	handlePrompt(context.Background(), func(map[string]any) {}, store, resources, resolved, a, nil, nil, msg, new(int), new(int), nil, nil, nil, nil)
	if calls.Load() == 0 {
		t.Fatal("authorized reference did not reach LLM")
	}
	request, _ := received.Load().(string)
	if !strings.Contains(request, "visible transcript") {
		t.Fatal("authorized transcript missing from provider request")
	}
	if strings.Contains(request, foreign.AuthToken) || strings.Contains(request, "auth_token") {
		t.Fatal("session capability leaked into provider request")
	}
}

func TestPromptRequestReferenceTokensJSON(t *testing.T) {
	var req promptRequest
	if err := json.Unmarshal([]byte(`{"content":"@sess:x","reference_tokens":{"x":"tok"}}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.ReferenceTokens["x"] != "tok" {
		t.Fatalf("reference tokens not forwarded: %#v", req.ReferenceTokens)
	}
}
