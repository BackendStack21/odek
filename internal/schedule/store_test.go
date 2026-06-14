package schedule

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewStoreAt: %v", err)
	}
	return st
}

func sampleJob() Job {
	return Job{
		Name:    "standup",
		Cron:    "0 9 * * 1-5",
		Task:    "Remind me about standup",
		Deliver: Delivery{Kind: DeliverStdout},
		Enabled: true,
	}
}

// ── Validation ──────────────────────────────────────────────────────────

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Job)
		wantErr bool
	}{
		{"valid", func(*Job) {}, false},
		{"empty task", func(j *Job) { j.Task = "" }, true},
		{"bad cron", func(j *Job) { j.Cron = "nope" }, true},
		{"impossible cron (Feb 30)", func(j *Job) { j.Cron = "0 0 30 2 *" }, true},
		{"impossible cron (Apr 31)", func(j *Job) { j.Cron = "0 0 31 4 *" }, true},
		{"bad timezone", func(j *Job) { j.Timezone = "Mars/Phobos" }, true},
		{"good timezone", func(j *Job) { j.Timezone = "Europe/Berlin" }, false},
		{"empty deliver kind", func(j *Job) { j.Deliver.Kind = "" }, true},
		{"unknown deliver kind", func(j *Job) { j.Deliver.Kind = "carrier-pigeon" }, true},
		{"telegram deliver", func(j *Job) { j.Deliver = Delivery{Kind: DeliverTelegram, ChatID: 42} }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := sampleJob()
			tc.mutate(&j)
			err := j.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// ── CRUD ────────────────────────────────────────────────────────────────

func TestAdd_AssignsIDAndCreatedAt(t *testing.T) {
	st := newTestStore(t)
	got, err := st.Add(sampleJob())
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got.ID == "" {
		t.Error("Add did not assign an ID")
	}
	if got.CreatedAt.IsZero() {
		t.Error("Add did not stamp CreatedAt")
	}
}

func TestAdd_RejectsInvalid(t *testing.T) {
	st := newTestStore(t)
	bad := sampleJob()
	bad.Cron = "garbage"
	if _, err := st.Add(bad); err == nil {
		t.Error("Add accepted an invalid job")
	}
	// Nothing should have been persisted.
	jobs, _ := st.List()
	if len(jobs) != 0 {
		t.Errorf("expected 0 jobs after rejected Add, got %d", len(jobs))
	}
}

func TestAdd_DuplicateID(t *testing.T) {
	st := newTestStore(t)
	j := sampleJob()
	j.ID = "jb-fixed"
	if _, err := st.Add(j); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if _, err := st.Add(j); err == nil {
		t.Error("Add accepted a duplicate ID")
	}
}

func TestGetAndList(t *testing.T) {
	st := newTestStore(t)
	a, _ := st.Add(sampleJob())
	b := sampleJob()
	b.Name = "second"
	bAdded, _ := st.Add(b)

	got, ok, err := st.Get(a.ID)
	if err != nil || !ok {
		t.Fatalf("Get(%s) ok=%v err=%v", a.ID, ok, err)
	}
	if got.Name != "standup" {
		t.Errorf("Get returned wrong job: %+v", got)
	}

	_, ok, _ = st.Get("jb-missing")
	if ok {
		t.Error("Get returned ok for a missing ID")
	}

	jobs, _ := st.List()
	if len(jobs) != 2 {
		t.Fatalf("List len = %d, want 2", len(jobs))
	}
	_ = bAdded
}

func TestPut_UpsertAndPreservesCreatedAt(t *testing.T) {
	st := newTestStore(t)
	a, _ := st.Add(sampleJob())
	created := a.CreatedAt

	a.Task = "updated task"
	a.CreatedAt = time.Time{} // simulate a caller that didn't carry it
	if err := st.Put(a); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, _, _ := st.Get(a.ID)
	if got.Task != "updated task" {
		t.Errorf("Put did not update task: %q", got.Task)
	}
	if !got.CreatedAt.Equal(created) {
		t.Errorf("Put did not preserve CreatedAt: got %v want %v", got.CreatedAt, created)
	}

	// Put with a fresh ID inserts.
	n := sampleJob()
	n.ID = "jb-new"
	if err := st.Put(n); err != nil {
		t.Fatalf("Put insert: %v", err)
	}
	if jobs, _ := st.List(); len(jobs) != 2 {
		t.Errorf("expected 2 jobs after Put-insert, got %d", len(jobs))
	}
}

func TestRemove(t *testing.T) {
	st := newTestStore(t)
	a, _ := st.Add(sampleJob())
	// Seed runtime state so we can confirm it is cleaned up.
	if err := st.SaveState(RunState{JobID: a.ID, LastStatus: StatusOK}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if err := st.Remove(a.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if jobs, _ := st.List(); len(jobs) != 0 {
		t.Errorf("expected 0 jobs after Remove, got %d", len(jobs))
	}
	states, _ := st.LoadState()
	if _, ok := states[a.ID]; ok {
		t.Error("Remove did not clean up runtime state")
	}
	if err := st.Remove("jb-missing"); err == nil {
		t.Error("Remove of missing ID should error")
	}
}

func TestSetEnabled(t *testing.T) {
	st := newTestStore(t)
	a, _ := st.Add(sampleJob())
	if err := st.SetEnabled(a.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	got, _, _ := st.Get(a.ID)
	if got.Enabled {
		t.Error("SetEnabled(false) did not disable the job")
	}
	if err := st.SetEnabled("jb-missing", true); err == nil {
		t.Error("SetEnabled on missing ID should error")
	}
}

// ── Persistence / round-trip ────────────────────────────────────────────

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st1, _ := NewStoreAt(dir)
	a, _ := st1.Add(sampleJob())

	// Re-open from the same dir — data must survive.
	st2, _ := NewStoreAt(dir)
	got, ok, err := st2.Get(a.ID)
	if err != nil || !ok {
		t.Fatalf("reopened Get ok=%v err=%v", ok, err)
	}
	if got.Cron != "0 9 * * 1-5" || got.Name != "standup" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestState_RoundTripAndIsolation(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := st.SaveState(RunState{JobID: "jb-1", LastStatus: StatusOK, LastRun: now}); err != nil {
		t.Fatalf("SaveState 1: %v", err)
	}
	if err := st.SaveState(RunState{JobID: "jb-2", LastStatus: StatusError, LastError: "boom"}); err != nil {
		t.Fatalf("SaveState 2: %v", err)
	}
	// Updating jb-2 must not disturb jb-1.
	if err := st.SaveState(RunState{JobID: "jb-2", LastStatus: StatusOK}); err != nil {
		t.Fatalf("SaveState 2b: %v", err)
	}
	states, _ := st.LoadState()
	// jb-1 must be untouched by jb-2's writes.
	if states["jb-1"].LastStatus != StatusOK || !states["jb-1"].LastRun.Equal(now) {
		t.Errorf("jb-1 state corrupted: %+v", states["jb-1"])
	}
	// jb-2 was re-saved; a fresh write replaces the whole entry, so the prior
	// LastError ("boom") must be gone.
	if states["jb-2"].LastStatus != StatusOK {
		t.Errorf("jb-2 status not updated: %+v", states["jb-2"])
	}
	if states["jb-2"].LastError != "" {
		t.Errorf("jb-2 LastError should be cleared by full-entry replace, got %q", states["jb-2"].LastError)
	}
	if err := st.SaveState(RunState{}); err == nil {
		t.Error("SaveState with empty JobID should error")
	}
}

func TestModTime(t *testing.T) {
	st := newTestStore(t)
	if !st.ModTime().IsZero() {
		t.Error("ModTime should be zero before any write")
	}
	if _, err := st.Add(sampleJob()); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if st.ModTime().IsZero() {
		t.Error("ModTime should be set after a write")
	}
}

// ── Atomicity / no temp leftovers ───────────────────────────────────────

func TestAtomicWrite_NoTempLeftover(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewStoreAt(dir)
	if _, err := st.Add(sampleJob()); err != nil {
		t.Fatalf("Add: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestAtomicWrite_SymlinkTargetReplaced(t *testing.T) {
	dir := t.TempDir()

	// Simulate an attacker swapping schedules.json with a symlink to a sensitive
	// file. The write must replace the symlink (the directory entry), not follow
	// it and overwrite the sensitive target.
	decoy := filepath.Join(dir, "decoy-sensitive.txt")
	if err := os.WriteFile(decoy, []byte("original-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, schedulesFile)
	if err := os.Symlink(decoy, target); err != nil {
		t.Fatal(err)
	}

	if err := writeJSONAtomic(target, scheduleDoc{Version: 1, Jobs: []Job{sampleJob()}}); err != nil {
		t.Fatalf("writeJSONAtomic: %v", err)
	}

	got, err := os.ReadFile(decoy)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original-secret" {
		t.Errorf("symlink target was overwritten: %q", string(got))
	}

	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("target is still a symlink; symlink was followed instead of replaced")
	}
}

// ── Concurrency (run with -race) ────────────────────────────────────────

func TestConcurrentStateWrites(t *testing.T) {
	st := newTestStore(t)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := "jb-" + string(rune('a'+n%5))
			_ = st.SaveState(RunState{JobID: id, Runs: n, LastStatus: StatusOK})
		}(i)
	}
	wg.Wait()
	states, err := st.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(states) != 5 {
		t.Errorf("expected 5 distinct job states, got %d", len(states))
	}
}
