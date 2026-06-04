package schedule

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── Test doubles ────────────────────────────────────────────────────────

type fakeRunner struct {
	mu      sync.Mutex
	calls   []Job
	result  string
	err     error
	block   chan struct{} // if non-nil, Run blocks on it (simulate a slow job)
	started chan string   // if non-nil, receives job ID when Run begins
}

func (f *fakeRunner) Run(_ context.Context, job Job) (string, int64, error) {
	f.mu.Lock()
	f.calls = append(f.calls, job)
	f.mu.Unlock()
	if f.started != nil {
		f.started <- job.ID
	}
	if f.block != nil {
		<-f.block
	}
	return f.result, 7, f.err
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeDeliverer struct {
	mu        sync.Mutex
	delivered []string
	err       error
}

func (f *fakeDeliverer) Deliver(_ Job, result string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.delivered = append(f.delivered, result)
	return nil
}

// peekNext exposes a job's scheduled next-fire for assertions.
func (s *Scheduler) peekNext(id string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next[id]
}

// addJob is a helper that adds a job and returns its stored copy.
func addJob(t *testing.T, st *Store, j Job) Job {
	t.Helper()
	got, err := st.Add(j)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return got
}

// ── fireDue ─────────────────────────────────────────────────────────────

func TestFireDue_RunsAndDelivers(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "j", Cron: "* * * * *", Task: "do it",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})

	runner := &fakeRunner{result: "hello"}
	deliv := &fakeDeliverer{}
	s := New(st, runner, deliv, Options{})

	t0 := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	s.reconcile(t0)

	// Not due yet (next is strictly after t0).
	s.fireDue(context.Background(), t0)
	s.Wait()
	if runner.callCount() != 0 {
		t.Fatalf("fired before due: %d calls", runner.callCount())
	}

	// Fire at the scheduled instant.
	due := s.peekNext(job.ID)
	s.fireDue(context.Background(), due)
	s.Wait()

	if runner.callCount() != 1 {
		t.Fatalf("expected 1 run, got %d", runner.callCount())
	}
	if len(deliv.delivered) != 1 || deliv.delivered[0] != "hello" {
		t.Fatalf("delivered = %v", deliv.delivered)
	}
	state, _ := st.LoadState()
	if state[job.ID].LastStatus != StatusOK {
		t.Errorf("status = %q, want ok", state[job.ID].LastStatus)
	}
	if state[job.ID].Runs != 1 {
		t.Errorf("runs = %d, want 1", state[job.ID].Runs)
	}
	if state[job.ID].LastResult != "hello" {
		t.Errorf("LastResult = %q", state[job.ID].LastResult)
	}
	// next must have advanced strictly past the fire instant.
	if !s.peekNext(job.ID).After(due) {
		t.Errorf("next did not advance after fire")
	}
}

func TestFireDue_RunnerError(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "j", Cron: "* * * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
	runner := &fakeRunner{err: context.DeadlineExceeded}
	deliv := &fakeDeliverer{}
	s := New(st, runner, deliv, Options{})

	t0 := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	s.reconcile(t0)
	s.fireDue(context.Background(), s.peekNext(job.ID))
	s.Wait()

	if len(deliv.delivered) != 0 {
		t.Error("delivery should not happen when runner errored")
	}
	state, _ := st.LoadState()
	if state[job.ID].LastStatus != StatusError || state[job.ID].LastError == "" {
		t.Errorf("expected error state, got %+v", state[job.ID])
	}
}

func TestFireDue_DeliveryError(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "j", Cron: "* * * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
	runner := &fakeRunner{result: "ok"}
	deliv := &fakeDeliverer{err: context.Canceled}
	s := New(st, runner, deliv, Options{})

	t0 := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	s.reconcile(t0)
	s.fireDue(context.Background(), s.peekNext(job.ID))
	s.Wait()

	state, _ := st.LoadState()
	if state[job.ID].LastStatus != StatusError {
		t.Errorf("status = %q, want error", state[job.ID].LastStatus)
	}
	if !strings.HasPrefix(state[job.ID].LastError, "delivery:") {
		t.Errorf("LastError = %q, want delivery: prefix", state[job.ID].LastError)
	}
}

func TestFireDue_OverlapGuard(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "slow", Cron: "* * * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
	runner := &fakeRunner{result: "ok", block: make(chan struct{}), started: make(chan string, 1)}
	s := New(st, runner, &fakeDeliverer{}, Options{})

	t0 := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	s.reconcile(t0)

	// First fire starts and blocks inside the runner.
	due1 := s.peekNext(job.ID)
	s.fireDue(context.Background(), due1)
	<-runner.started // ensure it's in flight

	// A second due fire while the first is still running must be skipped.
	due2 := s.peekNext(job.ID)
	s.fireDue(context.Background(), due2)
	if c := runner.callCount(); c != 1 {
		t.Fatalf("overlap guard failed: %d concurrent runs", c)
	}

	close(runner.block) // let the first finish
	s.Wait()
	if c := runner.callCount(); c != 1 {
		t.Errorf("expected exactly 1 run total, got %d", c)
	}
}

// ── Missed-run policy ───────────────────────────────────────────────────

func TestReconcile_MissedSkip(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "j", Cron: "0 9 * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true, Catchup: false})
	// Pretend a fire was due in the past while we were down.
	past := time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC)
	_ = st.SaveState(RunState{JobID: job.ID, NextRun: past, Sig: jobSig(job)})

	runner := &fakeRunner{}
	s := New(st, runner, &fakeDeliverer{}, Options{})
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	s.reconcile(now)

	// Next must be in the future (forward-scheduled), not the missed instant.
	if !s.peekNext(job.ID).After(now) {
		t.Errorf("missed fire not skipped forward: next=%v", s.peekNext(job.ID))
	}
	s.fireDue(context.Background(), now)
	s.Wait()
	if runner.callCount() != 0 {
		t.Error("missed fire should not run when catchup is off")
	}
	state, _ := st.LoadState()
	if state[job.ID].LastStatus != StatusSkipped {
		t.Errorf("status = %q, want skipped", state[job.ID].LastStatus)
	}
}

func TestReconcile_CronChangedWhileDownNotMissed(t *testing.T) {
	// Persisted NextRun was produced by an OLD cron (different sig). On restart
	// the job's cron has changed; the stale slot must NOT trigger a catchup.
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "j", Cron: "0 9 * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true, Catchup: true})
	// Seed state from a DIFFERENT schedule signature, with a past NextRun.
	past := time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC)
	_ = st.SaveState(RunState{JobID: job.ID, NextRun: past, Sig: "OLD|"})

	runner := &fakeRunner{}
	s := New(st, runner, &fakeDeliverer{}, Options{})
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	s.reconcile(now)

	// The stale (different-sig) slot is ignored → next is forward-scheduled, no fire.
	if !s.peekNext(job.ID).After(now) {
		t.Errorf("stale-sig NextRun should be ignored; next=%v", s.peekNext(job.ID))
	}
	s.fireDue(context.Background(), now)
	s.Wait()
	if runner.callCount() != 0 {
		t.Error("cron changed while down → must NOT catchup-fire on the old slot")
	}
}

func TestFireDue_CancelDuringDispatchUnblocks(t *testing.T) {
	// With all slots held by a blocked job, a cancelled ctx must let fireDue
	// return instead of wedging on the semaphore, and undispatched jobs must
	// have their overlap guard cleared.
	st := newTestStore(t)
	a := addJob(t, st, Job{Name: "a", Cron: "* * * * *", Task: "x", Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
	b := addJob(t, st, Job{Name: "b", Cron: "* * * * *", Task: "y", Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
	runner := &fakeRunner{block: make(chan struct{}), started: make(chan string, 2)}
	s := New(st, runner, &fakeDeliverer{}, Options{MaxConcurrent: 1}) // one slot

	t0 := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	s.reconcile(t0)
	due := s.peekNext(a.ID) // both share the same next minute

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.fireDue(ctx, due); close(done) }()

	<-runner.started // first job took the only slot and is blocked
	cancel()         // cancel while the second job can't acquire a slot

	select {
	case <-done:
		// fireDue returned despite the full semaphore — good.
	case <-time.After(2 * time.Second):
		close(runner.block)
		t.Fatal("fireDue wedged on the semaphore after ctx cancel")
	}
	close(runner.block)
	s.Wait()
	_ = b
}

func TestReconcile_MissedCatchup(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "j", Cron: "0 9 * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true, Catchup: true})
	past := time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC)
	_ = st.SaveState(RunState{JobID: job.ID, NextRun: past, Sig: jobSig(job)})

	runner := &fakeRunner{result: "caught up"}
	deliv := &fakeDeliverer{}
	s := New(st, runner, deliv, Options{})
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	s.reconcile(now)

	// Catchup schedules an immediate fire.
	if s.peekNext(job.ID).After(now) {
		t.Fatalf("catchup did not schedule an immediate fire: next=%v", s.peekNext(job.ID))
	}
	s.fireDue(context.Background(), now)
	s.Wait()
	if runner.callCount() != 1 {
		t.Errorf("catchup did not run the missed job: %d calls", runner.callCount())
	}
}

// ── Reconcile lifecycle ─────────────────────────────────────────────────

func TestReconcile_DropsDisabled(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "j", Cron: "* * * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
	s := New(st, &fakeRunner{}, &fakeDeliverer{}, Options{})
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	s.reconcile(now)
	if s.peekNext(job.ID).IsZero() {
		t.Fatal("job not tracked after reconcile")
	}
	if err := st.SetEnabled(job.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	s.reconcile(now)
	if !s.peekNext(job.ID).IsZero() {
		t.Error("disabled job not dropped from schedule")
	}
}

func TestReconcile_UnchangedKeepsNextFire(t *testing.T) {
	st := newTestStore(t)
	a := addJob(t, st, Job{Name: "a", Cron: "0 9 * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
	s := New(st, &fakeRunner{}, &fakeDeliverer{}, Options{})
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	s.reconcile(now)
	firstNext := s.peekNext(a.ID)

	// Adding an unrelated job and reconciling again must not shift a's fire.
	addJob(t, st, Job{Name: "b", Cron: "0 10 * * *", Task: "y",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
	s.reconcile(now.Add(time.Minute))
	if !s.peekNext(a.ID).Equal(firstNext) {
		t.Errorf("unrelated reconcile shifted job a: %v != %v", s.peekNext(a.ID), firstNext)
	}
}

func TestReconcile_SkipsInvalidJobWrittenDirectly(t *testing.T) {
	// A malformed job that bypassed Validate (e.g. hand-edited file) must be
	// skipped without aborting the reconcile of healthy jobs.
	st := newTestStore(t)
	good := addJob(t, st, Job{Name: "good", Cron: "* * * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
	// Write a bad job directly into the doc, sidestepping Add's validation.
	doc, _ := st.loadDoc()
	doc.Jobs = append(doc.Jobs, Job{ID: "jb-bad", Name: "bad", Cron: "not-a-cron",
		Task: "x", Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
	_ = st.saveDoc(doc)

	s := New(st, &fakeRunner{}, &fakeDeliverer{}, Options{})
	s.reconcile(time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC))
	if s.peekNext(good.ID).IsZero() {
		t.Error("good job dropped because a sibling was invalid")
	}
	if !s.peekNext("jb-bad").IsZero() {
		t.Error("invalid job should not be scheduled")
	}
}

func TestReconcile_JobTimezone(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("tz data unavailable: %v", err)
	}
	st := newTestStore(t)
	// Daily 09:00 Berlin; the engine's DefaultTZ is UTC, so the job's own
	// Timezone must win when compiling.
	job := addJob(t, st, Job{Name: "tz", Cron: "0 9 * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true, Timezone: "Europe/Berlin"})
	s := New(st, &fakeRunner{}, &fakeDeliverer{}, Options{DefaultTZ: time.UTC})

	// 06:00 UTC = 08:00 Berlin (CEST) → next fire is 09:00 Berlin today.
	now := time.Date(2026, 6, 4, 6, 0, 0, 0, time.UTC)
	s.reconcile(now)
	got := s.peekNext(job.ID)
	want := time.Date(2026, 6, 4, 9, 0, 0, 0, berlin)
	if !got.Equal(want) {
		t.Errorf("next = %v, want %v (job timezone must override DefaultTZ)", got.In(berlin), want)
	}
}

// ── Run lifecycle ───────────────────────────────────────────────────────

func TestRun_FiresThenStopsCleanly(t *testing.T) {
	st := newTestStore(t)
	// Far-future cron, but a missed catchup fire forces an immediate run on
	// startup — so we exercise Run's real loop without waiting a wall minute.
	job := addJob(t, st, Job{Name: "j", Cron: "0 0 1 1 *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true, Catchup: true})
	_ = st.SaveState(RunState{JobID: job.ID, NextRun: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), Sig: jobSig(job)})

	runner := &fakeRunner{result: "ok", started: make(chan string, 1)}
	s := New(st, runner, &fakeDeliverer{}, Options{ReloadEvery: 20 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	select {
	case <-runner.started:
		// fired as expected
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("scheduler did not fire the catchup job")
	}

	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if runner.callCount() != 1 {
		t.Errorf("expected 1 run, got %d", runner.callCount())
	}
}
