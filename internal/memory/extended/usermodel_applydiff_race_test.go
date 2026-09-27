package extended

import (
	"testing"
)

// TestApplyDiffDoesNotClobberOtherProcessUpdates verifies that a process
// with stale in-memory user-model state cannot erase sections another
// process persisted while it was idle: applyDiff must start from the
// on-disk state, not from its own stale copy.
func TestApplyDiffDoesNotClobberOtherProcessUpdates(t *testing.T) {
	dir := t.TempDir()

	// Process B starts (loads empty disk state), then goes idle.
	b := NewUserModelWithStore(dir, nil, DefaultConfig())

	// Process A persists a focus update.
	a := NewUserModelWithStore(dir, nil, DefaultConfig())
	if err := a.applyDiff(t.Context(), userStateDiff{
		Focus: &FocusState{Project: "odek", Task: "ship v2"},
	}); err != nil {
		t.Fatalf("process A applyDiff: %v", err)
	}
	if err := a.Save(); err != nil {
		t.Fatalf("process A save: %v", err)
	}

	// Process B (stale in-memory copy without A's focus) applies a style diff.
	if err := b.applyDiff(t.Context(), userStateDiff{
		Style: &StyleState{Tone: "dry"},
	}); err != nil {
		t.Fatalf("process B applyDiff: %v", err)
	}

	// A's focus must survive B's save.
	disk, err := NewUserModelWithStore(dir, nil, DefaultConfig()).store.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if disk.CurrentFocus.Project != "odek" {
		t.Fatalf("process B's applyDiff clobbered process A's focus: %+v", disk.CurrentFocus)
	}
	if disk.Style.Tone != "dry" {
		t.Fatalf("process B's style diff missing after save: %+v", disk.Style)
	}
}
