package main

// TDD RED phase — M3 (SUBAGENT_RESULT_ARTIFACTS_PLAN.md): staging +
// relocation, artifact counts on the completed event.
//
// The canonical artifact dir (~/.odek/artifacts) is doubly protected from
// child writes: confineToCWD rejects absolute paths, and the danger
// classifier escalates any ~/.odek write to system_write (denied for
// approval-less children). Children therefore stage deliverables INSIDE
// the workspace (.odek-artifacts/<task_id>/ — local_write, allowed), and
// the trusted child runner relocates them to the canonical dir before the
// exit scan. Wire format unchanged: artifact_root still names the
// canonical dir (v1.32.0 compatible).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStagingDirFor(t *testing.T) {
	cwd := t.TempDir()
	got := stagingDirFor(cwd, "task-abc")
	want := filepath.Join(cwd, ".odek-artifacts", "task-abc")
	if got != want {
		t.Errorf("stagingDirFor = %q, want %q", got, want)
	}
}

func TestRelocateStagingArtifacts_MovesFiles(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, ".odek-artifacts", "task-1")
	canonical := filepath.Join(root, "canonical", "task-1")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	writeArtifactFile(t, staging, "report.md", "# report")
	writeArtifactFile(t, staging, "data.json", "{}")
	if err := os.Mkdir(filepath.Join(staging, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeArtifactFile(t, filepath.Join(staging, "sub"), "nested.txt", "nested")

	n, err := relocateStagingArtifacts(staging, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("want 2 top-level files relocated, got %d", n)
	}
	for _, f := range []string{"report.md", "data.json"} {
		if _, err := os.Stat(filepath.Join(canonical, f)); err != nil {
			t.Errorf("%s missing in canonical: %v", f, err)
		}
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Error("staging dir must be removed after relocation")
	}
}

func TestRelocateStagingArtifacts_CopyFallback(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	canonical := filepath.Join(root, "canonical")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	writeArtifactFile(t, staging, "report.md", "payload")

	old := renameFailureHook
	renameFailureHook = func() bool { return true } // force the copy path
	defer func() { renameFailureHook = old }()

	n, err := relocateStagingArtifacts(staging, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 file copied, got %d", n)
	}
	b, err := os.ReadFile(filepath.Join(canonical, "report.md"))
	if err != nil || string(b) != "payload" {
		t.Errorf("copy fallback lost content: %q (%v)", b, err)
	}
}

func TestRelocateStagingArtifacts_MissingStagingNoop(t *testing.T) {
	root := t.TempDir()
	n, err := relocateStagingArtifacts(filepath.Join(root, "nope"), filepath.Join(root, "canonical"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("missing staging must be a no-op, got %d", n)
	}
}

func TestSubagentCompletedEvent_ArtifactCount(t *testing.T) {
	ev := subagentCompletedEvent("t1", map[string]any{
		"status":    "success",
		"artifacts": []any{map[string]any{"id": "a"}, map[string]any{"id": "b"}},
	}, "")
	if got, ok := ev.Data["artifact_count"].(int); !ok || got != 2 {
		t.Errorf("artifact_count = %v (%T), want 2", ev.Data["artifact_count"], ev.Data["artifact_count"])
	}

	ev = subagentCompletedEvent("t2", map[string]any{"status": "error"}, "error")
	if _, ok := ev.Data["artifact_count"]; ok {
		t.Error("artifact_count must be absent when the child reported none")
	}
}

func TestChildArtifactNote_RelativeStagingPath(t *testing.T) {
	// The instruction shown to the child must use the workspace-relative
	// staging path, never the canonical host path.
	note := childArtifactNote(".odek-artifacts/task-abc")
	if !strings.Contains(note, ".odek-artifacts/task-abc") {
		t.Errorf("note missing relative staging path: %s", note)
	}
	if strings.Contains(note, "/.odek/") {
		t.Errorf("note must not leak the canonical host path: %s", note)
	}
}
