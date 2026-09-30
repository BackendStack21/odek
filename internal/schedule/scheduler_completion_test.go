package schedule

import (
	"context"
	"testing"
	"time"
)

func TestRegression_OverlapKeepsPersistedNextRunCurrent(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "slow", Cron: "* * * * *", Task: "x", Enabled: true, Catchup: true, Deliver: Delivery{Kind: DeliverStdout}})
	runner := &fakeRunner{result: "ok", block: make(chan struct{}), started: make(chan string, 1)}
	s := New(st, runner, &fakeDeliverer{}, Options{})
	t0 := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	s.reconcile(t0)
	s.fireDue(context.Background(), s.peekNext(job.ID))
	<-runner.started
	skipped := s.peekNext(job.ID)
	s.fireDue(context.Background(), skipped)
	want := s.peekNext(job.ID)
	close(runner.block)
	s.Wait()
	state, err := st.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if got := state[job.ID].NextRun; !got.Equal(want) {
		t.Errorf("completed job persisted NextRun %s; scheduler had already advanced to %s", got, want)
	}
	restarted := New(st, &fakeRunner{}, &fakeDeliverer{}, Options{})
	restarted.reconcile(skipped.Add(30 * time.Second))
	if got := restarted.peekNext(job.ID); !got.Equal(want) {
		t.Errorf("restart invents a catchup at %s for a fire skipped while running; want %s", got, want)
	}
}

func TestRegression_CronEditSurvivesInflightCompletion(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "slow", Cron: "* * * * *", Task: "x", Enabled: true, Deliver: Delivery{Kind: DeliverStdout}})
	runner := &fakeRunner{result: "ok", block: make(chan struct{}), started: make(chan string, 1)}
	s := New(st, runner, &fakeDeliverer{}, Options{})
	t0 := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	s.reconcile(t0)
	s.fireDue(context.Background(), s.peekNext(job.ID))
	<-runner.started
	job.Cron = "0 12 * * *"
	if err := st.Put(job); err != nil {
		close(runner.block)
		s.Wait()
		t.Fatal(err)
	}
	s.reconcile(t0.Add(90 * time.Second))
	want := s.peekNext(job.ID)
	close(runner.block)
	s.Wait()
	state, err := st.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if got := state[job.ID]; got.Sig != jobSig(job) || !got.NextRun.Equal(want) {
		t.Fatalf("completion overwrote edited schedule: sig=%q next=%s; want sig=%q next=%s", got.Sig, got.NextRun, jobSig(job), want)
	}
}

func TestCompletionDoesNotRestoreRemovedJobState(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "slow", Cron: "* * * * *", Task: "x", Enabled: true, Deliver: Delivery{Kind: DeliverStdout}})
	runner := &fakeRunner{result: "ok", block: make(chan struct{}), started: make(chan string, 1)}
	s := New(st, runner, &fakeDeliverer{}, Options{})
	s.reconcile(time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC))
	s.fireDue(t.Context(), s.peekNext(job.ID))
	<-runner.started
	if err := st.Remove(job.ID); err != nil {
		close(runner.block)
		s.Wait()
		t.Fatal(err)
	}
	// Completion must respect deletion even before the next daemon reload.
	close(runner.block)
	s.Wait()
	state, err := st.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state[job.ID]; ok {
		t.Fatal("completion restored deleted job state")
	}
}

func TestCompletionKeepsDisabledJobHistoryWithoutNextRun(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "slow", Cron: "* * * * *", Task: "x", Enabled: true, Deliver: Delivery{Kind: DeliverStdout}})
	runner := &fakeRunner{result: "ok", block: make(chan struct{}), started: make(chan string, 1)}
	s := New(st, runner, &fakeDeliverer{}, Options{})
	t0 := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	s.reconcile(t0)
	s.fireDue(t.Context(), s.peekNext(job.ID))
	<-runner.started
	if err := st.SetEnabled(job.ID, false); err != nil {
		close(runner.block)
		s.Wait()
		t.Fatal(err)
	}
	s.reconcile(t0.Add(90 * time.Second))
	close(runner.block)
	s.Wait()
	state, err := st.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	got := state[job.ID]
	if got.LastResult != "ok" || got.Runs != 1 || !got.NextRun.IsZero() {
		t.Fatalf("disabled job lost history or kept a scheduled fire: %+v", got)
	}
}
