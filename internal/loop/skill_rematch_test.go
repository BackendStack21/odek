package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

func TestEngine_SkillRematch_OnPlanCreateTitlesOnly(t *testing.T) {
	var queries []string
	var ingested []string
	var sawWrapped bool

	skillLoader := func(q string) string {
		queries = append(queries, q)
		if strings.Contains(q, "Docker build pipeline") {
			return "LAZY-SKILL-BODY"
		}
		return ""
	}

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			fmt.Fprint(w, planTC("c1", `{"verb":"create","steps":[{"id":"s1","title":"Docker build pipeline","note":"DO-NOT-REMATCH-NOTE"}]}`))
			return
		}
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, msg := range body.Messages {
			if strings.Contains(msg.Content, "WRAPPED:skill:LAZY-SKILL-BODY") {
				sawWrapped = true
			}
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer server.Close()

	store := NewPlanStore(12, 2000)
	client := testChatClient(t, server.URL)
	engine := New(client, tool.NewRegistry([]tool.Tool{NewPlanTool(store)}), 10, "sys", nil, 0)
	engine.SetPlanStore(store)
	engine.SetSkillLoader(skillLoader)
	engine.SetUntrustedWrapper(func(source, content string) string {
		return "WRAPPED:" + source + ":" + content
	})

	ctx := WithIngestRecorder(context.Background(), func(source, content string) {
		ingested = append(ingested, source+"|"+content)
	})
	if _, _, err := engine.RunWithMessages(ctx, []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "ship the service"},
	}); err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}

	if len(queries) < 2 {
		t.Fatalf("skillLoader queries = %v, want user message then rematch titles", queries)
	}
	if queries[0] != "ship the service" {
		t.Errorf("first query = %q, want the user message", queries[0])
	}
	if !strings.Contains(queries[1], "Docker build pipeline") {
		t.Errorf("rematch query %q missing step title", queries[1])
	}
	if strings.Contains(queries[1], "DO-NOT-REMATCH-NOTE") {
		t.Errorf("rematch query leaked a step note: %q", queries[1])
	}
	if !sawWrapped {
		t.Error("rematch skill body was not passed through the untrusted wrapper")
	}
	foundIngest := false
	for _, row := range ingested {
		if strings.Contains(row, "skill|LAZY-SKILL-BODY") {
			foundIngest = true
		}
	}
	if !foundIngest {
		t.Errorf("rematch did not record an ingest, got %v", ingested)
	}
}

func TestEngine_SkillRematch_UpsertsExistingSlot(t *testing.T) {
	skillLoader := func(q string) string {
		return "LAZY-SKILL-BODY"
	}

	callCount := 0
	var secondCopies int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			fmt.Fprint(w, planTC("c1", `{"verb":"create","steps":[{"id":"s1","title":"Docker build pipeline"}]}`))
			return
		}
		if callCount == 2 {
			var body struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, msg := range body.Messages {
				secondCopies += strings.Count(msg.Content, "WRAPPED:skill:LAZY-SKILL-BODY")
			}
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer server.Close()

	store := NewPlanStore(12, 2000)
	client := testChatClient(t, server.URL)
	engine := New(client, tool.NewRegistry([]tool.Tool{NewPlanTool(store)}), 10, "sys", nil, 0)
	engine.SetPlanStore(store)
	engine.SetSkillLoader(skillLoader)
	engine.SetUntrustedWrapper(func(source, content string) string {
		return "WRAPPED:" + source + ":" + content
	})

	if _, _, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "ship the service"},
	}); err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}
	if secondCopies != 1 {
		t.Fatalf("skill copies on the think-after-plan call = %d, want 1 (rematch upserts)", secondCopies)
	}
}

func TestEngine_SkillRematch_NotOnPlanUpdate(t *testing.T) {
	var queries []string
	skillLoader := func(q string) string {
		queries = append(queries, q)
		return ""
	}

	store := NewPlanStore(12, 2000)
	if _, err := store.Execute(`{"verb":"create","steps":[{"id":"s1","title":"Already planned"}]}`); err != nil {
		t.Fatal(err)
	}

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			fmt.Fprint(w, planTC("c1", `{"verb":"update","updates":[{"id":"s1","status":"in_progress"}]}`))
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer server.Close()

	client := testChatClient(t, server.URL)
	engine := New(client, tool.NewRegistry([]tool.Tool{NewPlanTool(store)}), 10, "sys", nil, 0)
	engine.SetPlanStore(store)
	engine.SetSkillLoader(skillLoader)

	if _, _, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "keep going"},
	}); err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}
	if len(queries) != 1 || queries[0] != "keep going" {
		t.Errorf("skillLoader queries = %v, want only the user message (no rematch on update)", queries)
	}
}

func TestEngine_EpisodeQueryIncludesPlanTitles(t *testing.T) {
	var queries []string
	episodeCtx := func(q string) string {
		queries = append(queries, q)
		return ""
	}

	store := NewPlanStore(12, 2000)
	planMsg := renderedPlanMessage(t, `{"verb":"create","steps":[{"id":"s1","title":"Migrate postgres","note":"SECRET-NOTE-TEXT"}]}`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer server.Close()

	client := testChatClient(t, server.URL)
	engine := New(client, tool.NewRegistry(nil), 10, "sys", nil, 0)
	engine.SetPlanStore(store)
	engine.SetEpisodeContextFunc(episodeCtx)

	if _, _, err := engine.RunWithMessages(context.Background(), []session.Message{
		{Role: "system", Content: "sys"},
		planMsg,
		{Role: "user", Content: "continue the work"},
	}); err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}
	if len(queries) != 1 {
		t.Fatalf("episode queries = %v, want one", queries)
	}
	if !strings.Contains(queries[0], "Migrate postgres") {
		t.Errorf("episode query missing plan title: %q", queries[0])
	}
	if !strings.Contains(queries[0], "continue the work") {
		t.Errorf("episode query missing user message: %q", queries[0])
	}
	if strings.Contains(queries[0], "SECRET-NOTE-TEXT") {
		t.Errorf("episode query leaked a step note: %q", queries[0])
	}
}
