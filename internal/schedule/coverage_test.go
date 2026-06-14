package schedule

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file targets the error-handling and edge-case branches that the main
// test files leave uncovered, pushing the package to full statement coverage.

// writeFile is a tiny helper for seeding (possibly corrupt) on-disk state.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ── NewStore / NewStoreAt ─────────────────────────────────────────────────

func TestNewStore_HomeAndError(t *testing.T) {
	// Happy path: HOME points at a writable temp dir.
	t.Setenv("HOME", t.TempDir())
	if _, err := NewStore(); err != nil {
		t.Fatalf("NewStore with valid HOME: %v", err)
	}
	// Error path: an empty HOME makes os.UserHomeDir fail on Linux.
	t.Setenv("HOME", "")
	if _, err := NewStore(); err == nil {
		t.Error("NewStore with empty HOME should error")
	}
}

func TestNewStoreAt_MkdirError(t *testing.T) {
	// A path component that is a file makes MkdirAll fail with ENOTDIR.
	f := filepath.Join(t.TempDir(), "afile")
	writeFile(t, f, "x")
	if _, err := NewStoreAt(filepath.Join(f, "sub")); err == nil {
		t.Error("NewStoreAt under a file should error")
	}
}

// ── loadDoc error propagation across CRUD ─────────────────────────────────

func TestCRUD_LoadDocError(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewStoreAt(dir)
	// Corrupt the definitions file so every read fails to parse.
	writeFile(t, filepath.Join(dir, schedulesFile), "{not json")

	if _, err := st.Add(sampleJob()); err == nil {
		t.Error("Add should surface a corrupt store")
	}
	if _, err := st.List(); err == nil {
		t.Error("List should surface a corrupt store")
	}
	if _, _, err := st.Get("jb-x"); err == nil {
		t.Error("Get should surface a corrupt store")
	}
	put := sampleJob()
	put.ID = "jb-x"
	if err := st.Put(put); err == nil {
		t.Error("Put should surface a corrupt store")
	}
	if err := st.Remove("jb-x"); err == nil {
		t.Error("Remove should surface a corrupt store")
	}
	if err := st.SetEnabled("jb-x", true); err == nil {
		t.Error("SetEnabled should surface a corrupt store")
	}
}

func TestLoadState_Error(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewStoreAt(dir)
	writeFile(t, filepath.Join(dir, stateFile), "{bad")
	if _, err := st.LoadState(); err == nil {
		t.Error("LoadState should surface a corrupt state file")
	}
	if err := st.SaveState(RunState{JobID: "jb-1"}); err == nil {
		t.Error("SaveState should surface a corrupt state file on its load step")
	}
}

// ── saveDoc / writeJSONAtomic error paths ─────────────────────────────────

// makeReadOnly makes dir read-only so fsatomic.WriteFile's temp-file creation
// fails, exercising the save error path.
func makeReadOnly(t *testing.T, dir string) func() {
	t.Helper()
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	return func() { os.Chmod(dir, 0755) }
}

func TestAdd_SaveDocError(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewStoreAt(dir)
	cleanup := makeReadOnly(t, dir)
	defer cleanup()
	if _, err := st.Add(sampleJob()); err == nil {
		t.Error("Add should fail when the definitions file can't be written")
	}
}

func TestRemove_SaveDocError(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewStoreAt(dir)
	a, err := st.Add(sampleJob())
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	cleanup := makeReadOnly(t, dir)
	defer cleanup()
	if err := st.Remove(a.ID); err == nil {
		t.Error("Remove should fail when the definitions file can't be rewritten")
	}
}

func TestPut_ValidateAndEmptyID(t *testing.T) {
	st := newTestStore(t)
	bad := sampleJob()
	bad.ID = "jb-1"
	bad.Cron = "garbage"
	if err := st.Put(bad); err == nil {
		t.Error("Put should reject an invalid job")
	}
	noID := sampleJob()
	if err := st.Put(noID); err == nil {
		t.Error("Put should require an ID")
	}
}

// ── internal IO helpers (same-package direct calls) ───────────────────────

func TestReadJSON_ReadAndParseErrors(t *testing.T) {
	dir := t.TempDir()

	// A directory in place of the file makes os.ReadFile fail (not IsNotExist).
	asDir := filepath.Join(dir, "isdir.json")
	if err := os.Mkdir(asDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := readJSON(asDir, &scheduleDoc{}); err == nil {
		t.Error("readJSON should error when the path is a directory")
	}

	// An empty file is treated as an empty document (no error).
	empty := filepath.Join(dir, "empty.json")
	writeFile(t, empty, "")
	if err := readJSON(empty, &scheduleDoc{}); err != nil {
		t.Errorf("readJSON of an empty file should be nil, got %v", err)
	}

	// Invalid JSON is a parse error.
	bad := filepath.Join(dir, "bad.json")
	writeFile(t, bad, "{nope}")
	if err := readJSON(bad, &scheduleDoc{}); err == nil {
		t.Error("readJSON should error on invalid JSON")
	}
}

func TestWriteJSONAtomic_Errors(t *testing.T) {
	dir := t.TempDir()

	// Marshal error: a channel cannot be encoded to JSON.
	if err := writeJSONAtomic(filepath.Join(dir, "x.json"), make(chan int)); err == nil {
		t.Error("writeJSONAtomic should fail to marshal a channel")
	}

	// Write error: the directory is read-only, so temp-file creation fails.
	wdir := filepath.Join(dir, "wro")
	if err := os.Mkdir(wdir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wdir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(wdir, 0755)
	wpath := filepath.Join(wdir, "w.json")
	if err := writeJSONAtomic(wpath, map[string]int{"a": 1}); err == nil {
		t.Error("writeJSONAtomic should fail when the directory is not writable")
	}

	// Rename error: the destination is a non-empty directory, so rename of the
	// temp file onto it fails after a successful temp write.
	rpath := filepath.Join(dir, "r.json")
	if err := os.Mkdir(rpath, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(rpath, "child"), "x") // make it non-empty
	if err := writeJSONAtomic(rpath, map[string]int{"a": 1}); err == nil {
		t.Error("writeJSONAtomic should fail to rename onto a directory")
	}
}

func TestSaveDocSaveState_VersionDefaulting(t *testing.T) {
	st := newTestStore(t)
	// A zero-version document must be stamped to 1 on save.
	if err := st.saveDoc(&scheduleDoc{}); err != nil {
		t.Fatalf("saveDoc: %v", err)
	}
	doc, err := st.loadDoc()
	if err != nil || doc.Version != 1 {
		t.Errorf("saveDoc did not default Version: %+v err=%v", doc, err)
	}
	if err := st.saveState(&stateDoc{States: map[string]RunState{}}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	sd, err := st.loadState()
	if err != nil || sd.Version != 1 {
		t.Errorf("saveState did not default Version: %+v err=%v", sd, err)
	}
}

func TestLoadState_NullStatesMap(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewStoreAt(dir)
	// A persisted null states map must be normalised to an empty (non-nil) map.
	writeFile(t, filepath.Join(dir, stateFile), `{"version":1,"states":null}`)
	states, err := st.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if states == nil {
		t.Error("LoadState should return a non-nil map for a null states field")
	}
}

func TestFileLock_OpenError(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewStoreAt(dir)
	// A directory where the lock file should be makes OpenFile fail; the store
	// must still proceed (best-effort lock), so Add succeeds.
	if err := os.Mkdir(filepath.Join(dir, "schedules.lock"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add(sampleJob()); err != nil {
		t.Errorf("Add should still succeed when the lock can't be opened: %v", err)
	}
}

// ── List sort tiebreak ────────────────────────────────────────────────────

func TestList_SortByIDOnEqualCreatedAt(t *testing.T) {
	st := newTestStore(t)
	at := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	j1 := sampleJob()
	j1.ID, j1.CreatedAt = "jb-bbb", at
	j2 := sampleJob()
	j2.ID, j2.CreatedAt = "jb-aaa", at
	if _, err := st.Add(j1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add(j2); err != nil {
		t.Fatal(err)
	}
	jobs, _ := st.List()
	if len(jobs) != 2 || jobs[0].ID != "jb-aaa" {
		t.Errorf("equal CreatedAt should tiebreak by ID; got order %v", []string{jobs[0].ID, jobs[1].ID})
	}
}

// ── cronexpr edge branches ────────────────────────────────────────────────

func TestParseInLocation_NilLocDefaultsUTC(t *testing.T) {
	s, err := ParseInLocation("0 9 * * *", nil)
	if err != nil {
		t.Fatalf("ParseInLocation(nil loc): %v", err)
	}
	if s.loc != time.UTC {
		t.Errorf("nil loc should default to UTC, got %v", s.loc)
	}
}

func TestParseField_EmptyField(t *testing.T) {
	if _, _, err := parseField("", 0, 59, nil); err == nil {
		t.Error("parseField should reject an empty field")
	}
}

func TestParse_RangeAndEmptyValueErrors(t *testing.T) {
	bad := []string{
		"zz-5 * * * *", // non-numeric range low
		"5-zz * * * *", // non-numeric range high
		"-5 * * * *",   // empty range low
		"5- * * * *",   // empty range high
	}
	for _, expr := range bad {
		if _, err := Parse(expr); err == nil {
			t.Errorf("Parse(%q): expected error", expr)
		}
	}
}

func TestMatches_MonthMismatch(t *testing.T) {
	s := mustParse(t, "0 0 1 6 *") // only June 1
	if s.Matches(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("July must not match a June-only schedule")
	}
	if !s.Matches(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("June 1 should match")
	}
}

// ── scheduler edge branches ───────────────────────────────────────────────

func TestReconcile_ListAndLoadStateErrors(t *testing.T) {
	// List error: a corrupt definitions file makes reconcile bail early.
	dir := t.TempDir()
	st, _ := NewStoreAt(dir)
	writeFile(t, filepath.Join(dir, schedulesFile), "{bad")
	s := New(st, &fakeRunner{}, &fakeDeliverer{}, Options{})
	s.reconcile(time.Now()) // must not panic; logs and returns
	if len(s.next) != 0 {
		t.Error("reconcile should track nothing when List fails")
	}

	// LoadState error: valid definitions, corrupt state file → reconcile
	// continues with empty state.
	dir2 := t.TempDir()
	st2, _ := NewStoreAt(dir2)
	job := addJob(t, st2, Job{Name: "j", Cron: "* * * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
	writeFile(t, filepath.Join(dir2, stateFile), "{bad")
	s2 := New(st2, &fakeRunner{}, &fakeDeliverer{}, Options{})
	s2.reconcile(time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC))
	if s2.peekNext(job.ID).IsZero() {
		t.Error("reconcile should still schedule the job despite a corrupt state file")
	}
}

func TestReconcile_SkipSaveStateError(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewStoreAt(dir)
	job := addJob(t, st, Job{Name: "j", Cron: "0 9 * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true, Catchup: false})
	past := time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC)
	if err := st.SaveState(RunState{JobID: job.ID, NextRun: past, Sig: jobSig(job)}); err != nil {
		t.Fatal(err)
	}
	// Break state writes so the skip-record persistence fails (logged, not fatal).
	cleanup := makeReadOnly(t, dir)
	defer cleanup()
	s := New(st, &fakeRunner{}, &fakeDeliverer{}, Options{})
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	s.reconcile(now) // exercises the SaveState-error log branch
	if !s.peekNext(job.ID).After(now) {
		t.Error("missed fire should still be forward-scheduled")
	}
}

func TestExecute_SaveStateError(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewStoreAt(dir)
	job := addJob(t, st, Job{Name: "j", Cron: "* * * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
	s := New(st, &fakeRunner{result: "ok"}, &fakeDeliverer{}, Options{})
	t0 := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	s.reconcile(t0)
	// Break state writes; the run still completes, the SaveState error is logged.
	cleanup := makeReadOnly(t, dir)
	defer cleanup()
	s.fireDue(context.Background(), s.peekNext(job.ID))
	s.Wait()
}

func TestFireDue_DropsZeroNextFire(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "j", Cron: "* * * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
	runner := &fakeRunner{}
	s := New(st, runner, &fakeDeliverer{}, Options{})
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	s.reconcile(now)
	// Force a zero next-fire; fireDue must drop it and never run it.
	s.mu.Lock()
	s.next[job.ID] = time.Time{}
	s.mu.Unlock()
	s.fireDue(context.Background(), now)
	s.Wait()
	s.mu.Lock()
	_, tracked := s.next[job.ID]
	s.mu.Unlock()
	if tracked {
		t.Error("a zero next-fire should be dropped")
	}
	if runner.callCount() != 0 {
		t.Error("a zero next-fire must never run")
	}
}

func TestTimeToNext_EmptyAndPast(t *testing.T) {
	s := New(newTestStore(t), &fakeRunner{}, &fakeDeliverer{}, Options{})
	// No jobs → cap at maxSleep.
	if d := s.timeToNext(time.Now()); d != maxSleep {
		t.Errorf("timeToNext with no jobs = %v, want %v", d, maxSleep)
	}
	// A past next-fire → zero (fire immediately).
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	s.mu.Lock()
	s.next["x"] = now.Add(-time.Hour)
	s.mu.Unlock()
	if d := s.timeToNext(now); d != 0 {
		t.Errorf("timeToNext with a past fire = %v, want 0", d)
	}
	// A near-future fire (within maxSleep) → returns the exact remaining delay.
	s.mu.Lock()
	s.next["x"] = now.Add(5 * time.Minute)
	s.mu.Unlock()
	if d := s.timeToNext(now); d != 5*time.Minute {
		t.Errorf("timeToNext with a near fire = %v, want 5m", d)
	}
}

func TestCompile_BadTimezone(t *testing.T) {
	if _, err := compile(Job{Cron: "* * * * *", Timezone: "Mars/Phobos"}, time.UTC); err == nil {
		t.Error("compile should fail for an invalid timezone")
	}
}

func TestPreview_Truncates(t *testing.T) {
	short := "hello"
	if preview(short) != short {
		t.Error("preview should leave short text unchanged")
	}
	long := strings.Repeat("x", resultPreviewRunes+50)
	got := preview(long)
	if !strings.HasSuffix(got, "…") {
		t.Error("preview should append an ellipsis when truncating")
	}
	if len([]rune(got)) != resultPreviewRunes+1 {
		t.Errorf("preview length = %d runes, want %d", len([]rune(got)), resultPreviewRunes+1)
	}
}

func TestRun_ReloadTrigger(t *testing.T) {
	st := newTestStore(t)
	runner := &fakeRunner{result: "ok", started: make(chan string, 1)}
	// Long reload poll so the only way the new job fires promptly is via Reload().
	s := New(st, runner, &fakeDeliverer{}, Options{ReloadEvery: time.Hour})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	time.Sleep(40 * time.Millisecond) // let the initial reconcile run (empty store)

	job := Job{ID: "jb-reload", Name: "r", Cron: "0 0 1 1 *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true, Catchup: true}
	past := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := st.SaveState(RunState{JobID: job.ID, NextRun: past, Sig: jobSig(job)}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add(job); err != nil {
		t.Fatal(err)
	}
	s.Reload() // force an immediate reconcile instead of waiting an hour

	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("Reload() did not trigger a reconcile + catchup fire")
	}
	cancel()
	<-done

	// A Reload with no active Run loop must not block (buffered, coalescing).
	s.Reload()
	s.Reload()
}

func TestRun_ReloadsOnFileChange(t *testing.T) {
	st := newTestStore(t)
	runner := &fakeRunner{result: "ok", started: make(chan string, 1)}
	s := New(st, runner, &fakeDeliverer{}, Options{ReloadEvery: 10 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Let the initial reconcile run against the empty store first, so the new
	// job is only picked up via the reload (mtime-change) path.
	time.Sleep(60 * time.Millisecond)

	job := Job{ID: "jb-reload", Name: "r", Cron: "0 0 1 1 *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true, Catchup: true}
	// Seed a missed past fire with a matching signature so catchup fires it now.
	past := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := st.SaveState(RunState{JobID: job.ID, NextRun: past, Sig: jobSig(job)}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add(job); err != nil { // bumps schedules.json mtime → reload
		t.Fatal(err)
	}

	select {
	case <-runner.started:
		// reload picked up the new job and the catchup fire ran — good.
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("scheduler did not reload and fire the new job")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
