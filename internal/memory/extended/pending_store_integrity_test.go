package extended

import (
	"io"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

// ── In-memory pending queue must not desync from the on-disk store ──────────

// TestPendingWriteThroughAcrossProcesses verifies that a mutation performed
// by a second process-like instance (the CLI) becomes visible in the first
// instance's list view (the serve-side agent view) without a restart.
func TestPendingWriteThroughAcrossProcesses(t *testing.T) {
	dir := t.TempDir()

	serve := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	if err := serve.Load(); err != nil {
		t.Fatal(err)
	}
	// Serve infers one pending entry and persists it.
	diff := userStateDiff{Pending: []PendingReview{{
		Field: "style.tone", Value: "dry", Confidence: 0.9, CreatedAt: time.Now(),
	}}}
	if err := serve.applyDiff(nil, diff); err != nil {
		t.Fatal(err)
	}
	if err := serve.Save(); err != nil {
		t.Fatal(err)
	}
	id := serve.ListPendingReview()[0].ID

	// A separate CLI instance confirms the same id on disk.
	cli := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	if err := cli.ConfirmPendingReview(id); err != nil {
		t.Fatalf("CLI confirm failed: %v", err)
	}

	// The serve-side view must converge without a restart.
	got := serve.ListPendingReview()
	if len(got) != 0 {
		t.Fatalf("serve-side view still shows %d pending entries after CLI confirm (stale in-memory queue)", len(got))
	}
}

// TestPendingConfirmResyncsFromDisk verifies that confirming an id that
// exists on disk but not in the local in-memory snapshot succeeds after a
// reload, instead of erroring "not found".
func TestPendingConfirmResyncsFromDisk(t *testing.T) {
	dir := t.TempDir()

	// A stale instance loaded before the entry existed.
	stale := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	if err := stale.Load(); err != nil {
		t.Fatal(err)
	}

	// The on-disk store gains a pending entry (written by another process).
	fresh := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	if err := fresh.applyDiff(nil, userStateDiff{Pending: []PendingReview{{
		Field: "style.verbosity", Value: "low", Confidence: 0.8, CreatedAt: time.Now(),
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Save(); err != nil {
		t.Fatal(err)
	}
	id := fresh.ListPendingReview()[0].ID

	// The stale instance must now see and confirm it.
	got := stale.ListPendingReview()
	if len(got) != 1 || got[0].ID != id {
		t.Fatalf("stale instance did not resync from disk: %+v", got)
	}
	if err := stale.ConfirmPendingReview(id); err != nil {
		t.Fatalf("confirm after disk resync failed: %v", err)
	}
	if got := stale.State().Style.Verbosity; got != "low" {
		t.Errorf("expected confirmed value applied, verbosity = %q", got)
	}
}

// TestPendingConcurrentMutationsDoNotLoseEntries verifies that two processes
// mutating the same queue (one rejects entry A, the other confirms entry B)
// converge to the union of both effects, not last-writer-wins on the array.
func TestPendingConcurrentMutationsDoNotLoseEntries(t *testing.T) {
	dir := t.TempDir()

	a := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	if err := a.applyDiff(nil, userStateDiff{Pending: []PendingReview{
		{Field: "style.tone", Value: "dry", Confidence: 0.9, CreatedAt: time.Now()},
		{Field: "style.humor", Value: "wry", Confidence: 0.8, CreatedAt: time.Now()},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	pending := a.ListPendingReview()
	if len(pending) != 2 {
		t.Fatalf("setup: expected 2 pending, got %d", len(pending))
	}
	idA, idB := pending[0].ID, pending[1].ID

	b := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	// Process B rejects entry A on disk; process A confirms entry B.
	// (Order interleaves B's save with A's stale snapshot to expose
	// last-writer-wins data loss.)
	if err := b.RejectPendingReview(idA); err != nil {
		t.Fatalf("B reject failed: %v", err)
	}
	if err := a.ConfirmPendingReview(idB); err != nil {
		t.Fatalf("A confirm failed: %v", err)
	}

	// Final state must reflect BOTH mutations.
	final := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	if err := final.Load(); err != nil {
		t.Fatal(err)
	}
	if got := final.ListPendingReview(); len(got) != 0 {
		t.Fatalf("expected queue drained, got %d entries: %+v", len(got), got)
	}
	if final.State().Style.Humor != "wry" {
		t.Errorf("A's confirm lost: humor = %q (last-writer-wins on the array)", final.State().Style.Humor)
	}
	if final.State().Style.Tone != "" {
		t.Errorf("B's reject not honored: tone = %q, expected empty", final.State().Style.Tone)
	}
}

// TestPendingSelfReferentialDefectEntryResolvable is the regression test for
// the queue entry whose own value text documented this exact store defect
// (bodek session 20260919-7d1cbaf0): it must still be confirmable via the
// normal path.
func TestPendingSelfReferentialDefectEntryResolvable(t *testing.T) {
	dir := t.TempDir()
	um := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	if err := um.applyDiff(nil, userStateDiff{Pending: []PendingReview{{
		Field:      "focus.blocker",
		Value:      "pending review 888a17e488c73fa9a2868ab593b535c3 not found — store contains stale entries",
		Confidence: 0.7,
		CreatedAt:  time.Now(),
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := um.Save(); err != nil {
		t.Fatal(err)
	}
	id := um.ListPendingReview()[0].ID
	if err := um.ConfirmPendingReview(id); err != nil {
		t.Fatalf("self-referential defect entry not confirmable: %v", err)
	}
	if got := um.State().CurrentFocus.Blocker; !strings.Contains(got, "not found") {
		t.Errorf("confirmed value not applied: %q", got)
	}
}

// ── Pending entries referencing consumed/missing atom ids must be dropped ───────────

// TestLoadSweepDropsPendingWithMissingAtom verifies that a pending entry
// whose referenced atom id no longer exists is dropped on load and the drop
// is logged.
func TestLoadSweepDropsPendingWithMissingAtom(t *testing.T) {
	dir := t.TempDir()
	store := NewUserStateStore(dir)
	if err := store.Save(UserState{PendingReview: []PendingReview{{
		ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1", Field: "focus.blocker",
		Value: "resolved once the stale pending_review entries (bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb) are pruned",
	}}}); err != nil {
		t.Fatal(err)
	}

	logs := captureLogs(func() {
		um := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
		um.SetAtomChecker(func(id string) bool { return false }) // atom gone
		if err := um.Load(); err != nil {
			t.Fatal(err)
		}
		if got := um.ListPendingReview(); len(got) != 0 {
			t.Fatalf("stale pending entry survived load: %+v", got)
		}
	})
	if !strings.Contains(logs, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1") {
		t.Errorf("expected sweep to log dropped entry id, logs: %q", logs)
	}

	// The drop is persisted.
	disk := NewUserStateStore(dir)
	state, err := disk.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.PendingReview) != 0 {
		t.Errorf("drop not persisted to disk: %+v", state.PendingReview)
	}
}

// TestLoadSweepKeepsPendingWithLiveAtom verifies entries whose referenced
// atom still exists are kept.
func TestLoadSweepKeepsPendingWithLiveAtom(t *testing.T) {
	dir := t.TempDir()
	store := NewUserStateStore(dir)
	if err := store.Save(UserState{PendingReview: []PendingReview{{
		ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa2", Field: "focus.task",
		Value: "finish workstream bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}}}); err != nil {
		t.Fatal(err)
	}
	um := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	um.SetAtomChecker(func(id string) bool { return id == "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" })
	if err := um.Load(); err != nil {
		t.Fatal(err)
	}
	if got := um.ListPendingReview(); len(got) != 1 {
		t.Fatalf("live-atom pending entry was dropped: %+v", got)
	}
}

// TestLoadSweepConsolidatesDuplicatePending verifies identical (field, value)
// duplicates across sessions collapse to one entry, keeping the highest
// confidence.
func TestLoadSweepConsolidatesDuplicatePending(t *testing.T) {
	dir := t.TempDir()
	store := NewUserStateStore(dir)
	val := "confirm the release convention"
	if err := store.Save(UserState{PendingReview: []PendingReview{
		{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa3", Field: "interaction_patterns.common_openers", Value: val, Confidence: 0.6},
		{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa4", Field: "interaction_patterns.common_openers", Value: val, Confidence: 0.85},
		{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa5", Field: "style.tone", Value: "different", Confidence: 0.5},
	}}); err != nil {
		t.Fatal(err)
	}
	um := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	if err := um.Load(); err != nil {
		t.Fatal(err)
	}
	got := um.ListPendingReview()
	if len(got) != 2 {
		t.Fatalf("expected 2 entries after consolidation, got %d: %+v", len(got), got)
	}
	for _, p := range got {
		if p.Value == val && p.Confidence != 0.85 {
			t.Errorf("expected highest-confidence duplicate kept (0.85), got %.2f", p.Confidence)
		}
	}
}

// TestLoadSweepNoCheckerKeepsPending verifies that when no atom checker is
// installed (checker-less builds/tests) the sweep only dedups and never
// drops entries — fail-open on hygiene, never fail-closed on data.
func TestLoadSweepNoCheckerKeepsPending(t *testing.T) {
	dir := t.TempDir()
	store := NewUserStateStore(dir)
	if err := store.Save(UserState{PendingReview: []PendingReview{{
		ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa6", Field: "style.tone",
		Value: "references atom cccccccccccccccccccccccccccccccc blindly",
	}}}); err != nil {
		t.Fatal(err)
	}
	um := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	if err := um.Load(); err != nil {
		t.Fatal(err)
	}
	if got := um.ListPendingReview(); len(got) != 1 {
		t.Fatalf("entry dropped without atom checker installed: %+v", got)
	}
}

// TestListNeverContainsIDsAbsentFromDisk pins the acceptance invariant: the
// in-memory list served to agents never contains an id absent from the
// persisted array.
func TestListNeverContainsIDsAbsentFromDisk(t *testing.T) {
	dir := t.TempDir()
	serve := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	if err := serve.Load(); err != nil {
		t.Fatal(err)
	}
	if err := serve.applyDiff(nil, userStateDiff{Pending: []PendingReview{{
		Field: "style.tone", Value: "dry", Confidence: 0.9, CreatedAt: time.Now(),
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := serve.Save(); err != nil {
		t.Fatal(err)
	}

	// Another process drains the queue on disk.
	cli := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	for _, p := range func() []PendingReview {
		s, _ := NewUserStateStore(dir).Load()
		return s.PendingReview
	}() {
		if err := cli.RejectPendingReview(p.ID); err != nil {
			t.Fatal(err)
		}
	}

	got := serve.ListPendingReview()
	var ids []string
	for _, p := range got {
		ids = append(ids, p.ID)
	}
	state, err := NewUserStateStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	onDisk := map[string]bool{}
	for _, p := range state.PendingReview {
		onDisk[p.ID] = true
	}
	for _, id := range ids {
		if !onDisk[id] {
			t.Errorf("in-memory list serves id %s absent from the persisted array", id)
		}
	}
}

// TestApplyDiffDoesNotResurrectExternallyConfirmed is the regression test
// for the reviewer-found flock bypass: a serve process holding a stale
// in-memory pending queue must not re-persist an entry another process
// confirmed while its next inference cycle writes through.
func TestApplyDiffDoesNotResurrectExternallyConfirmed(t *testing.T) {
	dir := t.TempDir()
	serve := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	if err := serve.Load(); err != nil {
		t.Fatal(err)
	}
	if err := serve.applyDiff(nil, userStateDiff{Pending: []PendingReview{{
		Field: "style.tone", Value: "dry", Confidence: 0.9, CreatedAt: time.Now(),
	}}}); err != nil {
		t.Fatal(err)
	}
	id := serve.ListPendingReview()[0].ID

	// CLI confirms the entry on disk.
	cli := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	if err := cli.ConfirmPendingReview(id); err != nil {
		t.Fatal(err)
	}

	// Serve's next inference cycle (empty diff, write-through) must observe
	// the disk state under the flock and not resurrect the confirmed entry.
	if err := serve.applyDiff(nil, userStateDiff{}); err != nil {
		t.Fatal(err)
	}
	got := serve.ListPendingReview()
	if len(got) != 0 {
		t.Fatalf("confirmed entry resurrected by inference write-through: %+v", got)
	}
}

// captureLogs redirects the package logger's output for the duration of fn.
func captureLogs(fn func()) string {
	old := log.Writer()
	r, w, err := os.Pipe()
	if err != nil {
		fn()
		return ""
	}
	log.SetOutput(w)
	done := make(chan string, 1)
	go func() {
		var buf strings.Builder
		io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	w.Close()
	log.SetOutput(old)
	return <-done
}
