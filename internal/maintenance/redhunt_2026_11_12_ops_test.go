package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/flock"
	"github.com/BackendStack21/odek/internal/session"
)

// opsAgedSession creates a session through the store and backdates its
// updated_at (file and index) to age.
func opsAgedSession(t *testing.T, store *session.Store, task string, age time.Duration) *session.Session {
	t.Helper()
	sess, err := store.Create([]session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: task}}, "test-model", task)
	if err != nil {
		t.Fatal(err)
	}
	sess.UpdatedAt = time.Now().Add(-age).UTC()
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	return sess
}

// TestRED_SessionSweepDeletesSessionWithLiveRun: retention deletes sessions
// by updated_at, but a session whose turn is executing right now (its
// execution lease is held — `odek continue`, a REPL or Web UI turn resumed on
// a conversation older than sessions_max_age_days) still carries the old
// updated_at until the turn's first step persists. A janitor tick in that
// window (the janitor runs hourly inside serve, telegram and the schedule
// daemon, i.e. in another process) removes the session file under the live
// run: the run's next per-step save fails with "cannot update removed
// session", so both the resumed transcript and the in-flight turn are lost.
// The sweep must leave a session alone while its execution lease is held.
func TestRED_SessionSweepDeletesSessionWithLiveRun(t *testing.T) {
	home := t.TempDir()
	store, err := session.NewStoreWithDir(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	const age = 45 * 24 * time.Hour
	live := opsAgedSession(t, store, "resumed conversation", age)
	idle := opsAgedSession(t, store, "abandoned conversation", age)

	// A turn starts on the old conversation: the runner holds the lease and
	// has loaded the session (updated_at is still 45 days old).
	release, err := store.AcquireExecution(context.Background(), live.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// Precondition: the lease is observable through a separate open of the
	// lock file, as the janitor (another store instance) would see it.
	if r, err := flock.TryLock(filepath.Join(home, "sessions", "."+live.ID+".run.lock")); !errors.Is(err, flock.ErrLocked) {
		if r != nil {
			r()
		}
		t.Fatalf("precondition: held execution lease not visible to a second opener: %v", err)
	}
	running, err := store.Load(live.ID)
	if err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.AuditMaxAgeDays, cfg.PlansMaxAgeDays, cfg.ArtifactsMaxAgeHours = 0, 0, 0
	rep, err := Sweep(context.Background(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Precondition: the retention rule itself applies — the idle aged
	// session is removed.
	if _, err := os.Stat(store.Path(idle.ID)); !os.IsNotExist(err) {
		t.Fatalf("precondition: idle aged session not swept (removed=%d, stat err=%v)", rep.SessionsRemoved, err)
	}

	if _, err := os.Stat(store.Path(live.ID)); err != nil {
		t.Errorf("janitor deleted a session whose execution lease is held by a running turn: %v", err)
	}
	// The running turn persists its first completed step.
	running.Messages = append(running.Messages, session.Message{Role: "assistant", Content: "step one done"})
	if err := store.SaveNoIndex(running); err != nil {
		t.Errorf("the running turn can no longer persist its progress after the sweep: %v", err)
	}
}
