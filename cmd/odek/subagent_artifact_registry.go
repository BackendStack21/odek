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

type origKey struct {
	origID  string
	taskIdx int
}

var artifactRegistry struct {
	mu     sync.Mutex
	byID   map[string]*artifactEntry
	byOrig map[origKey]string // (original id, task) → effective registered id (D1 aliasing)
	order  []registrySlot
	seq    uint64
	marks  []uint64 // active per-run floor watermarks (F)
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
	artifactRegistry.byOrig = map[origKey]string{}
	artifactRegistry.order = nil
	artifactRegistry.seq = 0
	artifactRegistry.marks = nil
}

// registerSubagentArtifact records a validated artifact under its ref id.
// FIRST-WINS on duplicate ids (D1, SUBAGENT_ARTIFACT_DELIVERY_PLAN.md): the
// first occurrence keeps the plain id; a later task's duplicate registers
// under a probe-increment alias "<id>.t<taskIdx+1>", probing past any live
// entry — including real filename stems that collide with the alias
// namespace (dots are valid in ids). Aliasing never evicts a live entry.
// Returns the EFFECTIVE registered id and whether it was a duplicate.
func registerSubagentArtifact(e artifactEntry) (string, bool) {
	if e.Ref.ID == "" || e.Path == "" {
		return "", false
	}

	artifactRegistry.mu.Lock()
	defer artifactRegistry.mu.Unlock()
	artifactRegistry.seq++
	e.seq = artifactRegistry.seq
	if artifactRegistry.byID == nil {
		artifactRegistry.byID = map[string]*artifactEntry{}
	}
	if artifactRegistry.byOrig == nil {
		artifactRegistry.byOrig = map[origKey]string{}
	}
	orig := e.Ref.ID
	id := orig
	if _, taken := artifactRegistry.byID[orig]; taken {
		alias := aliasArtifactID(orig, e.TaskIdx)
		if alias == "" {
			return "", true
		}
		e.Ref.ID = alias
		id = alias
	}
	artifactRegistry.byID[id] = &e
	artifactRegistry.byOrig[origKey{origID: orig, taskIdx: e.TaskIdx}] = id
	artifactRegistry.order = append(artifactRegistry.order, registrySlot{id: id, seq: e.seq})
	evictArtifactRegistryLocked()
	return id, id != orig
}

// maxAliasProbes bounds the .t<N> probe walk before falling back to a
// seq-derived id (monotonic, therefore always free).
const maxAliasProbes = 128

// aliasArtifactID derives a free alias for a duplicate artifact id: the
// owning task's "<id>.t<taskIdx+1>", probing forward past any live entry.
// Caller must hold the registry mutex. Returns "" only if the fallback is
// somehow taken (cannot happen: seq is monotonic).
func aliasArtifactID(origID string, taskIdx int) string {
	for n := taskIdx + 1; n <= taskIdx+1+maxAliasProbes; n++ {
		cand := fmt.Sprintf("%s.t%d", origID, n)
		if !artifactIDRe.MatchString(cand) {
			break // too long/invalid — probing further only gets longer
		}
		if _, taken := artifactRegistry.byID[cand]; !taken {
			return cand
		}
	}
	for {
		artifactRegistry.seq++
		cand := fmt.Sprintf("artifact-%d", artifactRegistry.seq)
		if _, taken := artifactRegistry.byID[cand]; !taken {
			return cand
		}
	}
}

// evictArtifactRegistryLocked trims the queue to the cap, oldest first —
// but never evicts entries of an ACTIVE run (seq >= the lowest watermark):
// a delegate_tasks call's artifacts must stay resolvable while it is still
// collating (F — per-run registry floor). Caller holds the mutex.
func evictArtifactRegistryLocked() {
	for len(artifactRegistry.order) > artifactRegistryCap {
		front := artifactRegistry.order[0]
		if m := minActiveMarkLocked(); m != 0 && front.seq >= m {
			break
		}
		artifactRegistry.order = artifactRegistry.order[1:]
		// Lazy eviction: pop the slot unconditionally, but only delete the
		// live entry when this slot is still its insertion slot (a
		// re-registration owns the id now and has its own slot).
		if cur, ok := artifactRegistry.byID[front.id]; ok && cur.seq == front.seq {
			delete(artifactRegistry.byID, front.id)
		}
	}
}

// beginArtifactRegistryRun opens a per-run floor: entries registered from
// now on (seq >= watermark) are not evicted until the matching
// endArtifactRegistryRun (F). Returns the watermark to pass to the end
// function.
func beginArtifactRegistryRun() uint64 {
	artifactRegistry.mu.Lock()
	defer artifactRegistry.mu.Unlock()
	m := artifactRegistry.seq + 1
	artifactRegistry.marks = append(artifactRegistry.marks, m)
	return m
}

// endArtifactRegistryRun closes the floor opened by beginArtifactRegistryRun.
func endArtifactRegistryRun(mark uint64) {
	artifactRegistry.mu.Lock()
	defer artifactRegistry.mu.Unlock()
	for i, m := range artifactRegistry.marks {
		if m == mark {
			artifactRegistry.marks = append(artifactRegistry.marks[:i], artifactRegistry.marks[i+1:]...)
			return
		}
	}
}

// minActiveMarkLocked reports the lowest active watermark (0 = none).
// Caller holds the mutex.
func minActiveMarkLocked() uint64 {
	if len(artifactRegistry.marks) == 0 {
		return 0
	}
	min := artifactRegistry.marks[0]
	for _, m := range artifactRegistry.marks[1:] {
		if m < min {
			min = m
		}
	}
	return min
}

// lookupEffectiveArtifactID resolves the id a given (original id, task)
// pair registered under — the render side uses it so the parent always
// copies an id artifact_read can actually resolve (D1 provenance).
func lookupEffectiveArtifactID(origID string, taskIdx int) (string, bool) {
	artifactRegistry.mu.Lock()
	defer artifactRegistry.mu.Unlock()
	id, ok := artifactRegistry.byOrig[origKey{origID: origID, taskIdx: taskIdx}]
	return id, ok
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
		id, dup := registerSubagentArtifact(artifactEntry{Ref: ref, Path: path, TaskIdx: taskIdx})
		if dup {
			notes = append(notes, fmt.Sprintf("[artifact] duplicate id %q — task %d copy registered as %q", ref.ID, taskIdx+1, id))
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
	return "\n\nResult delivery: your final answer is capped at ~2000 characters — content beyond the cap is lost. If your result fits in a short paragraph, just answer. Otherwise write each deliverable (report, findings, diffs, generated content) as a FLAT file directly in " +
		stagingRel + "/ (no subdirectories — nested files are discarded), then end with a short headline: status, artifact file names, key decisions. Files are delivered to the orchestrator automatically; do not repeat their contents in the final answer."
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

const (
	// stagingSweepMaxAge matches the janitor backstop retention: orphaned
	// staging subtrees (crash/kill before relocation) older than this are
	// swept by the next artifact-bearing run in the same workspace.
	stagingSweepMaxAge = 24 * time.Hour
	// stagingGitignore keeps staged deliverables out of the user's
	// repository — the staging root lives INSIDE the workspace.
	stagingGitignore = "*\n!.gitignore\n"
)

// ensureStagingRoot creates the staging root (0700) and drops a
// self-gitignore so staged deliverables never land in the user's
// repository. Idempotent; best-effort.
func ensureStagingRoot(cwd string) {
	root := filepath.Join(cwd, stagingDirName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(root, ".gitignore"), []byte(stagingGitignore), 0o600)
}

// sweepStagingOrphans removes sibling staging task dirs older than maxAge —
// crash/kill orphans the runner could not clean (the janitor only knows
// ~/.odek, not user workspaces). The current task's dir is never touched,
// and fresh siblings may belong to in-flight parallel tasks in the same
// workspace. Returns the number of subtrees removed.
func sweepStagingOrphans(cwd, currentTaskID string, maxAge time.Duration) int {
	root := filepath.Join(cwd, stagingDirName)
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, e := range entries {
		if !e.IsDir() || e.Name() == currentTaskID {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if os.RemoveAll(filepath.Join(root, e.Name())) == nil {
			removed++
		}
	}
	return removed
}
