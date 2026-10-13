package extended

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func maintenanceEM(t *testing.T) (*ExtendedMemory, string) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	dir := t.TempDir()
	em := New(dir, nil, cfg)
	t.Cleanup(func() { em.Close() })
	return em, dir
}

func seedAtom(t *testing.T, em *ExtendedMemory, text, typ string, age time.Duration) string {
	t.Helper()
	atom := MemoryAtom{Text: text, Type: typ, SourceClass: SourceUserSaid, CreatedAt: time.Now().UTC().Add(-age)}
	if err := em.AddAtom(context.Background(), atom); err != nil {
		t.Fatalf("seed %q: %v", text, err)
	}
	atoms, _ := em.List()
	for _, a := range atoms {
		if a.Text == text {
			return a.ID
		}
	}
	t.Fatalf("seeded atom %q not listed", text)
	return ""
}

// TestRED_PruneRemovesExpiredEphemeralsAndOrphanChunks: TTL-expired goals,
// questions and the like are only filtered at recall time, so they keep
// occupying the store (787 goals / 433 questions observed) and feeding the
// stale-goal nudges. Prune deletes them, keeps durable and pinned atoms, and
// sweeps chunk files whose atom is no longer in the index.
func TestRED_PruneRemovesExpiredEphemeralsAndOrphanChunks(t *testing.T) {
	em, dir := maintenanceEM(t)
	old := 40 * 24 * time.Hour
	expiredGoal := seedAtom(t, em, "Ship the schedules migration by Friday", TypeGoal, old)
	expiredQ := seedAtom(t, em, "Does the usage counter include sub-agent cost?", TypeQuestion, old)
	freshGoal := seedAtom(t, em, "Finish the memory CLI fixes", TypeGoal, time.Hour)
	durable := seedAtom(t, em, "The user prefers tabs over spaces", TypePreference, old)
	pinnedGoal := seedAtom(t, em, "Keep odek under 15 MB", TypeGoal, old)
	if err := em.PinAtom(pinnedGoal); err != nil {
		t.Fatal(err)
	}
	// An orphan chunk: a file in chunks/ with no atom behind it.
	orphan := filepath.Join(dir, "chunks", "0123456789abcdef0123456789abcdef.md")
	if err := os.WriteFile(orphan, []byte("orphan text"), 0600); err != nil {
		t.Fatal(err)
	}

	// Dry run reports but changes nothing.
	rep, err := em.Prune(time.Now().UTC(), true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ExpiredAtoms != 2 || rep.OrphanChunks != 1 {
		t.Fatalf("dry-run report = %+v, want 2 expired atoms and 1 orphan chunk", rep)
	}
	if atoms, _ := em.List(); len(atoms) != 5 {
		t.Fatalf("dry run changed the store: %d atoms", len(atoms))
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatal("dry run removed the orphan chunk")
	}

	rep, err = em.Prune(time.Now().UTC(), false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ExpiredAtoms != 2 || rep.OrphanChunks != 1 {
		t.Fatalf("report = %+v", rep)
	}
	atoms, _ := em.List()
	ids := map[string]bool{}
	for _, a := range atoms {
		ids[a.ID] = true
	}
	if ids[expiredGoal] || ids[expiredQ] {
		t.Error("expired ephemeral atoms still stored")
	}
	for _, keep := range []string{freshGoal, durable, pinnedGoal} {
		if !ids[keep] {
			t.Errorf("atom %s wrongly pruned", keep)
		}
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("orphan chunk not swept")
	}
	for _, gone := range []string{expiredGoal, expiredQ} {
		if _, err := os.Stat(filepath.Join(dir, "chunks", gone+".md")); !os.IsNotExist(err) {
			t.Errorf("chunk of pruned atom %s still on disk", gone)
		}
	}
}

// TestRED_TriageMovesMatchingLiveAtomsToQuarantine: an operator can find
// live atoms by pattern and move them to quarantine with a reason, where
// the normal promote/forget review applies. The atoms that were stored as
// user_approved from sub-agent task briefs are recovered this way.
func TestRED_TriageMovesMatchingLiveAtomsToQuarantine(t *testing.T) {
	em, _ := maintenanceEM(t)
	brief := seedAtom(t, em, "I plan to deliver a read-only adversarial review of commit 20da00f as a verdict line", TypeGoal, time.Hour)
	keep := seedAtom(t, em, "The user wants the WebUI fixed first", TypePreference, time.Hour)

	re := regexp.MustCompile(`(?i)adversarial|verdict line`)
	matched, err := em.Triage(re, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 || matched[0].ID != brief {
		t.Fatalf("triage listing = %+v, want only the brief", matched)
	}
	if atoms, _ := em.List(); len(atoms) != 2 {
		t.Fatal("listing-only triage changed the store")
	}

	matched, err = em.Triage(re, true, "operator triage: sub-agent brief")
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatalf("moved %d atoms, want 1", len(matched))
	}
	atoms, _ := em.List()
	if len(atoms) != 1 || atoms[0].ID != keep {
		t.Fatalf("live atoms after triage = %+v, want only %s", atoms, keep)
	}
	q, _ := em.ListQuarantineEntries()
	if len(q) != 1 || q[0].ID != brief || !strings.Contains(q[0].Reason, "operator triage") {
		t.Fatalf("quarantine = %+v", q)
	}
	// Review still works: promote brings it back as user_approved.
	if err := em.PromoteAtom(brief); err != nil {
		t.Fatal(err)
	}
	if atoms, _ := em.List(); len(atoms) != 2 {
		t.Fatalf("promote after triage: %d live atoms", len(atoms))
	}
}

// TestRED_PersistedIndexStatsWithoutRebuild: a fresh process (the CLI) must
// be able to report the persisted vector index — count and whether it
// matches the current corpus — without rebuilding it. Before, stats always
// said "0 vectors (dirty)" because the index was never loaded.
func TestRED_PersistedIndexStatsWithoutRebuild(t *testing.T) {
	em, dir := maintenanceEM(t)
	seedAtom(t, em, "The repo uses Go 1.27", TypeFact, time.Hour)
	seedAtom(t, em, "CI runs golangci-lint", TypeConvention, time.Hour)
	// Build and persist the index, then close this instance.
	em.Compact()
	em.Close()

	fresh := New(dir, nil, em.cfg)
	defer fresh.Close()
	vectors, stale, ok := fresh.PersistedIndex()
	if !ok {
		t.Fatal("persisted index not found")
	}
	if vectors != 2 || stale {
		t.Fatalf("persisted index = %d vectors, stale=%v; want 2, false", vectors, stale)
	}
	// A corpus change makes the persisted index stale until compacted.
	seedAtom(t, fresh, "Tests run with -count=1", TypeConvention, time.Hour)
	if _, stale, _ := fresh.PersistedIndex(); !stale {
		t.Fatal("index should be stale after the corpus changed")
	}
}

// TestRED_AtomContextCarriesSessionID: atoms extracted per turn record the
// session they came from so an operator can trace one back; the shared
// session context must flow into the atom context even when the caller
// passes none.
func TestRED_AtomContextCarriesSessionID(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	llm := &gateLLM{started: make(chan struct{}), release: make(chan struct{}), resp: extractJSONResponse("User likes Python")}
	close(llm.release)
	em := New(t.TempDir(), llm, cfg)
	defer em.Close()
	em.SetSessionContext("20260101-abc", "/work/odek")
	em.OnUserMessage(AtomContext{Turn: 3}, "I like Python")
	atoms, _ := em.List()
	if len(atoms) != 1 {
		t.Fatalf("want 1 atom, got %d", len(atoms))
	}
	if atoms[0].Context.SessionID != "20260101-abc" || atoms[0].Context.Project != "/work/odek" {
		t.Fatalf("atom context = %+v; session/project not recorded", atoms[0].Context)
	}
}
