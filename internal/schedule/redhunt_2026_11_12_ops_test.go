package schedule

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/redact"
)

// opsSecret builds a GitHub-token-shaped credential the redactor recognises.
func opsSecret(fill byte) string {
	return "ghp_" + strings.Repeat(string(fill), 36)
}

// TestRED_RunStatePersistsResultAndErrorsUnredacted: a scheduled run's
// result preview (LastResult) and failure text (LastError) are written to
// schedule-state.json verbatim. That state is served by GET /api/schedules
// and rendered by the Web UI ("Last run result") and Telegram /schedule view,
// while every delivery sink (stdout, log, Telegram) redacts the same result.
// The persisted state must not carry a credential the run echoed, including
// a fragment left behind when the 280-rune preview cuts a secret in half.
func TestRED_RunStatePersistsResultAndErrorsUnredacted(t *testing.T) {
	whole := opsSecret('A')
	straddling := opsSecret('B')
	for _, s := range []string{whole, straddling} {
		if got := redact.RedactSecrets("token " + s); strings.Contains(got, s) {
			t.Fatalf("precondition: redactor does not recognise %q", s)
		}
	}

	// Result: one whole secret near the start, a second one that begins a
	// few runes before the 280-rune preview cut.
	head := "Deploy key rotated. New token: " + whole + " ."
	pad := strings.Repeat("x", resultPreviewRunes-len([]rune(head))-10)
	result := head + pad + " " + straddling + " trailing text"

	t0 := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)

	t.Run("result", func(t *testing.T) {
		st := newTestStore(t)
		job := addJob(t, st, Job{Name: "rotate", Cron: "* * * * *", Task: "rotate the deploy key",
			Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
		s := New(st, &fakeRunner{result: result}, &fakeDeliverer{}, Options{})
		s.reconcile(t0)
		s.fireDue(context.Background(), s.peekNext(job.ID))
		s.Wait()
		state, err := st.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		got := state[job.ID].LastResult
		if state[job.ID].LastStatus != StatusOK || got == "" {
			t.Fatalf("precondition: run did not record a result: %+v", state[job.ID])
		}
		if strings.Contains(got, whole) {
			t.Errorf("LastResult persists the credential verbatim: %q", got)
		}
		if frag := straddling[:5]; strings.Contains(got, frag) {
			t.Errorf("LastResult persists a prefix of a credential cut by the preview (%q): %q", frag, got)
		}
	})

	t.Run("run_error", func(t *testing.T) {
		st := newTestStore(t)
		job := addJob(t, st, Job{Name: "rotate", Cron: "* * * * *", Task: "rotate the deploy key",
			Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
		runErr := errors.New("provider rejected request using token " + whole)
		s := New(st, &fakeRunner{err: runErr}, &fakeDeliverer{}, Options{})
		s.reconcile(t0)
		s.fireDue(context.Background(), s.peekNext(job.ID))
		s.Wait()
		state, _ := st.LoadState()
		if state[job.ID].LastStatus != StatusError {
			t.Fatalf("precondition: expected error status: %+v", state[job.ID])
		}
		if strings.Contains(state[job.ID].LastError, whole) {
			t.Errorf("LastError persists the credential verbatim: %q", state[job.ID].LastError)
		}
	})

	t.Run("delivery_error", func(t *testing.T) {
		st := newTestStore(t)
		job := addJob(t, st, Job{Name: "rotate", Cron: "* * * * *", Task: "rotate the deploy key",
			Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
		delErr := errors.New("POST https://hooks.example/notify?token=" + whole + ": 502")
		s := New(st, &fakeRunner{result: "ok"}, &fakeDeliverer{err: delErr}, Options{})
		s.reconcile(t0)
		s.fireDue(context.Background(), s.peekNext(job.ID))
		s.Wait()
		state, _ := st.LoadState()
		if state[job.ID].LastStatus != StatusError {
			t.Fatalf("precondition: expected error status: %+v", state[job.ID])
		}
		if strings.Contains(state[job.ID].LastError, whole) {
			t.Errorf("LastError persists the delivery credential verbatim: %q", state[job.ID].LastError)
		}
	})
}

// State files written by earlier versions may hold unredacted text; reading
// them redacts what the API, Web UI and Telegram would display.
func TestOpsLoadStateRedactsLegacyText(t *testing.T) {
	st := newTestStore(t)
	job := addJob(t, st, Job{Name: "c", Cron: "0 9 * * *", Task: "x", Deliver: Delivery{Kind: DeliverStdout}, Enabled: true})
	tok := opsSecret('C')
	if err := st.SaveState(RunState{JobID: job.ID, LastResult: "key " + tok, LastError: "bad " + tok}); err != nil {
		t.Fatal(err)
	}
	state, err := st.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(state[job.ID].LastResult, tok) || strings.Contains(state[job.ID].LastError, tok) {
		t.Fatalf("LoadState returned an unredacted credential: %+v", state[job.ID])
	}
}
