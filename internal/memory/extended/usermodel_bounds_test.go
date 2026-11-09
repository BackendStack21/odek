package extended

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestAppendUniqueKeepsTheMostRecentEntries(t *testing.T) {
	var list []string
	for i := 0; i < maxUserModelListItems+15; i++ {
		list = appendUnique(list, []string{fmt.Sprintf("p%d", i)})
	}
	if len(list) != maxUserModelListItems {
		t.Fatalf("len = %d, want %d", len(list), maxUserModelListItems)
	}
	if list[0] != "p15" || list[len(list)-1] != fmt.Sprintf("p%d", maxUserModelListItems+14) {
		t.Fatalf("kept %q … %q, want the most recent entries", list[0], list[len(list)-1])
	}
}

func TestAppendUniqueMovesAReinforcedEntryToTheEnd(t *testing.T) {
	list := appendUnique(nil, []string{"tdd", "go", "docker"})
	list = appendUnique(list, []string{" tdd ", ""})
	if got := strings.Join(list, ","); got != "go,docker,tdd" {
		t.Fatalf("order = %q, want go,docker,tdd", got)
	}
	// A habit that keeps being re-inferred survives a flood of newer one-offs.
	for i := 0; i < maxUserModelListItems-1; i++ {
		list = appendUnique(list, []string{fmt.Sprintf("once-%d", i), "tdd"})
	}
	if list[len(list)-1] != "tdd" {
		t.Fatalf("reinforced entry dropped or not last: %q", list)
	}
}

func TestAppendUniqueDoesNotAliasItsInput(t *testing.T) {
	base := []string{"a", "b", "c"}
	_ = appendUnique(base[:2], []string{"a"})
	if strings.Join(base, ",") != "a,b,c" {
		t.Fatalf("input mutated: %q", base)
	}
}

func TestAppendUniqueCutsLongEntriesOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("é", maxUserModelItemRunes+50)
	list := appendUnique(nil, []string{long})
	if n := utf8.RuneCountInString(list[0]); n != maxUserModelItemRunes {
		t.Fatalf("runes = %d, want %d", n, maxUserModelItemRunes)
	}
	if !utf8.ValidString(list[0]) {
		t.Fatal("cut inside a multi-byte character")
	}
}

func TestClipRunes(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"abcdef", 3, "abc"},
		{"abc", 3, "abc"},
		{"abc", 10, "abc"},
		{"日本語テキスト", 3, "日本語"},
		{"abc", 0, ""},
	} {
		if got := clipRunes(c.in, c.n); got != c.want {
			t.Errorf("clipRunes(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// A model file written before the limits (hundreds of near-duplicate patterns)
// is cut down on load, so neither the system prompt nor the intent predictor's
// request (which marshals this state) carries the backlog.
func TestLoadBoundsAnOversizedModel(t *testing.T) {
	dir := t.TempDir()
	var patterns []string
	for i := 0; i < 671; i++ {
		patterns = append(patterns, fmt.Sprintf("hunts security bugs with RED-first tests, variant %d %s", i, strings.Repeat("x", 400)))
	}
	big := UserState{Technical: TechnicalState{Patterns: patterns, Tools: []string{"go", "go", "make"}}}
	if err := NewUserStateStore(dir).Save(big); err != nil {
		t.Fatal(err)
	}
	um := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	if err := um.Load(); err != nil {
		t.Fatal(err)
	}
	st := um.State()
	if len(st.Technical.Patterns) != maxUserModelListItems {
		t.Fatalf("patterns = %d, want %d", len(st.Technical.Patterns), maxUserModelListItems)
	}
	if !strings.Contains(st.Technical.Patterns[len(st.Technical.Patterns)-1], "variant 670") {
		t.Fatalf("did not keep the most recent pattern: %q", st.Technical.Patterns[len(st.Technical.Patterns)-1][:40])
	}
	for _, p := range st.Technical.Patterns {
		if utf8.RuneCountInString(p) > maxUserModelItemRunes {
			t.Fatalf("pattern of %d runes kept", utf8.RuneCountInString(p))
		}
	}
	if got := strings.Join(st.Technical.Tools, ","); got != "go,make" {
		t.Fatalf("tools = %q, want duplicates collapsed", got)
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	// About 150 KB before the limits; now one list's worth plus the other fields.
	if limit := 8 << 10; len(data) > limit {
		t.Fatalf("predictor state is %d bytes, want at most %d", len(data), limit)
	}
}

func TestSummaryIsBounded(t *testing.T) {
	um := NewUserModelWithStore(t.TempDir(), newMockLLM(), DefaultConfig())
	long := func(prefix string) []string {
		var out []string
		for i := 0; i < maxUserModelListItems; i++ {
			out = append(out, fmt.Sprintf("%s %d %s", prefix, i, strings.Repeat("y", maxUserModelItemRunes)))
		}
		return out
	}
	var pending []PendingReview
	for i := 0; i < 20; i++ {
		pending = append(pending, PendingReview{Field: "technical.patterns", Value: fmt.Sprintf("pending %d %s", i, strings.Repeat("z", 250)), Confidence: 0.5})
	}
	um.state = UserState{
		Technical:           TechnicalState{Languages: long("lang"), Patterns: long("pattern"), Tools: long("tool")},
		CurrentFocus:        FocusState{Project: "odek", Task: "bound the user model"},
		InteractionPatterns: InteractionPatterns{CommonOpeners: long("opener"), FollowupAfterBugfix: "runs the tests"},
		PendingReview:       pending,
	}
	summary := um.Summary()
	if summary == "" {
		t.Fatal("summary rejected")
	}
	if n := utf8.RuneCountInString(summary); n > maxUserModelSummaryRunes {
		t.Fatalf("summary is %d runes, want at most %d", n, maxUserModelSummaryRunes)
	}
	// Every section renders, whatever the lists hold, and the block stays delimited.
	for _, want := range []string{"\n═══ USER MODEL ═══\n", "patterns: ", "tools: ", "Current focus:", "bound the user model",
		"Interaction patterns:", "runs the tests", "Pending review (20):", " …"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary lacks %q", want)
		}
	}
	if !strings.HasSuffix(summary, "────────────────────\n") {
		t.Fatalf("summary lost its closing line: …%q", summary[len(summary)-40:])
	}
}

// boundOversizedStore writes a model from before the bounds (hundreds of long
// patterns) and returns a UserModel over it.
func boundOversizedStore(t *testing.T, extra func(*UserState)) (*UserModel, string) {
	t.Helper()
	dir := t.TempDir()
	var patterns []string
	for i := 0; i < 671; i++ {
		patterns = append(patterns, fmt.Sprintf("pattern %d %s", i, strings.Repeat("p", 400)))
	}
	big := UserState{Technical: TechnicalState{Patterns: patterns}}
	if extra != nil {
		extra(&big)
	}
	data, err := json.Marshal(big)
	if err != nil {
		t.Fatal(err)
	}
	// Written raw, as an older odek left it: Save would bound it already.
	if err := os.WriteFile(filepath.Join(dir, userStateFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return NewUserModelWithStore(dir, newMockLLM(), DefaultConfig()), dir
}

func savedPatterns(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, userStateFileName))
	if err != nil {
		t.Fatal(err)
	}
	var st UserState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	return st.Technical.Patterns
}

// An update that touches only the style reloads the file under the lock and
// saves the whole state: the saved lists must be bounded, not the raw backlog.
func TestStyleOnlyUpdateSavesBoundedLists(t *testing.T) {
	um, dir := boundOversizedStore(t, nil)
	if err := um.Load(); err != nil {
		t.Fatal(err)
	}
	if err := um.applyDiff(context.Background(), userStateDiff{Style: &StyleState{Tone: "direct"}}); err != nil {
		t.Fatal(err)
	}
	saved := savedPatterns(t, dir)
	if len(saved) != maxUserModelListItems || !strings.HasPrefix(saved[len(saved)-1], "pattern 670 ") {
		t.Fatalf("saved %d patterns (last %.20q), want the %d most recent", len(saved), saved[len(saved)-1], maxUserModelListItems)
	}
	if got := um.State().Technical.Patterns; len(got) != maxUserModelListItems {
		t.Fatalf("in-memory patterns = %d after the update", len(got))
	}
}

// Confirming a pending review reloads and saves the whole state the same way.
func TestPendingConfirmSavesBoundedLists(t *testing.T) {
	um, dir := boundOversizedStore(t, func(s *UserState) {
		s.PendingReview = []PendingReview{{ID: "pr-1", Field: "style.tone", Value: "dry", Evidence: strings.Repeat("e", 900), Confidence: 0.9}}
	})
	if err := um.Load(); err != nil {
		t.Fatal(err)
	}
	if err := um.ConfirmPendingReview("pr-1"); err != nil {
		t.Fatal(err)
	}
	if saved := savedPatterns(t, dir); len(saved) != maxUserModelListItems {
		t.Fatalf("saved %d patterns, want %d", len(saved), maxUserModelListItems)
	}
	if um.State().Style.Tone != "dry" {
		t.Fatalf("confirm did not apply: tone %q", um.State().Style.Tone)
	}
}

// Legacy entries (over the length limit, padded, duplicated) are normalized
// before comparison, so a re-inferred entry matches its stored copy.
func TestAppendUniqueNormalizesTheExistingList(t *testing.T) {
	legacy := strings.Repeat("L", maxUserModelItemRunes+150)
	base := []string{legacy, " go", "go", "make"}
	got := appendUnique(base, []string{legacy, "go"})
	if strings.Join(got, "|") != "make|"+legacy[:maxUserModelItemRunes]+"|go" {
		t.Fatalf("got %q", got)
	}
}

func TestBoundUserStateCutsFreeText(t *testing.T) {
	long := strings.Repeat("w", 1000)
	st := boundUserState(UserState{
		Style:         StyleState{Tone: long},
		CurrentFocus:  FocusState{Task: long},
		PendingReview: []PendingReview{{ID: "x", Value: long, Evidence: long}},
	})
	for name, v := range map[string]string{"tone": st.Style.Tone, "task": st.CurrentFocus.Task,
		"pending value": st.PendingReview[0].Value, "pending evidence": st.PendingReview[0].Evidence} {
		if utf8.RuneCountInString(v) != maxUserModelItemRunes {
			t.Fatalf("%s kept %d runes", name, utf8.RuneCountInString(v))
		}
	}
	if st.PendingReview[0].ID != "x" {
		t.Fatal("pending id changed")
	}
}

// A file of any length costs one bounded pass: 200K entries load quickly.
func TestBoundStringsIsLinear(t *testing.T) {
	list := make([]string, 200000)
	for i := range list {
		list[i] = fmt.Sprintf("entry %d", i)
	}
	start := time.Now()
	got := boundStrings(list)
	if len(got) != maxUserModelListItems || got[len(got)-1] != "entry 199999" {
		t.Fatalf("got %d entries ending %q", len(got), got[len(got)-1])
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("bounding took %v", d)
	}
}
