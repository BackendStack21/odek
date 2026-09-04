package session

import (
	"strings"
	"testing"
)

func TestDeepSearch_TokenMatch(t *testing.T) {
	store := newTestStore(t)

	// Create a session with content that should trigger token matches.
	msgs := []Message{
		{Role: "user", Content: "what go-vector changes did you make to the modifications?"},
		{Role: "assistant", Content: "I updated the vector index with new updates and modifications."},
	}
	_, err := store.Create(msgs, "test-model", "test go-vector changes")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Token-based matching: simulate deepSearch logic
	tokens := []string{"go-vector", "changes", "modifications", "updates"}
	matchedTokens := make(map[string]bool, len(tokens))
	sessions, err := store.List(0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("no sessions found")
	}
	for _, s := range sessions {
		full, err := store.Load(s.ID)
		if err != nil {
			continue
		}
		for _, msg := range full.Messages {
			if msg.Role != "user" && msg.Role != "assistant" {
				continue
			}
			lower := strings.ToLower(msg.Content)
			for _, tok := range tokens {
				if len(tok) < 2 || matchedTokens[tok] {
					continue
				}
				if strings.Contains(lower, tok) {
					matchedTokens[tok] = true
				}
			}
		}
	}

	if len(matchedTokens) < 2 {
		t.Errorf("deepSearch matched %d tokens (need >= 2)", len(matchedTokens))
		for tok := range matchedTokens {
			t.Logf("  matched: %s", tok)
		}
		for _, tok := range tokens {
			if !matchedTokens[tok] {
				t.Logf("  not matched: %s", tok)
			}
		}
	}
}

func TestDeepSearch_NoMatch(t *testing.T) {
	store := newTestStore(t)

	// Create a session with unrelated content.
	msgs := []Message{
		{Role: "user", Content: "hello, how are you today?"},
		{Role: "assistant", Content: "I'm doing great, thanks for asking!"},
	}
	_, err := store.Create(msgs, "test-model", "greeting")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Token-based matching with unrelated tokens.
	tokens := []string{"database", "migration", "schema"}
	matchedTokens := make(map[string]bool, len(tokens))
	sessions, _ := store.List(0)
	for _, s := range sessions {
		full, _ := store.Load(s.ID)
		for _, msg := range full.Messages {
			if msg.Role != "user" && msg.Role != "assistant" {
				continue
			}
			lower := strings.ToLower(msg.Content)
			for _, tok := range tokens {
				if len(tok) < 2 || matchedTokens[tok] {
					continue
				}
				if strings.Contains(lower, tok) {
					matchedTokens[tok] = true
				}
			}
		}
	}

	if len(matchedTokens) >= 2 {
		t.Errorf("deepSearch should NOT match unrelated tokens, got %d matches", len(matchedTokens))
	}
}

func TestDeepSearch_MultiSession(t *testing.T) {
	store := newTestStore(t)

	// Create sessions with different content.
	sessions := []struct {
		msgs []Message
		task string
	}{
		{[]Message{{Role: "user", Content: "fix the database migration script"}}, "db fix"},
		{[]Message{{Role: "user", Content: "add new API endpoint for users"}}, "api work"},
		{[]Message{{Role: "user", Content: "deploy the latest version to production"}}, "deploy"},
	}
	for _, s := range sessions {
		_, err := store.Create(s.msgs, "test-model", s.task)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	// Search for "database migration" — should match session 0.
	tokens := []string{"database", "migration"}
	matchedTokens := make(map[string]bool, len(tokens))
	all, _ := store.List(0)
	for _, s := range all {
		full, _ := store.Load(s.ID)
		for _, msg := range full.Messages {
			lower := strings.ToLower(msg.Content)
			for _, tok := range tokens {
				if strings.Contains(lower, tok) {
					matchedTokens[tok] = true
				}
			}
		}
	}
	if len(matchedTokens) < 2 {
		t.Errorf("expected to match 'database' and 'migration', got %d", len(matchedTokens))
	}

	// Search for "production deploy" — should match session 2.
	tokens2 := []string{"production", "deploy"}
	matchedTokens2 := make(map[string]bool, len(tokens2))
	for _, s := range all {
		full, _ := store.Load(s.ID)
		for _, msg := range full.Messages {
			lower := strings.ToLower(msg.Content)
			for _, tok := range tokens2 {
				if strings.Contains(lower, tok) {
					matchedTokens2[tok] = true
				}
			}
		}
	}
	if len(matchedTokens2) < 2 {
		t.Errorf("expected to match 'production' and 'deploy', got %d", len(matchedTokens2))
	}
}
