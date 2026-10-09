package schedule

import (
	"testing"
	"time"
)

// Recording a skipped (missed, no catchup) fire must not erase the job's real
// last run: LastRun/LastResult describe the last time it actually ran.
func TestRED_SkipRecordClobbersLastRun(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "j", Cron: "0 9 * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
	realRun := time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)
	missed := time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC)
	_ = st.SaveState(RunState{JobID: job.ID, LastRun: realRun, LastStatus: StatusOK,
		LastResult: "report", NextRun: missed, Sig: jobSig(job), Runs: 1})

	s := New(st, &fakeRunner{}, &fakeDeliverer{}, Options{})
	s.reconcile(time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC))

	state, _ := st.LoadState()
	got := state[job.ID]
	if !got.LastRun.Equal(realRun) {
		t.Fatalf("LastRun = %v after a skip; want the real last run %v", got.LastRun, realRun)
	}
	if got.LastResult != "report" {
		t.Fatalf("LastResult = %q after a skip; real result was erased", got.LastResult)
	}
}

func TestRED_SkipRecordClearsLastErrorAndStampsSkippedAt(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "j", Cron: "0 9 * * *", Task: "x",
		Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
	realRun := time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)
	_ = st.SaveState(RunState{JobID: job.ID, LastRun: realRun, LastStatus: StatusError,
		LastError: "boom", NextRun: time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC), Sig: jobSig(job), Runs: 3})

	s := New(st, &fakeRunner{}, &fakeDeliverer{}, Options{})
	skipAt := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	s.reconcile(skipAt)

	state, _ := st.LoadState()
	got := state[job.ID]
	if !got.SkippedAt.Equal(skipAt) {
		t.Fatalf("SkippedAt = %v, want %v", got.SkippedAt, skipAt)
	}
	if got.LastStatus != StatusSkipped || got.LastError != "" || !got.LastRun.Equal(realRun) || got.Runs != 3 {
		t.Fatalf("unexpected state after skip: %+v", got)
	}
	if !got.NextRun.After(time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("NextRun not advanced: %v", got.NextRun)
	}
}
