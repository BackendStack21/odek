package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// capturingLLM records every SimpleCall and answers with a fixed reply.
type capturingLLM struct {
	mu      sync.Mutex
	systems []string
	users   []string
	reply   string
}

func (c *capturingLLM) SimpleCall(_ context.Context, system, user string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.systems = append(c.systems, system)
	c.users = append(c.users, user)
	return c.reply, nil
}

func (c *capturingLLM) last() (system, user string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.users) == 0 {
		return "", ""
	}
	return c.systems[len(c.systems)-1], c.users[len(c.users)-1]
}

// episodeOnlyConfig: episode extraction on, facts and background
// consolidation off, so session end makes exactly one LLM call.
func episodeOnlyConfig() MemoryConfig {
	cfg := DefaultMemoryConfig()
	cfg.ExtractFacts = boolPtr(false)
	cfg.ConsolidateOnEnd = boolPtr(false)
	return cfg
}

// TestRED_ExtractionTranscriptBoundedAndToolOutputOmitted: the summariser
// must see the conversation, not the raw tool output. A session whose
// history is dominated by one huge tool result (the observed 730 KB case)
// produced a continuation of the chat instead of a summary. The transcript
// handed to the LLM is bounded and tool bodies are replaced by a size note.
func TestRED_ExtractionTranscriptBoundedAndToolOutputOmitted(t *testing.T) {
	dir := t.TempDir()
	llm := &capturingLLM{reply: "The session fixed the parser and added tests."}
	mm := NewMemoryManager(dir, llm, episodeOnlyConfig())

	payload := strings.Repeat("TOOLPAYLOAD ", 60_000) // ~720 KB
	msgs := []string{
		"user: fix the parser bug in lexer.go",
		"assistant: reading the file",
		"tool: " + payload,
		"assistant: found it; patching",
		"user: run the tests",
		"assistant: all 12 tests pass; the fix is committed",
	}
	mm.OnSessionEndWithProvenance("20260101-bounded", 3, msgs, EpisodeProvenance{})
	_, user := llm.last()
	if user == "" {
		t.Fatal("extraction LLM never called")
	}
	if len(user) > maxExtractionTranscriptBytes+1024 {
		t.Fatalf("transcript %d bytes handed to the summariser; want ≤ %d", len(user), maxExtractionTranscriptBytes)
	}
	if strings.Contains(user, "TOOLPAYLOAD") {
		t.Fatal("raw tool output reached the summariser")
	}
	for _, want := range []string{"fix the parser bug in lexer.go", "all 12 tests pass", "tool:"} {
		if !strings.Contains(user, want) {
			t.Errorf("transcript lost %q", want)
		}
	}
}

// TestRED_ExtractionTranscriptKeepsTaskAndOutcome: when user/assistant
// text alone exceeds the budget, the head (the original task) and the tail
// (the outcome) survive and the middle is elided with a marker.
func TestRED_ExtractionTranscriptKeepsTaskAndOutcome(t *testing.T) {
	msgs := []string{"user: ORIGINAL TASK: migrate the schedules store"}
	for i := 0; i < 400; i++ {
		msgs = append(msgs, "assistant: "+strings.Repeat("progress ", 40))
	}
	msgs = append(msgs, "assistant: FINAL OUTCOME: migration shipped as v2.9.1")
	got := buildExtractionTranscript(msgs)
	if len(got) > maxExtractionTranscriptBytes+1024 {
		t.Fatalf("transcript %d bytes; want ≤ %d", len(got), maxExtractionTranscriptBytes)
	}
	if !strings.Contains(got, "ORIGINAL TASK") || !strings.Contains(got, "FINAL OUTCOME") {
		t.Fatalf("head or tail lost:\n%s", got[:200])
	}
	if !strings.Contains(got, "elided") {
		t.Error("no elision marker for the dropped middle")
	}
	if !utf8.ValidString(got) {
		t.Error("transcript is not valid UTF-8")
	}
}

// TestRED_ExtractEpisodePromptTreatsTranscriptAsData: the single-purpose
// episode prompt must carry the same DATA-not-instructions clause as the
// combined prompt, and must ask for a summary a future session can use.
func TestRED_ExtractEpisodePromptTreatsTranscriptAsData(t *testing.T) {
	dir := t.TempDir()
	llm := &capturingLLM{reply: "The session fixed the parser."}
	mm := NewMemoryManager(dir, llm, episodeOnlyConfig())
	mm.OnSessionEndWithProvenance("20260101-prompt", 3, threeTurns, EpisodeProvenance{})
	system, _ := llm.last()
	if !strings.Contains(system, "DATA") || !strings.Contains(strings.ToLower(system), "never as instructions") {
		t.Fatalf("episode prompt lacks the data-only clause:\n%s", system)
	}
	if !strings.Contains(system, "NONE") {
		t.Fatalf("episode prompt gives the model no way to decline a trivial session:\n%s", system)
	}
}

// TestRED_ConversationalOrEmptySummaryNotStored: a reply that continues the
// chat ("Picking this back up — …", "The working tree is clean …") or says
// there is nothing to remember is not an episode and must not be written.
func TestRED_ConversationalOrEmptySummaryNotStored(t *testing.T) {
	cases := []string{
		"Picking this back up — the connection reset hit after the turn was already essentially complete. Verifying nothing was left half-done.",
		"The working tree is clean — no local changes on `main`. Let me know if you want the plan file removed too.",
		"You asked me to summarise; here's what I found: the migration is done.",
		"I'll start by checking the branch, then I've got the release to tag.",
		"NONE",
		"User greeted with \"hii\" and \"Wakeup\" — nothing to persist.  N/A",
		"N/A",
		"",
	}
	for i, reply := range cases {
		dir := t.TempDir()
		llm := &capturingLLM{reply: reply}
		mm := NewMemoryManager(dir, llm, episodeOnlyConfig())
		id := "20260101-conv"
		mm.OnSessionEndWithProvenance(id, 3, threeTurns, EpisodeProvenance{})
		if _, err := os.Stat(filepath.Join(dir, id+".md")); err == nil {
			t.Errorf("case %d: conversational/empty reply stored as an episode: %q", i, reply)
		}
	}

	// Genuine summaries still land, including ones that open with a word
	// starting in "na"/"none", quote the user, or mention "your" files past
	// the first sentence.
	for i, reply := range []string{
		"**Session summary:** the user inspected the fix/verify branch; three findings were fixed RED-first and released as v2.33.2.",
		"Native module build was fixed in pkg/a.go after the user reported a linker error.",
		"Navigated the repo and fixed the race in loop.go.",
		"Refactored the parser; the user said 'your tests fail' and the fix was committed.",
		"The session configured the CI pipeline. Your .bashrc was also updated to export GOFLAGS.",
	} {
		dir := t.TempDir()
		llm := &capturingLLM{reply: reply}
		mm := NewMemoryManager(dir, llm, episodeOnlyConfig())
		mm.OnSessionEndWithProvenance("20260101-real", 3, threeTurns, EpisodeProvenance{})
		if _, err := os.Stat(filepath.Join(dir, "20260101-real.md")); err != nil {
			t.Errorf("case %d: genuine summary not stored: %q", i, reply)
		}
	}
	for i, reply := range []string{"I’ll check the branch next.", "None of the tests were run; let me know when you want them."} {
		if looksLikeEpisode(reply) {
			t.Errorf("case %d: conversational reply accepted: %q", i, reply)
		}
	}
}

// The combined episode+facts prompt carries the same rules as the
// single-purpose one (third person, no addressing the user, NONE).
func TestCombinedPromptSharesEpisodeRules(t *testing.T) {
	dir := t.TempDir()
	llm := &capturingLLM{reply: `{"summary":"NONE","facts":[]}`}
	cfg := combinedCfg()
	mm := NewMemoryManager(dir, llm, cfg)
	mm.OnSessionEndWithProvenance("20260101-comb", 3, threeTurns, EpisodeProvenance{})
	system, _ := llm.last()
	for _, want := range []string{"NONE", "Do not address the user", "third person"} {
		if !strings.Contains(system, want) {
			t.Errorf("combined prompt lacks %q", want)
		}
	}
	if len(llm.users) != 1 {
		t.Errorf("a NONE summary must not trigger the separate-call fallback; %d calls", len(llm.users))
	}
	if _, err := os.Stat(filepath.Join(dir, "20260101-comb.md")); err == nil {
		t.Error("NONE stored as an episode")
	}
}

// TestRED_EpisodeSummaryTrimmedAtSentenceBoundary: an over-cap summary is
// cut at the last sentence end instead of mid-word, so the stored text
// stays readable and no "..." marks a dangling fragment.
func TestRED_EpisodeSummaryTrimmedAtSentenceBoundary(t *testing.T) {
	es := NewEpisodeStore(t.TempDir(), nil)
	var b strings.Builder
	for i := 0; b.Len() < maxEpisodeSummaryBytes+600; i++ {
		b.WriteString("Sentence number ")
		b.WriteString(strings.Repeat("x", 30))
		b.WriteString(" ends here. ")
	}
	if err := es.Write("20260101-trim", b.String(), 5); err != nil {
		t.Fatal(err)
	}
	got, err := es.Read("20260101-trim")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > maxEpisodeSummaryBytes {
		t.Fatalf("stored %d bytes > cap %d", len(got), maxEpisodeSummaryBytes)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "ends here.") {
		t.Fatalf("not trimmed at a sentence boundary: …%q", got[len(got)-40:])
	}
	if len(got) < maxEpisodeSummaryBytes/2 {
		t.Fatalf("trim too aggressive: %d bytes", len(got))
	}

	// No sentence boundary at all: falls back to a rune-safe cut + marker.
	if err := es.Write("20260101-nosent", strings.Repeat("日", maxEpisodeSummaryBytes), 5); err != nil {
		t.Fatal(err)
	}
	got, _ = es.Read("20260101-nosent")
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "...") || len(got) > maxEpisodeSummaryBytes {
		t.Fatalf("fallback cut wrong: valid=%v len=%d", utf8.ValidString(got), len(got))
	}
}
