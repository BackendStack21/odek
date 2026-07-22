package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/memory"
	"github.com/BackendStack21/odek/internal/memory/extended"
)

// ── Item 1: follow-up suggestions ─────────────────────────────────────

func TestFormatFollowUpSuggestions(t *testing.T) {
	if got := formatFollowUpSuggestions(nil); got != "" {
		t.Errorf("nil suggestions: got %q, want empty", got)
	}
	if got := formatFollowUpSuggestions([]string{}); got != "" {
		t.Errorf("empty suggestions: got %q, want empty", got)
	}

	got := formatFollowUpSuggestions([]string{"ship the release", "write the changelog"})
	want := "── You might also want to ──\n• ship the release\n• write the changelog\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// Capped at maxFollowUpSuggestions.
	many := []string{"one", "two", "three", "four", "five"}
	got = formatFollowUpSuggestions(many)
	if strings.Contains(got, "four") || strings.Contains(got, "five") {
		t.Errorf("expected cap at %d suggestions, got %q", maxFollowUpSuggestions, got)
	}
	if strings.Count(got, "• ") != maxFollowUpSuggestions {
		t.Errorf("expected %d bullet lines, got %q", maxFollowUpSuggestions, got)
	}
}

// fakeSuggester implements followUpSuggester for tests.
type fakeSuggester struct {
	suggestions []string
	called      bool
}

func (f *fakeSuggester) FollowUpSuggestions() []string {
	f.called = true
	return f.suggestions
}

func TestPrintFollowUpSuggestions(t *testing.T) {
	sugs := []string{"try X", "try Y"}

	t.Run("printed in engaging mode", func(t *testing.T) {
		var buf bytes.Buffer
		printFollowUpSuggestions(&buf, &fakeSuggester{suggestions: sugs}, "engaging")
		if !strings.Contains(buf.String(), "── You might also want to ──") ||
			!strings.Contains(buf.String(), "• try X") {
			t.Errorf("expected suggestion block, got %q", buf.String())
		}
	})

	t.Run("printed in enhance mode", func(t *testing.T) {
		var buf bytes.Buffer
		printFollowUpSuggestions(&buf, &fakeSuggester{suggestions: sugs}, "enhance")
		if buf.Len() == 0 {
			t.Error("expected suggestion block in enhance mode")
		}
	})

	t.Run("suppressed in off mode", func(t *testing.T) {
		var buf bytes.Buffer
		fs := &fakeSuggester{suggestions: sugs}
		printFollowUpSuggestions(&buf, fs, "off")
		if buf.Len() != 0 {
			t.Errorf("expected no output in off mode, got %q", buf.String())
		}
		if fs.called {
			t.Error("FollowUpSuggestions should not be called in off mode")
		}
	})

	t.Run("suppressed in verbose mode", func(t *testing.T) {
		var buf bytes.Buffer
		printFollowUpSuggestions(&buf, &fakeSuggester{suggestions: sugs}, "verbose")
		if buf.Len() != 0 {
			t.Errorf("expected no output in verbose mode, got %q", buf.String())
		}
	})

	t.Run("absent when no suggestions", func(t *testing.T) {
		var buf bytes.Buffer
		printFollowUpSuggestions(&buf, &fakeSuggester{}, "engaging")
		if buf.Len() != 0 {
			t.Errorf("expected no output with empty suggestions, got %q", buf.String())
		}
	})

	t.Run("nil manager is a no-op", func(t *testing.T) {
		var buf bytes.Buffer
		printFollowUpSuggestions(&buf, nil, "engaging")
		if buf.Len() != 0 {
			t.Errorf("expected no output for nil manager, got %q", buf.String())
		}
	})
}

// TestPrintFollowUpSuggestions_NoExtendedMemory: a real MemoryManager without
// Extended Memory yields no suggestions and prints nothing.
func TestPrintFollowUpSuggestions_NoExtendedMemory(t *testing.T) {
	mm := memory.NewMemoryManager(t.TempDir(), nil, memory.DefaultMemoryConfig())
	var buf bytes.Buffer
	printFollowUpSuggestions(&buf, mm, "engaging")
	if buf.Len() != 0 {
		t.Errorf("expected no output without extended memory, got %q", buf.String())
	}
}

// ── Item 2: return-after-break injection ──────────────────────────────

// simpleLLMServer returns a mock OpenAI-compatible endpoint whose every
// chat completion responds with the given content.
func simpleLLMServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, content)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newExtendedBackedManager seeds one trusted atom in a temp extended store,
// then returns a MemoryManager whose Extended Memory is backed by llmSrv.
func newExtendedBackedManager(t *testing.T, llmSrv *httptest.Server) *memory.MemoryManager {
	t.Helper()
	dir := t.TempDir()
	extCfg := extended.DefaultConfig()
	enabled := true
	extCfg.Enabled = &enabled

	// Seed an atom via a throwaway ExtendedMemory (nil LLM suffices for writes).
	seeder := extended.New(filepath.Join(dir, "extended"), nil, extCfg)
	if err := seeder.AddAtom(context.Background(), extended.MemoryAtom{
		Text:        "Review auth refactor",
		SourceClass: extended.SourceUserSaid,
		Type:        extended.TypeFact,
	}); err != nil {
		t.Fatalf("seed atom: %v", err)
	}
	if err := seeder.Close(); err != nil {
		t.Fatalf("close seeder: %v", err)
	}

	cfg := memory.DefaultMemoryConfig()
	cfg.Extended = &extCfg
	mm := memory.NewMemoryManager(dir, nil, cfg)
	mm.InitExtended(llm.New(llmSrv.URL, "sk-mock", "mock-model", "", 0, 30*time.Second), dir)
	return mm
}

func TestInjectReturnAfterBreak_NilManager(t *testing.T) {
	msgs := []llm.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"}}
	out := injectReturnAfterBreak(context.Background(), nil, msgs)
	if len(out) != len(msgs) {
		t.Errorf("nil manager should leave messages unchanged, got %d messages", len(out))
	}
}

func TestInjectReturnAfterBreak_ExtendedDisabled(t *testing.T) {
	mm := memory.NewMemoryManager(t.TempDir(), nil, memory.DefaultMemoryConfig())
	msgs := []llm.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"}}
	out := injectReturnAfterBreak(context.Background(), mm, msgs)
	if len(out) != len(msgs) {
		t.Errorf("disabled extended memory should leave messages unchanged, got %d messages", len(out))
	}
}

func TestInjectReturnAfterBreak_InsertsAfterLastSystem(t *testing.T) {
	srv := simpleLLMServer(t, "You were reviewing the auth refactor.")
	mm := newExtendedBackedManager(t, srv)

	msgs := []llm.Message{
		{Role: "system", Content: "identity"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "answer"},
	}
	out := injectReturnAfterBreak(context.Background(), mm, msgs)
	if len(out) != len(msgs)+1 {
		t.Fatalf("expected %d messages, got %d", len(msgs)+1, len(out))
	}
	rb := out[1]
	if rb.Role != "system" {
		t.Errorf("injected message role = %q, want system", rb.Role)
	}
	if !strings.Contains(rb.Content, `source="return_after_break"`) {
		t.Errorf("injected message should be wrapped as untrusted return_after_break, got %q", rb.Content)
	}
	if !strings.Contains(rb.Content, "auth refactor") {
		t.Errorf("injected message should contain the summary, got %q", rb.Content)
	}
	// Original order otherwise preserved.
	if out[0].Content != "identity" || out[2].Content != "first" || out[3].Content != "answer" {
		t.Errorf("message order not preserved: %+v", out)
	}
}

func TestInjectReturnAfterBreak_NoSystemMessagePrepends(t *testing.T) {
	srv := simpleLLMServer(t, "You were reviewing the auth refactor.")
	mm := newExtendedBackedManager(t, srv)

	msgs := []llm.Message{{Role: "user", Content: "first"}}
	out := injectReturnAfterBreak(context.Background(), mm, msgs)
	if len(out) != 2 || out[0].Role != "system" {
		t.Fatalf("expected injected system message at index 0, got %+v", out)
	}
}

// ── Item 4: telegram nudge push ───────────────────────────────────────

func TestProactiveNudgesEnabled(t *testing.T) {
	tru := true
	fls := false
	cases := []struct {
		name string
		cfg  memory.MemoryConfig
		want bool
	}{
		{"no extended config", memory.MemoryConfig{}, false},
		{"extended without flag", memory.MemoryConfig{Extended: &extended.Config{}}, false},
		{"opted in", memory.MemoryConfig{Extended: &extended.Config{ProactiveNudgesEnabled: &tru}}, true},
		{"explicitly off", memory.MemoryConfig{Extended: &extended.Config{ProactiveNudgesEnabled: &fls}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := proactiveNudgesEnabled(tc.cfg); got != tc.want {
				t.Errorf("proactiveNudgesEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// fakeNudgeMemory implements nudgeMemory, running background work inline.
type fakeNudgeMemory struct {
	nudges []extended.Nudge
	err    error
}

func (f *fakeNudgeMemory) RunBackground(fn func()) { fn() }

func (f *fakeNudgeMemory) TakeNudges(_ context.Context, maxN int) ([]extended.Nudge, error) {
	if f.err != nil {
		return nil, f.err
	}
	if maxN < len(f.nudges) {
		return f.nudges[:maxN], nil
	}
	return f.nudges, nil
}

func TestPushTelegramNudge(t *testing.T) {
	t.Run("nudge present fires push", func(t *testing.T) {
		var sent []string
		mm := &fakeNudgeMemory{nudges: []extended.Nudge{
			{Text: "Your goal 'ship v2' has been quiet for a while.", Kind: extended.NudgeKindStaleGoal},
			{Text: "second nudge", Kind: extended.NudgeKindOpenQuestion},
		}}
		pushTelegramNudge(mm, func(text string) { sent = append(sent, text) })
		if len(sent) != 1 {
			t.Fatalf("expected exactly one nudge push, got %v", sent)
		}
		if !strings.HasPrefix(sent[0], "💡 ") || !strings.Contains(sent[0], "ship v2") {
			t.Errorf("unexpected nudge message %q", sent[0])
		}
	})

	t.Run("no nudges stays silent", func(t *testing.T) {
		sent := 0
		pushTelegramNudge(&fakeNudgeMemory{}, func(string) { sent++ })
		if sent != 0 {
			t.Errorf("expected no push without nudges, got %d", sent)
		}
	})

	t.Run("error stays silent", func(t *testing.T) {
		sent := 0
		pushTelegramNudge(&fakeNudgeMemory{err: errors.New("boom")}, func(string) { sent++ })
		if sent != 0 {
			t.Errorf("expected no push on error, got %d", sent)
		}
	})

	t.Run("blank text stays silent", func(t *testing.T) {
		sent := 0
		pushTelegramNudge(&fakeNudgeMemory{nudges: []extended.Nudge{{Text: "  ", Kind: extended.NudgeKindDrift}}},
			func(string) { sent++ })
		if sent != 0 {
			t.Errorf("expected no push for blank nudge text, got %d", sent)
		}
	})
}
