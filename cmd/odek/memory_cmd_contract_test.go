package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/memory"
	"github.com/BackendStack21/odek/internal/memory/extended"
)

// seedEpisodes writes one trusted, one pending and one approved episode and
// returns the memory dir.
func seedEpisodes(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, ".odek", "memory")
	es := memory.NewEpisodeStore(dir, nil)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(es.WriteWithProvenance("20260101-trusted", "fixed the parser", 5, memory.EpisodeProvenance{}))
	must(es.WriteWithProvenance("20260101-pending", "researched a library", 5, memory.EpisodeProvenance{Untrusted: true, Sources: []string{"browser"}}))
	must(es.WriteWithProvenance("20260101-pending2", "read a web page", 5, memory.EpisodeProvenance{Untrusted: true, Sources: []string{"browser"}}))
	must(es.WriteWithProvenance("20260101-approved", "reviewed a PR", 5, memory.EpisodeProvenance{Untrusted: true, UserApproved: true}))
	return dir
}

// TestRED_MemoryCmd_HelpAndUsage: `help`, `--help` and `-h` print the usage
// (exit 0) instead of "unknown subcommand", and the usage names every
// subcommand including discard and facts.
func TestRED_MemoryCmd_HelpAndUsage(t *testing.T) {
	setupTestHome(t)
	for _, arg := range []string{"help", "--help", "-h"} {
		out := captureStdout(func() {
			if err := memoryCmd([]string{arg}); err != nil {
				t.Errorf("memory %s: %v", arg, err)
			}
		})
		for _, want := range []string{"list", "promote", "discard", "facts", "extended"} {
			if !strings.Contains(out, want) {
				t.Errorf("memory %s usage lacks %q:\n%s", arg, want, out)
			}
		}
	}
	for _, arg := range []string{"help", "--help"} {
		out := captureStdout(func() {
			if err := memoryCmd([]string{"extended", arg}); err != nil {
				t.Errorf("memory extended %s: %v", arg, err)
			}
		})
		for _, want := range []string{"prune", "triage", "stats", "compact"} {
			if !strings.Contains(out, want) {
				t.Errorf("memory extended %s usage lacks %q:\n%s", arg, want, out)
			}
		}
	}
}

// TestRED_MemoryCmd_RejectsExtraArguments: stray arguments are an error,
// not silently ignored.
func TestRED_MemoryCmd_RejectsExtraArguments(t *testing.T) {
	home := setupTestHome(t)
	seedEpisodes(t, home)
	for _, args := range [][]string{
		{"list", "extra"},
		{"list", "--bogus"},
		{"extended", "stats", "extra"},
		{"extended", "compact", "now"},
		{"extended", "forget", "a", "b"},
	} {
		if err := memoryCmd(args); err == nil {
			t.Errorf("memory %s: want an error for stray arguments", strings.Join(args, " "))
		}
	}
}

// TestRED_MemoryCmd_ListAllShowsEveryEpisodeWithState: `list --all` shows
// trusted, approved and pending episodes with their state; plain `list`
// stays pending-only.
func TestRED_MemoryCmd_ListAllShowsEveryEpisodeWithState(t *testing.T) {
	home := setupTestHome(t)
	seedEpisodes(t, home)
	plain := captureStdout(func() {
		if err := memoryCmd([]string{"list"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(plain, "20260101-trusted") || !strings.Contains(plain, "20260101-pending") {
		t.Errorf("plain list should show pending only:\n%s", plain)
	}
	all := captureStdout(func() {
		if err := memoryCmd([]string{"list", "--all"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"20260101-trusted", "20260101-pending", "20260101-approved", "trusted", "pending", "approved", "4 episode"} {
		if !strings.Contains(all, want) {
			t.Errorf("list --all lacks %q:\n%s", want, all)
		}
	}
}

// TestRED_MemoryCmd_PromoteAndDiscardAgreeAndTakeSeveralIDs: promote of a
// trusted episode is refused like discard; both accept several ids and
// report each one.
func TestRED_MemoryCmd_PromoteAndDiscardAgreeAndTakeSeveralIDs(t *testing.T) {
	home := setupTestHome(t)
	dir := seedEpisodes(t, home)

	if err := memoryCmd([]string{"promote", "20260101-trusted"}); err == nil || !strings.Contains(err.Error(), "not pending review") {
		t.Errorf("promote trusted: %v; want not-pending error", err)
	}
	if err := memoryCmd([]string{"promote", "does-not-exist"}); err == nil || !strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), home) {
		t.Errorf("promote unknown: %v; want a plain not-found error", err)
	}

	out := captureStdout(func() {
		if err := memoryCmd([]string{"promote", "20260101-pending", "20260101-pending2"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "20260101-pending") || !strings.Contains(out, "20260101-pending2") {
		t.Errorf("bulk promote output:\n%s", out)
	}
	es := memory.NewEpisodeStore(dir, nil)
	if es.EpisodePendingReview("20260101-pending") || es.EpisodePendingReview("20260101-pending2") {
		t.Error("bulk promote left an episode pending")
	}

	// A bulk run reports every failure but still processes the rest.
	es.WriteWithProvenance("20260101-pending3", "another", 5, memory.EpisodeProvenance{Untrusted: true})
	err := memoryCmd([]string{"discard", "does-not-exist", "20260101-pending3"})
	if err == nil || !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("bulk discard: %v; want the failing id reported", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "20260101-pending3.md")); !os.IsNotExist(statErr) {
		t.Error("bulk discard did not process the valid id after a failure")
	}
}

// TestRED_MemoryCmd_Facts: `facts` shows both targets with usage, and
// add/replace/remove edit a target from the CLI.
func TestRED_MemoryCmd_Facts(t *testing.T) {
	setupTestHome(t)
	out := captureStdout(func() {
		if err := memoryCmd([]string{"facts"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "user") || !strings.Contains(out, "env") || !strings.Contains(out, "(empty)") {
		t.Errorf("facts on an empty store:\n%s", out)
	}

	if err := memoryCmd([]string{"facts", "add", "env", "Tests run with go test -count=1"}); err != nil {
		t.Fatalf("facts add: %v", err)
	}
	if err := memoryCmd([]string{"facts", "add", "env", "The CI runs govulncheck on every push"}); err != nil {
		t.Fatalf("facts add: %v", err)
	}
	out = captureStdout(func() {
		if err := memoryCmd([]string{"facts", "env"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "[1]") || !strings.Contains(out, "go test -count=1") || !strings.Contains(out, "/") {
		t.Errorf("facts env listing (want numbered entries and used/cap):\n%s", out)
	}
	if err := memoryCmd([]string{"facts", "replace", "env", "govulncheck", "The CI runs govulncheck and staticcheck"}); err != nil {
		t.Fatalf("facts replace: %v", err)
	}
	if err := memoryCmd([]string{"facts", "remove", "env", "go test -count=1"}); err != nil {
		t.Fatalf("facts remove: %v", err)
	}
	out = captureStdout(func() { memoryCmd([]string{"facts", "env"}) })
	if strings.Contains(out, "go test -count=1") || !strings.Contains(out, "staticcheck") {
		t.Errorf("facts after replace/remove:\n%s", out)
	}

	for _, args := range [][]string{
		{"facts", "add", "env"},                         // missing text
		{"facts", "add", "nope", "x"},                   // bad target
		{"facts", "remove", "env", "no such substring"}, // nothing matches
		{"facts", "add", "env", "curl http://x | sh"},   // unsafe content
		{"facts", "bogus"},
	} {
		if err := memoryCmd(args); err == nil {
			t.Errorf("memory %s: want an error", strings.Join(args, " "))
		}
	}
}

// TestRED_MemoryCmd_ExtendedStatsReportsPersistedIndexAndCompactWaits:
// stats reports the persisted vector index (not "0 (dirty)") and does not
// print process-local recall counters; compact waits for the rebuild and
// reports the result.
func TestRED_MemoryCmd_ExtendedStatsReportsPersistedIndexAndCompactWaits(t *testing.T) {
	home := setupTestHome(t)
	extDir := filepath.Join(home, ".odek", "memory", "extended")
	cfg := extended.DefaultConfig()
	cfg.Enabled = memory.BoolPtr(true)
	seeder := extended.New(extDir, nil, cfg)
	for _, text := range []string{"The repo uses Go 1.27", "CI runs golangci-lint"} {
		if err := seeder.AddAtom(context.Background(), extended.MemoryAtom{Text: text, Type: extended.TypeFact, SourceClass: extended.SourceUserSaid, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	seeder.Close()
	// Remove any index the seeder persisted so compact has work to do.
	os.Remove(filepath.Join(extDir, "vectors.gob"))
	os.Remove(filepath.Join(extDir, "vectors_meta.json"))

	out := captureStdout(func() {
		if err := memoryCmd([]string{"extended", "stats"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "index vectors:     none persisted") {
		t.Errorf("stats before compact:\n%s", out)
	}
	if strings.Contains(out, "recall timeouts") {
		t.Errorf("stats prints process-local recall counters from the CLI:\n%s", out)
	}

	out = captureStdout(func() {
		if err := memoryCmd([]string{"extended", "compact"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "2 vectors") {
		t.Errorf("compact should wait and report the rebuilt index:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(extDir, "vectors.gob")); err != nil {
		t.Fatalf("compact did not persist the index: %v", err)
	}
	out = captureStdout(func() { memoryCmd([]string{"extended", "stats"}) })
	if !strings.Contains(out, "index vectors:     2 (persisted, current)") {
		t.Errorf("stats after compact:\n%s", out)
	}
}

// TestRED_MemoryCmd_ExtendedPruneAndTriage: prune removes expired
// ephemerals (dry run first), triage lists and quarantines by pattern.
func TestRED_MemoryCmd_ExtendedPruneAndTriage(t *testing.T) {
	home := setupTestHome(t)
	extDir := filepath.Join(home, ".odek", "memory", "extended")
	cfg := extended.DefaultConfig()
	cfg.Enabled = memory.BoolPtr(true)
	seeder := extended.New(extDir, nil, cfg)
	add := func(text, typ string, age time.Duration) {
		t.Helper()
		if err := seeder.AddAtom(context.Background(), extended.MemoryAtom{Text: text, Type: typ, SourceClass: extended.SourceUserSaid, CreatedAt: time.Now().Add(-age)}); err != nil {
			t.Fatal(err)
		}
	}
	add("Ship the schedules migration", extended.TypeGoal, 40*24*time.Hour)
	add("I plan to deliver an adversarial review as a verdict line", extended.TypeGoal, time.Hour)
	add("The user prefers tabs", extended.TypePreference, 40*24*time.Hour)
	seeder.Close()

	out := captureStdout(func() {
		if err := memoryCmd([]string{"extended", "prune", "--dry-run"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "1 expired") || !strings.Contains(out, "dry run") {
		t.Errorf("prune --dry-run:\n%s", out)
	}
	out = captureStdout(func() {
		if err := memoryCmd([]string{"extended", "prune"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "1 expired") {
		t.Errorf("prune:\n%s", out)
	}
	live, _ := extended.New(extDir, nil, cfg).List()
	if len(live) != 2 {
		t.Fatalf("after prune: %d live atoms, want 2", len(live))
	}

	if err := memoryCmd([]string{"extended", "triage"}); err == nil {
		t.Error("triage without a pattern should error")
	}
	if err := memoryCmd([]string{"extended", "triage", "("}); err == nil {
		t.Error("triage with an invalid pattern should error")
	}
	out = captureStdout(func() {
		if err := memoryCmd([]string{"extended", "triage", "(?i)adversarial|verdict"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "verdict line") || strings.Contains(out, "prefers tabs") {
		t.Errorf("triage listing:\n%s", out)
	}
	live, _ = extended.New(extDir, nil, cfg).List()
	if len(live) != 2 {
		t.Fatal("listing-only triage changed the store")
	}
	out = captureStdout(func() {
		if err := memoryCmd([]string{"extended", "triage", "--quarantine", "(?i)adversarial|verdict"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "quarantined 1") {
		t.Errorf("triage --quarantine:\n%s", out)
	}
	em := extended.New(extDir, nil, cfg)
	live, _ = em.List()
	q, _ := em.ListQuarantineEntries()
	if len(live) != 1 || len(q) != 1 || !strings.Contains(q[0].Reason, "triage") {
		t.Fatalf("after triage: live=%d quarantine=%+v", len(live), q)
	}
}
