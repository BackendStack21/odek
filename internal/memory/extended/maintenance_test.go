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

func seedMaintAtom(t *testing.T, em *ExtendedMemory, text, typ string, age time.Duration) string {
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
	expiredGoal := seedMaintAtom(t, em, "Ship the schedules migration by Friday", TypeGoal, old)
	expiredQ := seedMaintAtom(t, em, "Does the usage counter include sub-agent cost?", TypeQuestion, old)
	freshGoal := seedMaintAtom(t, em, "Finish the memory CLI fixes", TypeGoal, time.Hour)
	durable := seedMaintAtom(t, em, "The user prefers tabs over spaces", TypePreference, old)
	pinnedGoal := seedMaintAtom(t, em, "Keep odek under 15 MB", TypeGoal, old)
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
	brief := seedMaintAtom(t, em, "I plan to deliver a read-only adversarial review of commit 20da00f as a verdict line", TypeGoal, time.Hour)
	keep := seedMaintAtom(t, em, "The user wants the WebUI fixed first", TypePreference, time.Hour)

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
	seedMaintAtom(t, em, "The repo uses Go 1.27", TypeFact, time.Hour)
	seedMaintAtom(t, em, "CI runs golangci-lint", TypeConvention, time.Hour)
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
	// A corpus change behind this instance's back (another process writing
	// the store) makes the persisted index stale until it is rebuilt.
	if err := NewAtomStore(dir).Add(MemoryAtom{ID: "0123456789abcdef0123456789abcdef", Text: "Tests run with -count=1",
		Type: TypeConvention, SourceClass: SourceUserSaid, CreatedAt: time.Now().UTC()}, 0); err != nil {
		t.Fatal(err)
	}
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
	llm := newMockLLM(extractJSONResponse("User likes Python"))
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

// Edge cases of the maintenance API: a disabled subsystem refuses, a nil
// pattern is an error, pinned atoms are listed by triage but never moved, a
// dry run counts expired quarantine entries, and a store without a persisted
// index reports ok=false.
func TestMaintenanceEdgeCases(t *testing.T) {
	disabled := New(t.TempDir(), nil, DefaultConfig())
	if _, err := disabled.Prune(time.Now(), true); err == nil {
		t.Error("Prune on a disabled subsystem should error")
	}
	if _, err := disabled.Triage(regexp.MustCompile("x"), false, ""); err == nil {
		t.Error("Triage on a disabled subsystem should error")
	}
	var nilEM *ExtendedMemory
	if _, _, ok := nilEM.PersistedIndex(); ok {
		t.Error("nil receiver reports a persisted index")
	}

	em, _ := maintenanceEM(t)
	if _, err := em.Triage(nil, false, ""); err == nil {
		t.Error("nil pattern should error")
	}
	if _, _, ok := em.PersistedIndex(); ok {
		t.Error("fresh store reports a persisted index")
	}
	pinned := seedMaintAtom(t, em, "Keep the binary under 15 MB", TypeGoal, time.Hour)
	if err := em.PinAtom(pinned); err != nil {
		t.Fatal(err)
	}
	matched, err := em.Triage(regexp.MustCompile("binary"), true, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Fatalf("pinned atom not listed: %+v", matched)
	}
	if atoms, _ := em.List(); len(atoms) != 1 {
		t.Fatal("pinned atom was moved to quarantine")
	}

	// Expired quarantine entries are counted on a dry run and evicted on a
	// real one.
	em.quarantine.SetTTLDays(7)
	em.cfg.QuarantineTTLDays = 7
	old := MemoryAtom{ID: "fedcba9876543210fedcba9876543210", Text: "stale quarantined", Type: TypeFact, SourceClass: SourceWeb, CreatedAt: time.Now().UTC().Add(-30 * 24 * time.Hour)}
	if err := em.quarantine.StoreWithReason(old, "tainted"); err != nil {
		t.Fatal(err)
	}
	// Backdate the quarantine timestamp: StoreWithReason stamps now.
	entries, _ := em.quarantine.loadAtomsLocked()
	for i := range entries {
		entries[i].QuarantinedAt = time.Now().UTC().Add(-30 * 24 * time.Hour)
	}
	if err := em.quarantine.saveLocked(entries); err != nil {
		t.Fatal(err)
	}
	rep, err := em.Prune(time.Now().UTC(), true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ExpiredQuarantine != 1 {
		t.Fatalf("dry run counted %d expired quarantine entries, want 1", rep.ExpiredQuarantine)
	}
	rep, err = em.Prune(time.Now().UTC(), false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ExpiredQuarantine != 1 {
		t.Fatalf("prune evicted %d expired quarantine entries, want 1", rep.ExpiredQuarantine)
	}
}

// Remaining branches: a store that never wrote a chunk has no chunks
// directory; a zero quarantine TTL counts nothing; an unpinned triage move
// without a reason gets the default one; a corrupt or incomplete persisted
// index reports ok=false.
func TestMaintenanceRemainingBranches(t *testing.T) {
	em, dir := maintenanceEM(t)
	em.cfg.QuarantineTTLDays = 0
	rep, err := em.Prune(time.Now().UTC(), true)
	if err != nil || rep != (PruneReport{}) {
		t.Fatalf("prune on empty store: %+v %v", rep, err)
	}

	id := seedMaintAtom(t, em, "I plan to deliver a verdict line", TypeGoal, time.Hour)
	if _, err := em.Triage(regexp.MustCompile("verdict"), true, ""); err != nil {
		t.Fatal(err)
	}
	q, _ := em.ListQuarantineEntries()
	if len(q) != 1 || q[0].ID != id || q[0].Reason != "operator triage" {
		t.Fatalf("quarantine = %+v, want default reason", q)
	}

	// Persisted meta without the vectors file, then a corrupt meta file.
	if err := os.WriteFile(filepath.Join(dir, vectorMetaFile), []byte(`{"fingerprint":"rp/256","corpus":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, vectorFile))
	if _, _, ok := em.PersistedIndex(); ok {
		t.Error("missing vectors file reported as a persisted index")
	}
	if err := os.WriteFile(filepath.Join(dir, vectorMetaFile), []byte(`{not json`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := em.PersistedIndex(); ok {
		t.Error("corrupt meta reported as a persisted index")
	}
}
