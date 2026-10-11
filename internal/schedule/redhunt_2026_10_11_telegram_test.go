package schedule

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Re-enabling a job that was disabled while the scheduler kept running must
// resume at its next regular slot. The disable left the job's last projected
// NextRun in schedule-state.json; on re-enable reconcile treats that stale
// slot as "missed while down" and, with catchup on, fires the job right away
// even though no fire was missed by a downed process — the operator disabled
// it on purpose.
func TestRED_ReenableWithCatchupFiresImmediately(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "daily", Cron: "0 9 * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true, Catchup: true})

	runner := &fakeRunner{result: "ran"}
	s := New(st, runner, &fakeDeliverer{}, Options{})

	// Day 1 08:00: scheduler running, job projected for 09:00.
	t0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	s.reconcile(t0)

	// 08:30: operator disables the job (scheduler stays up).
	if err := st.SetEnabled(job.ID, false); err != nil {
		t.Fatal(err)
	}
	s.reconcile(t0.Add(30 * time.Minute))

	// Day 3 10:00: operator re-enables it.
	t1 := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	if err := st.SetEnabled(job.ID, true); err != nil {
		t.Fatal(err)
	}
	s.reconcile(t1)
	s.fireDue(context.Background(), t1)
	s.Wait()

	if runner.callCount() != 0 {
		t.Fatalf("re-enabling a disabled job fired it immediately at %v (next regular slot is %v)",
			t1, time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC))
	}
	if want := time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC); !s.peekNext(job.ID).Equal(want) {
		t.Errorf("next after re-enable = %v, want %v", s.peekNext(job.ID), want)
	}
}

// A disabled job that still carries a persisted slot (direct file edit, or a
// run that finished before the scheduler saw the disable) loses it at the
// next reconcile; Put with Enabled=false clears it too.
func TestRED_DisabledJobSlotClearedOnReconcileAndPut(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "daily", Cron: "0 9 * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: false, Catchup: true})
	slot := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	if err := st.SaveState(RunState{JobID: job.ID, NextRun: slot, Runs: 3, Sig: jobSig(job)}); err != nil {
		t.Fatal(err)
	}
	New(st, &fakeRunner{}, &fakeDeliverer{}, Options{}).reconcile(slot.Add(time.Hour))
	state, err := st.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if got := state[job.ID]; !got.NextRun.IsZero() || got.Runs != 3 {
		t.Fatalf("disabled job state after reconcile = %+v, want NextRun cleared and Runs kept", got)
	}

	if err := st.SaveState(RunState{JobID: job.ID, NextRun: slot, Sig: jobSig(job)}); err != nil {
		t.Fatal(err)
	}
	if err := st.Put(job); err != nil {
		t.Fatal(err)
	}
	state, _ = st.LoadState()
	if !state[job.ID].NextRun.IsZero() {
		t.Fatalf("Put of a disabled job kept NextRun %v", state[job.ID].NextRun)
	}
}

// Clearing a disabled job's slot is best-effort: an unreadable state file
// leaves SetEnabled working, and a lock failure is reported, not fatal.
func TestScheduleFix_ClearProjectedNextFailurePaths(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "daily", Cron: "0 9 * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})

	// No state yet: nothing to clear.
	if err := st.SetEnabled(job.ID, false); err != nil {
		t.Fatal(err)
	}
	// Corrupt state file: the clear is skipped, the disable still succeeds.
	if err := os.WriteFile(filepath.Join(st.dir, stateFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEnabled(job.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEnabled(job.ID, false); err != nil {
		t.Fatalf("disable with unreadable state: %v", err)
	}
	if err := os.Remove(filepath.Join(st.dir, stateFile)); err != nil {
		t.Fatal(err)
	}

	// Lock failure: reported by clearProjectedNext and logged by reconcile.
	if err := st.SaveState(RunState{JobID: job.ID, NextRun: time.Now()}); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(st.dir, "schedules.lock")
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := st.clearProjectedNext(job.ID); err == nil {
		t.Fatal("clearProjectedNext with an unusable lock file: want error")
	}
	New(st, &fakeRunner{}, &fakeDeliverer{}, Options{}).reconcile(time.Now())
	if state, _ := st.LoadState(); state[job.ID].NextRun.IsZero() {
		t.Error("slot cleared despite the lock failure")
	}
}

// A failed definition write is reported by SetEnabled and Put and leaves the
// persisted slot alone.
func TestScheduleFix_DisableWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced for root")
	}
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "daily", Cron: "0 9 * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
	if err := os.Chmod(st.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(st.dir, 0o700) })
	if err := st.SetEnabled(job.ID, false); err == nil {
		t.Error("SetEnabled on a read-only store: want error")
	}
	job.Enabled = false
	if err := st.Put(job); err == nil {
		t.Error("Put on a read-only store: want error")
	}
}

// A job disabled through the store while no scheduler is running (CLI or
// Telegram while the daemon is down) and re-enabled later must not be treated
// as having missed a fire either.
func TestRED_ReenableAfterOfflineDisableDoesNotCatchUp(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "daily", Cron: "0 9 * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true, Catchup: true})

	t0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	New(st, &fakeRunner{}, &fakeDeliverer{}, Options{}).reconcile(t0) // projects 09:00, then the daemon stops

	if err := st.SetEnabled(job.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEnabled(job.ID, true); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{result: "ran"}
	s := New(st, runner, &fakeDeliverer{}, Options{})
	t1 := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	s.reconcile(t1)
	s.fireDue(context.Background(), t1)
	s.Wait()
	if runner.callCount() != 0 {
		t.Fatalf("job disabled and re-enabled while the daemon was down fired immediately at %v", t1)
	}

	// A plain restart (no disable) still catches up a missed fire.
	st2 := newTestStore(t)
	job2 := addJob(t, st2, Job{Name: "daily", Cron: "0 9 * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true, Catchup: true})
	New(st2, &fakeRunner{}, &fakeDeliverer{}, Options{}).reconcile(t0)
	s2 := New(st2, &fakeRunner{}, &fakeDeliverer{}, Options{})
	s2.reconcile(t1)
	if !s2.peekNext(job2.ID).Equal(t1) {
		t.Errorf("missed fire after a restart: next = %v, want catch-up at %v", s2.peekNext(job2.ID), t1)
	}
}
