package extended

import (
	"fmt"
	"log"
	"sort"
)

// sizedAtom pairs an atom with its actual on-disk size in bytes.
type sizedAtom struct {
	atom MemoryAtom
	size int64
}

// Evictor selects atoms for eviction when the store approaches its size cap.
type Evictor interface {
	// SelectForEviction returns atom IDs to remove to free at least needBytes.
	// sizedAtoms provides the actual disk size for each atom.
	SelectForEviction(sizedAtoms []sizedAtom, needBytes int64) []string
}

// newEvictor builds the eviction strategy selected by cfg.EvictionPolicy.
func newEvictor(cfg Config) Evictor {
	switch cfg.EvictionPolicy {
	case "retention_decay", "":
		return &retentionDecayEvictor{cfg: cfg}
	default:
		return &retentionDecayEvictor{cfg: cfg}
	}
}

// retentionDecayEvictor evicts atoms with the lowest retention scores first,
// skipping pinned atoms.
type retentionDecayEvictor struct {
	cfg Config
}

// SelectForEviction returns IDs to remove. Pinned atoms are never selected.
func (e *retentionDecayEvictor) SelectForEviction(sizedAtoms []sizedAtom, needBytes int64) []string {
	if needBytes <= 0 {
		return nil
	}
	scored := make([]struct {
		sized sizedAtom
		score float32
	}, 0, len(sizedAtoms))
	for _, s := range sizedAtoms {
		if s.atom.Pin {
			continue
		}
		score := RetentionScore(s.atom, e.cfg.DecayHalfLifeDays)
		scored = append(scored, struct {
			sized sizedAtom
			score float32
		}{sized: s, score: score})
	}
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score < scored[j].score
	})

	var freed int64
	var ids []string
	for _, s := range scored {
		if freed >= needBytes {
			break
		}
		ids = append(ids, s.sized.atom.ID)
		freed += s.sized.size
		if s.sized.size <= 0 {
			freed += int64(len(s.sized.atom.Text)) + 256
		}
	}
	if freed < needBytes {
		log.Printf("extended memory: eviction freed only %d of %d requested bytes", freed, needBytes)
	}
	return ids
}

// buildSizedAtoms resolves the actual on-disk size for each atom. If the store
// cannot provide a per-atom size, it falls back to len(text)+overhead.
func buildSizedAtoms(store *AtomStore, atoms []MemoryAtom) []sizedAtom {
	out := make([]sizedAtom, 0, len(atoms))
	for _, a := range atoms {
		size, err := store.AtomSize(a.ID)
		if err != nil {
			size = int64(len(a.Text)) + 256
		}
		out = append(out, sizedAtom{atom: a, size: size})
	}
	return out
}

// vectorCost estimates the amortized vector-index cost per atom. It is used
// when the index has not yet been persisted.
func vectorCost(totalAtoms int) int64 {
	// Rough per-atom estimate: 256-dim float32 vector + small overhead.
	const bytesPerVec = 256 * 4
	const overhead = 64
	return bytesPerVec + overhead
}

// projectSize returns the estimated total size if newBytes were added.
func projectSize(storeSize, quarantineSize, newBytes int64) int64 {
	return storeSize + quarantineSize + newBytes
}

func sizeLabel(n int64) string {
	if n >= 1024*1024 {
		return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
	}
	if n >= 1024 {
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	}
	return fmt.Sprintf("%d B", n)
}
