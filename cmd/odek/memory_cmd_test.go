package main

import (
	"context"
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

// TestMemoryCmd_ListAndPromote exercises the human-gated promote path end to
// end through the CLI command: a seeded untrusted episode is pending, the
// command promotes it, and the approval is persisted to the on-disk index.
func TestMemoryCmd_ListAndPromote(t *testing.T) {
	home := setupTestHome(t)
	dir := filepath.Join(home, ".odek", "memory")

	es := memory.NewEpisodeStore(dir, nil)
	if err := es.WriteWithProvenance("20260108-web", "researched a library", 5,
		memory.EpisodeProvenance{Untrusted: true, Sources: []string{"browser"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := memoryCmd([]string{"list"}); err != nil {
		t.Fatalf("memory list: %v", err)
	}
	if err := memoryCmd([]string{"promote", "20260108-web"}); err != nil {
		t.Fatalf("memory promote: %v", err)
	}

	fresh := memory.NewEpisodeStore(dir, nil)
	idx, err := fresh.ReadIndex()
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(idx) != 1 || !idx[0].Provenance.UserApproved {
		t.Errorf("episode not approved after promote: %+v", idx)
	}

	if err := memoryCmd([]string{"promote", "does-not-exist"}); err == nil {
		t.Error("promoting an unknown id should error")
	}
	if err := memoryCmd([]string{"bogus"}); err == nil {
		t.Error("unknown subcommand should error")
	}
}

// TestMemoryCmd_ListEmpty: list on a clean home must not error.
func TestMemoryCmd_ListEmpty(t *testing.T) {
	setupTestHome(t)
	if err := memoryCmd([]string{"list"}); err != nil {
		t.Fatalf("memory list on empty home: %v", err)
	}
}

// ── Extended Memory: nudges preview ─────────────────────────────────

// unsetAPIKeys clears LLM API key env vars for the duration of a test.
func unsetAPIKeys(t *testing.T) {
	t.Helper()
	for _, k := range []string{"DEEPSEEK_API_KEY", "OPENAI_API_KEY", "ODEK_API_KEY"} {
		orig, ok := os.LookupEnv(k)
		os.Unsetenv(k)
		if ok {
			t.Cleanup(func() { os.Setenv(k, orig) })
		}
	}
}

// TestMemoryCmd_ExtendedNudges_NoBackend: nudges needs an LLM backend.
func TestMemoryCmd_ExtendedNudges_NoBackend(t *testing.T) {
	setupTestHome(t)
	unsetAPIKeys(t)
	if err := memoryCmd([]string{"extended", "nudges"}); err == nil {
		t.Fatal("nudges without an API key should error")
	}
}

// TestMemoryCmd_ExtendedNudges_Empty: a fresh store prints the empty message.
func TestMemoryCmd_ExtendedNudges_Empty(t *testing.T) {
	home := setupTestHome(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"[]"}}]}`)
	}))
	defer srv.Close()

	os.MkdirAll(filepath.Join(home, ".odek"), 0700)
	cfgJSON := fmt.Sprintf(`{"base_url": %q, "api_key": "sk-mock", "model": "mock-model"}`, srv.URL)
	if err := os.WriteFile(filepath.Join(home, ".odek", "config.json"), []byte(cfgJSON), 0600); err != nil {
		t.Fatal(err)
	}
	// Env outranks config.json and shields the test from env leftovers of
	// earlier tests in the package.
	t.Setenv("ODEK_BASE_URL", srv.URL)
	t.Setenv("DEEPSEEK_API_KEY", "sk-mock")

	out := captureStdout(func() {
		if err := memoryCmd([]string{"extended", "nudges"}); err != nil {
			t.Errorf("nudges on empty store: %v", err)
		}
	})
	if !strings.Contains(out, "No nudges right now.") {
		t.Errorf("expected empty-nudges message, got %q", out)
	}
}

// TestMemoryCmd_ExtendedNudges_PrintsNudges: with a stale goal in the store
// and an LLM that synthesizes a nudge, the preview prints kind, text, and
// source atom IDs — and notes that the daily cap is not consumed.
func TestMemoryCmd_ExtendedNudges_PrintsNudges(t *testing.T) {
	home := setupTestHome(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"[{\"text\":\"Your goal 'ship v2' went quiet.\",\"kind\":\"stale_goal\",\"source_atom_ids\":[\"atom-1\"]}]"}}]}`)
	}))
	defer srv.Close()

	os.MkdirAll(filepath.Join(home, ".odek"), 0700)
	cfgJSON := fmt.Sprintf(`{"base_url": %q, "api_key": "sk-mock", "model": "mock-model"}`, srv.URL)
	if err := os.WriteFile(filepath.Join(home, ".odek", "config.json"), []byte(cfgJSON), 0600); err != nil {
		t.Fatal(err)
	}
	// Env outranks config.json and shields the test from env leftovers of
	// earlier tests in the package.
	t.Setenv("ODEK_BASE_URL", srv.URL)
	t.Setenv("DEEPSEEK_API_KEY", "sk-mock")

	// Seed a stale goal atom (old enough to clear the stale-goal threshold).
	extDir := filepath.Join(home, ".odek", "memory", "extended")
	extCfg := extended.DefaultConfig()
	enabled := true
	extCfg.Enabled = &enabled
	seeder := extended.New(extDir, nil, extCfg)
	if err := seeder.AddAtom(context.Background(), extended.MemoryAtom{
		Text:        "Ship v2 of the API",
		SourceClass: extended.SourceUserSaid,
		Type:        extended.TypeGoal,
		CreatedAt:   time.Now().Add(-30 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("seed atom: %v", err)
	}
	if err := seeder.Close(); err != nil {
		t.Fatalf("close seeder: %v", err)
	}

	out := captureStdout(func() {
		if err := memoryCmd([]string{"extended", "nudges"}); err != nil {
			t.Errorf("nudges: %v", err)
		}
	})
	if !strings.Contains(out, "• [stale_goal] Your goal 'ship v2' went quiet.") {
		t.Errorf("expected nudge line, got %q", out)
	}
	if !strings.Contains(out, "atoms: atom-1") {
		t.Errorf("expected source atom IDs, got %q", out)
	}
	if !strings.Contains(out, "does not consume the daily cap") {
		t.Errorf("expected preview note, got %q", out)
	}
}
