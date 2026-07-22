package extended

import (
	"strings"
	"sync/atomic"
)

// Stats is an observability snapshot of the Extended Memory subsystem.
type Stats struct {
	LiveAtoms         int
	QuarantinedAtoms  int
	QuarantineReasons map[string]int
	IndexVectors      int
	IndexDirty        bool
	StoreSizeBytes    int64
	RecallTimeouts    uint64
	RecallFailures    uint64
}

// recallStats holds the atomic recall-path error counters backing Stats.
// Deadline-exceeded errors count as timeouts, anything else as a failure.
type recallStats struct {
	timeouts atomic.Uint64
	failures atomic.Uint64
}

// Stats returns a snapshot of live/quarantine atom counts, index state,
// on-disk store size, and recall error counters.
func (em *ExtendedMemory) Stats() Stats {
	if em == nil {
		return Stats{}
	}
	var s Stats
	if atoms, err := em.store.List(); err == nil {
		s.LiveAtoms = len(atoms)
	}
	if entries, err := em.quarantine.ListEntries(); err == nil {
		s.QuarantinedAtoms = len(entries)
		s.QuarantineReasons = make(map[string]int, len(entries))
		for _, e := range entries {
			// Count by reason prefix: "scan_rejected: <detail>" collapses to
			// "scan_rejected", "tainted" stays "tainted".
			reason, _, _ := strings.Cut(e.Reason, ":")
			reason = strings.TrimSpace(reason)
			if reason == "" {
				reason = "unknown"
			}
			s.QuarantineReasons[reason]++
		}
	}
	s.IndexVectors, s.IndexDirty = em.index.stats()
	s.StoreSizeBytes = em.Size()
	s.RecallTimeouts = em.stats.timeouts.Load()
	s.RecallFailures = em.stats.failures.Load()
	return s
}
