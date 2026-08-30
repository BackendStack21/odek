package memory

// RED-first tests for the memory auto-eviction workstream (M1–M4):
//
//	M1 — cap errors carry a decision-ready entry index + eviction guidance
//	M2 — `stats` action: per-entry sizes/fill without a raw read
//	M3 — tool description codifies the LLM eviction policy
//	M4 — system-prompt memory block warns when a fact file is ≥90% full
//
// The eviction judgment itself stays in the agent's ReAct loop: every
// eviction remains an explicit, auditable memory remove/replace call.

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func evictionCfg() MemoryConfig {
	cfg := DefaultMemoryConfig()
	cfg.FactsLimitUser = 200
	cfg.FactsLimitEnv = 100
	return cfg
}

// ── M1: cap errors list entries + guidance ──────────────────────────

func TestAutoEviction_CapErrorListsEntries(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, evictionCfg())
	if err := mm.facts.Add("env", strings.Repeat("a", 40)); err != nil {
		t.Fatalf("seed entry 1: %v", err)
	}
	if err := mm.facts.Add("env", strings.Repeat("b", 45)); err != nil {
		t.Fatalf("seed entry 2: %v", err)
	}

	err := mm.facts.Add("env", strings.Repeat("c", 20))
	if err == nil {
		t.Fatal("expected cap error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"would exceed cap (100 chars)",
		"current: 89, max: 100",
		"[1] 40c",
		"[2] 45c",
		"memory remove/replace",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("cap error missing %q:\n%s", want, msg)
		}
	}
}

func TestAutoEviction_CapErrorEmptyFile(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, evictionCfg())
	err := mm.facts.Add("env", strings.Repeat("x", 150))
	if err == nil {
		t.Fatal("expected cap error for oversized single entry, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "would exceed cap (100 chars)") {
		t.Errorf("missing cap report:\n%s", msg)
	}
	if !strings.Contains(msg, "empty") {
		t.Errorf("empty-file case should say the file has no entries:\n%s", msg)
	}
}

func TestAutoEviction_ReplaceCapErrorListsEntries(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, evictionCfg())
	if err := mm.facts.Add("env", strings.Repeat("a", 40)); err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	err := mm.facts.Replace("env", "aaa", strings.Repeat("z", 101))
	if err == nil {
		t.Fatal("expected replace cap error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "[1] 40c") {
		t.Errorf("replace cap error should list entries:\n%s", msg)
	}
	if !strings.Contains(msg, "memory remove") {
		t.Errorf("replace cap error should carry eviction guidance:\n%s", msg)
	}
}

// ── M2: stats action ────────────────────────────────────────────────

func TestMemoryStatsAction(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, evictionCfg())
	if err := mm.facts.Add("env", strings.Repeat("a", 40)); err != nil {
		t.Fatalf("seed entry 1: %v", err)
	}
	if err := mm.facts.Add("env", strings.Repeat("b", 45)); err != nil {
		t.Fatalf("seed entry 2: %v", err)
	}
	tool := NewMemoryTool(mm)

	res, _ := tool.Call(`{"action":"stats","target":"env"}`)
	var out struct {
		Success bool   `json:"success"`
		Target  string `json:"target"`
		Used    int    `json:"used"`
		Cap     int    `json:"cap"`
		Entries []struct {
			Index   int    `json:"index"`
			Chars   int    `json:"chars"`
			Preview string `json:"preview"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(res), &out); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if !out.Success {
		t.Fatalf("expected success, got %s", res)
	}
	if out.Target != "env" {
		t.Errorf("target = %q, want env", out.Target)
	}
	if out.Used != 89 {
		t.Errorf("used = %d, want 89 (40 + 4-byte separator + 45)", out.Used)
	}
	if out.Cap != 100 {
		t.Errorf("cap = %d, want 100", out.Cap)
	}
	if len(out.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(out.Entries))
	}
	if out.Entries[0].Chars != 40 || out.Entries[1].Chars != 45 {
		t.Errorf("entry chars = %d/%d, want 40/45", out.Entries[0].Chars, out.Entries[1].Chars)
	}
	if out.Entries[0].Index != 1 || out.Entries[1].Index != 2 {
		t.Errorf("entry indexes = %d/%d, want 1/2", out.Entries[0].Index, out.Entries[1].Index)
	}
	if !strings.HasPrefix(out.Entries[0].Preview, "aaaa") {
		t.Errorf("preview = %q, want aaa... prefix", out.Entries[0].Preview)
	}
}

func TestMemoryStatsActionMissingFile(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, evictionCfg())
	tool := NewMemoryTool(mm)
	res, _ := tool.Call(`{"action":"stats","target":"env"}`)
	var out struct {
		Success bool `json:"success"`
		Used    int  `json:"used"`
		Cap     int  `json:"cap"`
		Entries []struct {
			Index int `json:"index"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(res), &out); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if !out.Success {
		t.Fatalf("stats on a not-yet-created file must succeed with zero fill, got %s", res)
	}
	if out.Used != 0 || len(out.Entries) != 0 {
		t.Errorf("expected empty stats, got used=%d entries=%d", out.Used, len(out.Entries))
	}
	if out.Cap != 100 {
		t.Errorf("cap = %d, want 100", out.Cap)
	}
}

func TestMemoryStatsActionGuards(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, evictionCfg())
	tool := NewMemoryTool(mm)

	res, _ := tool.Call(`{"action":"stats","target":"episodes"}`)
	if !strings.Contains(res, "must be 'user' or 'env'") {
		t.Errorf("episodes target must be rejected (view is the episodes surface), got %s", res)
	}
	res, _ = tool.Call(`{"action":"stats"}`)
	if !strings.Contains(res, "target is required") {
		t.Errorf("missing target must be rejected, got %s", res)
	}
}

func TestMemoryStatsPreviewRuneSafe(t *testing.T) {
	cfg := evictionCfg()
	cfg.FactsLimitEnv = 400 // room for one 60-rune multibyte entry (180 bytes)
	mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, cfg)
	entry := strings.Repeat("—", 60)
	if err := mm.facts.Add("env", entry); err != nil {
		t.Fatalf("seed multibyte entry: %v", err)
	}
	tool := NewMemoryTool(mm)
	res, _ := tool.Call(`{"action":"stats","target":"env"}`)
	var out struct {
		Entries []struct {
			Chars   int    `json:"chars"`
			Preview string `json:"preview"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(res), &out); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if len(out.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(out.Entries))
	}
	p := out.Entries[0].Preview
	if !utf8.ValidString(p) {
		t.Errorf("preview is not valid UTF-8 (mid-rune cut): %q", p)
	}
	if n := len([]rune(p)); n > 61 { // 60 + ellipsis
		t.Errorf("preview = %d runes, want ≤61", n)
	}
}

// ── M3: eviction policy in the tool description ─────────────────────

func TestMemoryToolDescriptionEvictionPolicy(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, evictionCfg())
	desc := NewMemoryTool(mm).Description()
	for _, want := range []string{"at cap", "evict", "stats"} {
		if !strings.Contains(desc, want) {
			t.Errorf("tool description missing eviction-policy term %q: %s", want, desc)
		}
	}
}

// ── M4: near-cap hint in the system-prompt memory block ─────────────

func TestPromptNearCapHint(t *testing.T) {
	t.Run("env at 95 percent gets a hint", func(t *testing.T) {
		mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, evictionCfg())
		if err := mm.facts.Add("env", strings.Repeat("e", 95)); err != nil {
			t.Fatalf("seed: %v", err)
		}
		block := mm.BuildSystemPrompt()
		if !strings.Contains(block, "env fact file 95% full") {
			t.Errorf("missing near-cap hint for env:\n%s", block)
		}
		if !strings.Contains(block, "memory remove") {
			t.Errorf("hint must point at the eviction action:\n%s", block)
		}
	})

	t.Run("env well below cap stays clean", func(t *testing.T) {
		mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, evictionCfg())
		if err := mm.facts.Add("env", strings.Repeat("e", 50)); err != nil {
			t.Fatalf("seed: %v", err)
		}
		block := mm.BuildSystemPrompt()
		if strings.Contains(block, "full — evict") {
			t.Errorf("hint must not appear below the near-cap threshold:\n%s", block)
		}
	})

	t.Run("user file at 95 percent gets a hint", func(t *testing.T) {
		mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, evictionCfg())
		if err := mm.facts.Add("user", strings.Repeat("u", 190)); err != nil {
			t.Fatalf("seed: %v", err)
		}
		block := mm.BuildSystemPrompt()
		if !strings.Contains(block, "user fact file 95% full") {
			t.Errorf("missing near-cap hint for user:\n%s", block)
		}
	})
}
