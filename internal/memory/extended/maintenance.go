package extended

import (
	"encoding/json"
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

// Prune deletes TTL-expired ephemeral atoms, sweeps orphan chunk files and
// evicts expired quarantine entries. Recall already hides expired atoms
// (filterExpiredAtoms); Prune reclaims their space and keeps them out of
// store-wide passes such as the stale-goal nudges. Pinned and durable atoms
// are never touched. With dryRun the report is computed and nothing changes.
func (em *ExtendedMemory) Prune(now time.Time, dryRun bool) (PruneReport, error) {
	var rep PruneReport
	if em == nil || !em.Enabled() {
		return rep, fmt.Errorf("extended memory: disabled")
	}
	atoms, err := em.store.List()
	if err != nil {
		return rep, err
	}
	live := make(map[string]bool, len(atoms))
	for _, a := range atoms {
		live[a.ID] = true
		if !AtomExpired(a, em.cfg.EphemeralTTLDays, now) {
			continue
		}
		rep.ExpiredAtoms++
		if dryRun {
			continue
		}
		if err := em.ForgetAtom(a.ID); err != nil {
			return rep, err
		}
	}

	orphans, err := em.store.orphanChunks(live)
	if err != nil {
		return rep, err
	}
	rep.OrphanChunks = len(orphans)
	if !dryRun {
		for _, p := range orphans {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return rep, fmt.Errorf("extended store: remove orphan chunk: %w", err)
			}
		}
	}

	if dryRun {
		expired, err := em.quarantine.countExpired(em.cfg.QuarantineTTLDays, now)
		if err != nil {
			return rep, err
		}
		rep.ExpiredQuarantine = expired
		return rep, nil
	}
	n, err := em.quarantine.EvictExpired(em.cfg.QuarantineTTLDays)
	if err != nil {
		return rep, err
	}
	rep.ExpiredQuarantine = n
	return rep, nil
}

// orphanChunks returns the paths of chunk files whose atom id is not in
// live. Only well-formed "<id>.md" names are considered.
func (s *AtomStore) orphanChunks(live map[string]bool) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.chunksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("extended store: read chunks: %w", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		id, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok || id == "" || live[id] {
			continue
		}
		out = append(out, filepath.Join(s.chunksDir, e.Name()))
	}
	return out, nil
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
