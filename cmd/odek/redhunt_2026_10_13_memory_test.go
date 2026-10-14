package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/memory"
	"github.com/BackendStack21/odek/internal/memory/extended"
)

// memhuntMockLLM serves one fixed chat-completion reply whose message content
// is reply (JSON-encoded for the transport).
func memhuntMockLLM(t *testing.T, home, reply string) {
	t.Helper()
	content, _ := json.Marshal(reply)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}]}`, content)
	}))
	t.Cleanup(srv.Close)
	os.MkdirAll(filepath.Join(home, ".odek"), 0700)
	cfgJSON := fmt.Sprintf(`{"base_url": %q, "api_key": "sk-mock", "model": "mock-model"}`, srv.URL)
	if err := os.WriteFile(filepath.Join(home, ".odek", "config.json"), []byte(cfgJSON), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODEK_BASE_URL", srv.URL)
	t.Setenv("DEEPSEEK_API_KEY", "sk-mock")
}

// TestRED_MemoryCmd_NudgesSourceAtomIDsAreEscaped: `odek memory extended
// nudges` prints the LLM-supplied source_atom_ids raw. The kind and text of
// each nudge are escaped (SanitizeInline), but the ids are not, so a model
// reply can emit terminal escape sequences on the operator's review surface —
// the sibling the quarantine/pending escape fixes missed.
func TestRED_MemoryCmd_NudgesSourceAtomIDsAreEscaped(t *testing.T) {
	home := setupTestHome(t)
	reply := `[{"text":"Your goal went quiet.","kind":"stale_goal","source_atom_ids":["atom-1\u001b]0;pwned\u0007\u001b[2K\u001b[1A"]}]`
	memhuntMockLLM(t, home, reply)

	extDir := filepath.Join(home, ".odek", "memory", "extended")
	extCfg := extended.DefaultConfig()
	extCfg.Enabled = memory.BoolPtr(true)
	seeder := extended.New(extDir, nil, extCfg)
	if err := seeder.AddAtom(context.Background(), extended.MemoryAtom{
		Text: "Ship v2 of the API", SourceClass: extended.SourceUserSaid,
		Type: extended.TypeGoal, CreatedAt: time.Now().Add(-30 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("seed atom: %v", err)
	}
	seeder.Close()

	out := captureStdout(func() {
		if err := memoryCmd([]string{"extended", "nudges"}); err != nil {
			t.Errorf("nudges: %v", err)
		}
	})
	if !strings.Contains(out, "atoms:") {
		t.Fatalf("nudge preview missing atoms line: %q", out)
	}
	if strings.ContainsRune(out, '\x1b') || strings.ContainsRune(out, '\a') {
		t.Fatalf("nudges preview printed raw terminal control bytes from LLM-supplied atom ids: %q", out)
	}
}

// TestRED_MemoryCmd_ConsolidateUsesMemoryLLM: `odek memory extended
// consolidate` dials the MAIN model (resolved.Model) for the per-group merge
// calls. The agent runs the same consolidation with the memory LLM
// (extended.ResolveLLM honouring memory.extended.llm: model, thinking,
// max_tokens, timeout), and docs/EXTENDED_MEMORY.md says consolidation "asks
// the memory LLM". An operator who points memory.extended.llm at a cheap
// model still pays the main (possibly thinking) model from the CLI.
func TestRED_MemoryCmd_ConsolidateUsesMemoryLLM(t *testing.T) {
	home := setupTestHome(t)
	var models []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string          `json:"model"`
			Input json.RawMessage `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			// Every text gets the same vector: the seed is one near-duplicate group.
			var inputs []string
			if json.Unmarshal(req.Input, &inputs) != nil {
				inputs = []string{""}
			}
			var data []string
			for i := range inputs {
				data = append(data, fmt.Sprintf(`{"index":%d,"embedding":[1,0,0,0]}`, i))
			}
			fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(data, ","))
			return
		}
		models = append(models, req.Model)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"The repository builds with Go 1.27 and runs go vet in CI."}}]}`)
	}))
	defer srv.Close()
	os.MkdirAll(filepath.Join(home, ".odek"), 0700)
	cfgJSON := fmt.Sprintf(`{"base_url": %q, "api_key": "sk-mock", "model": "main-model",
	  "memory": {"embedding": {"provider": "http", "base_url": %q, "model": "embed-model"},
	    "extended": {"enabled": true, "semantic_dedup_threshold": 0, "llm": {"model": "memory-model"}}}}`, srv.URL, srv.URL)
	if err := os.WriteFile(filepath.Join(home, ".odek", "config.json"), []byte(cfgJSON), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODEK_BASE_URL", srv.URL)
	t.Setenv("DEEPSEEK_API_KEY", "sk-mock")

	extDir := filepath.Join(home, ".odek", "memory", "extended")
	extCfg := extended.DefaultConfig()
	extCfg.Enabled = memory.BoolPtr(true)
	extCfg.SemanticDedupThreshold = new(float32) // keep the pair apart at add time
	seeder := extended.New(extDir, nil, extCfg)
	for _, text := range []string{
		"The repository builds with Go 1.27 and runs go vet in CI",
		"Go vet runs in CI for every repository build",
	} {
		if err := seeder.AddAtom(context.Background(), extended.MemoryAtom{Text: text, Type: extended.TypeFact, SourceClass: extended.SourceUserSaid, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	seeder.Close()

	cout := captureStdout(func() {
		if err := memoryCmd([]string{"extended", "consolidate"}); err != nil {
			t.Errorf("consolidate: %v", err)
		}
	})
	if len(models) == 0 {
		t.Fatalf("consolidate made no LLM call; the near-duplicate seed did not group: %s", cout)
	}
	for _, m := range models {
		if m != "memory-model" {
			t.Fatalf("consolidate called model %q; want the configured memory LLM %q (calls: %v)", m, "memory-model", models)
		}
	}
}

type memhuntSuggester []string

func (s memhuntSuggester) FollowUpSuggestions() []string { return s }

// TestRED_FollowUpSuggestionsEscapedForTerminal: the follow-up block printed
// after a CLI/REPL turn is the Extended Memory predictor's LLM output, which
// is generated from the user message and recent messages — including wrapped
// tool/web content. formatFollowUpSuggestions writes each suggestion raw,
// while every other model- or tool-sourced string the CLI prints goes through
// the renderer's terminal escaping, so a predicted intent carrying OSC/CSI
// sequences retitles the window, erases lines or forges hyperlinks.
func TestRED_FollowUpSuggestionsEscapedForTerminal(t *testing.T) {
	var b strings.Builder
	printFollowUpSuggestions(&b, memhuntSuggester{"Run the tests\x1b[2K\x1b[1A\x1b]8;;https://evil.example\x07click\x1b]8;;\x07"}, "engaging")
	out := b.String()
	if !strings.Contains(out, "Run the tests") {
		t.Fatalf("suggestion block missing: %q", out)
	}
	if strings.ContainsRune(out, '\x1b') || strings.ContainsRune(out, '\a') {
		t.Fatalf("follow-up suggestions printed raw terminal control bytes: %q", out)
	}
}
