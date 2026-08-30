package main

// Registry of validated sub-agent result artifacts (M2). delegate_tasks
// registers every ref that passed fail-closed validation at collation time,
// together with the validated path; artifact_read resolves ids against this
// registry. The model supplies only the id — paths never cross the model
// boundary in either direction. Same pattern as subagentCtl: process-global,
// mutex-guarded, bounded.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/artifact"
)

const (
	// artifactRegistryCap bounds the LIVE registry; oldest entries evict
	// first. 8 tasks × 64 refs is the worst single delegate_tasks call, so
	// 512 covers a session's recent history comfortably.
	artifactRegistryCap = 512
	// maxListedArtifactIDs bounds the available-id list on unknown-id errors.
	maxListedArtifactIDs = 16
)

// artifactEntry is one validated artifact: the ref as rendered, plus the
// validated (symlink-resolved) path captured at collation time. seq is a
// monotonic registration counter used for lazy queue eviction.
type artifactEntry struct {
	Ref          artifact.Ref
	Path         string
	TaskIdx      int
	RegisteredAt time.Time
	seq          uint64
}

// registrySlot pairs an id with the seq that inserted it; stale slots
// (superseded by a last-wins re-registration) are skipped at eviction.
type registrySlot struct {
	id  string
	seq uint64
}

var artifactRegistry struct {
	mu    sync.Mutex
	byID  map[string]*artifactEntry
	order []registrySlot
	seq   uint64
}

func init() {
	resetArtifactRegistryForTest()
}

// resetArtifactRegistryForTest clears the registry. Tests only — production
// code relies on eviction, never on a reset.
func resetArtifactRegistryForTest() {
	artifactRegistry.mu.Lock()
	defer artifactRegistry.mu.Unlock()
	artifactRegistry.byID = map[string]*artifactEntry{}
	artifactRegistry.order = nil
	artifactRegistry.seq = 0
}

// registerSubagentArtifact records a validated artifact under its ref id.
// Last-wins on duplicate ids (a later task overwrites an earlier one);
// returns true when the id was already present so the caller can flag the
// ambiguity in the collated summary. Evicts the oldest LIVE entry at cap;
// superseded queue slots are skipped lazily.
func registerSubagentArtifact(e artifactEntry) bool {
	if e.Ref.ID == "" || e.Path == "" {
		return false
	}

	artifactRegistry.mu.Lock()
	defer artifactRegistry.mu.Unlock()
	artifactRegistry.seq++
	e.seq = artifactRegistry.seq
	if artifactRegistry.byID == nil {
		artifactRegistry.byID = map[string]*artifactEntry{}
	}
	_, dup := artifactRegistry.byID[e.Ref.ID]
	artifactRegistry.byID[e.Ref.ID] = &e
	artifactRegistry.order = append(artifactRegistry.order, registrySlot{id: e.Ref.ID, seq: e.seq})

	for len(artifactRegistry.order) > artifactRegistryCap {
		front := artifactRegistry.order[0]
		artifactRegistry.order = artifactRegistry.order[1:]
		// Lazy eviction: pop the slot unconditionally, but only delete the
		// live entry when this slot is still its insertion slot (a
		// last-wins re-registration owns the id now and has its own slot).
		if cur, ok := artifactRegistry.byID[front.id]; ok && cur.seq == front.seq {
			delete(artifactRegistry.byID, front.id)
		}
	}
	return dup
}

// lookupSubagentArtifact resolves an id to its validated entry.
func lookupSubagentArtifact(id string) (artifactEntry, bool) {
	artifactRegistry.mu.Lock()
	defer artifactRegistry.mu.Unlock()
	e, ok := artifactRegistry.byID[id]
	if !ok || e == nil {
		return artifactEntry{}, false
	}
	return *e, true
}

// listSubagentArtifactIDs returns up to maxListedArtifactIDs registered ids
// (oldest first), plus the total live count.
func listSubagentArtifactIDs() ([]string, int) {
	artifactRegistry.mu.Lock()
	defer artifactRegistry.mu.Unlock()
	ids := make([]string, 0, maxListedArtifactIDs)
	for _, slot := range artifactRegistry.order {
		if len(ids) >= maxListedArtifactIDs {
			break
		}
		if cur, ok := artifactRegistry.byID[slot.id]; ok && cur.seq == slot.seq {
			ids = append(ids, slot.id)
		}
	}
	return ids, len(artifactRegistry.byID)
}

// registerTaskArtifacts validates and registers every artifact of one
// child result against the task's dir, returning human-readable note lines
// for ambiguities (duplicate ids). Validation failures are silently skipped
// — renderArtifacts already flags them in the summary.
func registerTaskArtifacts(raw, dir string, taskIdx int) []string {
	var r subagentResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil || len(r.Artifacts) == 0 {
		return nil
	}
	var notes []string
	for _, ref := range r.Artifacts {
		path, err := artifact.Validate(ref, []string{dir})
		if err != nil {
			continue
		}
		dup := registerSubagentArtifact(artifactEntry{Ref: ref, Path: path, TaskIdx: taskIdx})
		if dup {
			notes = append(notes, fmt.Sprintf("[artifact] duplicate id %q — artifact_read now resolves to the task %d copy", ref.ID, taskIdx+1))
		}
	}
	return notes
}

// artifactReadEnabled reports whether this process gets the artifact_read
// tool: top-level operator runs only (SelfTrust empty). Sub-agents run in
// their own process whose registry is always empty — the tool would be dead
// weight, and the design keeps it parent-only.
func artifactReadEnabled(tcfg toolConfig) bool {
	return tcfg.SelfTrust == ""
}

// artifactIDList renders the bounded available-id list for unknown-id errors.
func artifactIDList() string {
	ids, total := listSubagentArtifactIDs()
	if total == 0 {
		return "no artifacts registered this session"
	}
	list := strings.Join(ids, ", ")
	if total > len(ids) {
		list += fmt.Sprintf(" (+%d more)", total-len(ids))
	}
	return list
}

// ── M3: child staging + trusted-runner relocation ────────────────────

// The canonical artifact dir (~/.odek/artifacts) is doubly protected from
// child writes: confineToCWD rejects absolute paths, and the danger
// classifier escalates any ~/.odek write to system_write (denied for
// approval-less children). Children therefore stage deliverables INSIDE
// the workspace — an ordinary local_write both gates allow — and the
// trusted child runner relocates them to the canonical dir before the
// exit scan. Gates untouched; wire format unchanged (artifact_root still
// names the canonical dir, v1.32.0 compatible).
const stagingDirName = ".odek-artifacts"

// stagingDirFor returns the child-visible staging dir for a task, INSIDE
// the workspace (cwd).
func stagingDirFor(cwd, taskID string) string {
	return filepath.Join(cwd, stagingDirName, taskID)
}

// childArtifactNote builds the trusted runner instruction appended to the
// child's request. It references the workspace-RELATIVE staging path only.
func childArtifactNote(stagingRel string) string {
	return "\n\nArtifact output: any deliverable larger than a short headline must ALSO be written as a file in " +
		stagingRel + "/ (use your file tools; plain files, no subdirectories). Files there are delivered to the parent automatically — do not repeat their contents in your final answer."
}

// renameFailureHook lets tests force the copy fallback (rename across
// devices). Production never sets it.
var renameFailureHook func() bool

// relocateStagingArtifacts moves every top-level regular file from the
// staging dir into the canonical dir (rename, with a copy fallback for
// cross-device workspaces), then removes the staging subtree. Nested
// directories are not artifacts (scanArtifacts skips them) and are
// discarded with the staging tree. A missing staging dir is a no-op.
func relocateStagingArtifacts(staging, canonical string) (int, error) {
	entries, err := os.ReadDir(staging)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("artifact staging: %w", err)
	}
	if err := os.MkdirAll(canonical, 0o700); err != nil {
		return 0, fmt.Errorf("artifact canonical dir: %w", err)
	}
	moved := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src := filepath.Join(staging, e.Name())
		dst := filepath.Join(canonical, e.Name())
		if renameFailureHook == nil || !renameFailureHook() {
			if err := os.Rename(src, dst); err == nil {
				moved++
				continue
			}
		}
		if err := copyFileContents(src, dst); err != nil {
			return moved, fmt.Errorf("artifact relocate %q: %w", e.Name(), err)
		}
		os.Remove(src)
		moved++
	}
	// Only top-level files are artifacts; anything else staged (nested
	// dirs) is discarded with the staging tree.
	os.RemoveAll(staging)
	return moved, nil
}

// copyFileContents copies src to dst (0600), replacing any existing dst.
func copyFileContents(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
