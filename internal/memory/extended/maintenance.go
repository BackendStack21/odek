package extended

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BackendStack21/go-vector/pkg/vector"
)

// PruneReport counts what a Prune pass removed (or, on a dry run, would
// remove).
type PruneReport struct {
	ExpiredAtoms      int // TTL-expired ephemeral atoms (goal, intent, question, error, file)
	OrphanChunks      int // chunk files with no atom in the index
	ExpiredQuarantine int // quarantine entries past quarantine_ttl_days
}

// orphanChunkGrace is how old a chunk file must be before the sweep treats
// it as an orphan. AtomStore.Add writes the chunk before the atoms.json
// entry, and another process (a serve or Telegram daemon extracting atoms
// while the operator runs prune) is not covered by the in-process locks,
// so a chunk younger than this is left alone.
const orphanChunkGrace = 10 * time.Minute

// Prune deletes TTL-expired ephemeral atoms, sweeps orphan chunk files and
// evicts expired quarantine entries. Recall already hides expired atoms
// (filterExpiredAtoms); Prune reclaims their space and keeps them out of
// store-wide passes such as the stale-goal nudges. Pinned and durable atoms
// are never touched. With dryRun the report is computed and nothing
// changes. A failure on one atom does not stop the pass: every step runs
// and the errors are returned joined, next to the report of what was done.
// Quarantine eviction uses the wall clock (EvictExpired); now is the TTL
// reference for atoms.
func (em *ExtendedMemory) Prune(now time.Time, dryRun bool) (PruneReport, error) {
	var rep PruneReport
	if em == nil || !em.Enabled() {
		return rep, fmt.Errorf("extended memory: disabled")
	}
	atoms, err := em.store.List()
	if err != nil {
		return rep, err
	}
	var errs []error
	for _, a := range atoms {
		if !AtomExpired(a, em.cfg.EphemeralTTLDays, now) {
			continue
		}
		rep.ExpiredAtoms++
		if dryRun {
			continue
		}
		if err := em.ForgetAtom(a.ID); err != nil {
			errs = append(errs, err)
		}
	}

	orphans, err := em.store.sweepOrphanChunks(dryRun, now.Add(-orphanChunkGrace))
	if err != nil {
		errs = append(errs, err)
	}
	rep.OrphanChunks = orphans

	if dryRun {
		expired, err := em.quarantine.countExpired(em.cfg.QuarantineTTLDays, time.Now().UTC())
		if err != nil {
			errs = append(errs, err)
		}
		rep.ExpiredQuarantine = expired
		return rep, errors.Join(errs...)
	}
	n, err := em.quarantine.EvictExpired(em.cfg.QuarantineTTLDays)
	if err != nil {
		errs = append(errs, err)
	}
	rep.ExpiredQuarantine = n
	return rep, errors.Join(errs...)
}

// sweepOrphanChunks removes (or, on a dry run, counts) chunk files whose
// atom id is absent from atoms.json. The index is re-read under the store
// locks so an atom added between a caller's listing and the sweep is seen,
// and chunks modified after olderThan are skipped (see orphanChunkGrace).
// Only well-formed "<id>.md" names are considered.
func (s *AtomStore) sweepOrphanChunks(dryRun bool, olderThan time.Time) (int, error) {
	lock := dirLock(s.dir)
	lock.Lock()
	defer lock.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	metas, err := s.loadAtomsLocked()
	if err != nil {
		return 0, err
	}
	live := make(map[string]bool, len(metas))
	for _, m := range metas {
		live[m.ID] = true
	}
	entries, err := os.ReadDir(s.chunksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("extended store: read chunks: %w", err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		id, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok || id == "" || live[id] {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(olderThan) {
			continue
		}
		n++
		if dryRun {
			continue
		}
		if err := os.Remove(filepath.Join(s.chunksDir, e.Name())); err != nil && !os.IsNotExist(err) {
			return n, fmt.Errorf("extended store: remove orphan chunk: %w", err)
		}
		s.chunkCacheMu.Lock()
		delete(s.chunkCache, id)
		s.chunkCacheMu.Unlock()
	}
	return n, nil
}

// countExpired reports how many quarantine entries EvictExpired would drop
// without dropping them.
func (q *Quarantine) countExpired(ttlDays int, now time.Time) (int, error) {
	if ttlDays <= 0 {
		return 0, nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	entries, err := q.loadAtomsLocked()
	if err != nil {
		return 0, err
	}
	cutoff := now.AddDate(0, 0, -ttlDays)
	n := 0
	for _, e := range entries {
		if !e.QuarantinedAt.IsZero() && e.QuarantinedAt.Before(cutoff) {
			n++
		}
	}
	return n, nil
}

// Triage returns the live atoms whose text matches re. With move, each is
// taken out of the live store into quarantine under the given reason, where
// the usual review applies (promote restores it as user_approved, forget
// deletes it). It is the operator's recovery path for atoms that were stored
// with the wrong trust label — for instance sub-agent task briefs recorded
// as the principal's words before extraction was disabled for sub-agents.
// Pinned atoms are listed but never moved.
func (em *ExtendedMemory) Triage(re *regexp.Regexp, move bool, reason string) ([]MemoryAtom, error) {
	if em == nil || !em.Enabled() {
		return nil, fmt.Errorf("extended memory: disabled")
	}
	if re == nil {
		return nil, fmt.Errorf("extended memory: triage pattern required")
	}
	atoms, err := em.store.List()
	if err != nil {
		return nil, err
	}
	var matched []MemoryAtom
	for _, a := range atoms {
		if !re.MatchString(a.Text) {
			continue
		}
		matched = append(matched, a)
		if !move || a.Pin {
			continue
		}
		if reason == "" {
			reason = "operator triage"
		}
		if err := em.quarantine.StoreWithReason(a, reason); err != nil {
			return matched, err
		}
		if err := em.store.Remove(a.ID); err != nil {
			// Undo the quarantine copy so the atom is not in both places.
			_ = em.quarantine.Forget(a.ID)
			return matched, err
		}
		em.assoc.RemoveAtom(a.ID)
	}
	if move && len(matched) > 0 {
		_ = em.assoc.Persist()
		em.index.markDirty()
	}
	return matched, nil
}

// PersistedIndex describes the vector index on disk without rebuilding it:
// the number of persisted vectors and whether they are stale against the
// current atom corpus. ok is false when no index has been persisted. A
// short-lived process (the CLI) uses it to report the index state a
// long-lived process left behind.
func (em *ExtendedMemory) PersistedIndex() (vectors int, stale bool, ok bool) {
	if em == nil {
		return 0, false, false
	}
	return em.index.persisted()
}

func (vi *atomVectorIndex) persisted() (vectors int, stale bool, ok bool) {
	data, err := os.ReadFile(filepath.Join(vi.dir, vectorMetaFile))
	if err != nil {
		return 0, false, false
	}
	var meta vectorMeta
	if json.Unmarshal(data, &meta) != nil {
		return 0, false, false
	}
	store := vector.NewStore(vector.CosineDistance)
	if err := store.Load(filepath.Join(vi.dir, vectorFile)); err != nil {
		return 0, false, false
	}
	vectors = store.Len()
	vi.mu.RLock()
	fp := vi.emb.Fingerprint()
	listFn := vi.listAtoms
	vi.mu.RUnlock()
	stale = meta.Fingerprint != fp || meta.Corpus == ""
	if !stale && listFn != nil {
		atoms, err := listFn()
		stale = err != nil || meta.Corpus != corpusFingerprint(atoms)
	}
	return vectors, stale, true
}
