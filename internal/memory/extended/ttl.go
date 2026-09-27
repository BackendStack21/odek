package extended

import "time"

// ephemeralTypes are atom classes whose value decays with time: intents and
// goals expire once achieved, errors and questions once resolved, and file
// references rot as the codebase moves. Durable classes (preference,
// convention, fact, decision) carry no TTL by design.
var ephemeralTypes = map[string]bool{
	TypeIntent:   true,
	TypeGoal:     true,
	TypeError:    true,
	TypeQuestion: true,
	TypeFile:     true,
}

// DefaultEphemeralTTLDays is the fallback TTL for ephemeral atom classes
// when the config does not set one.
const DefaultEphemeralTTLDays = 14

// AtomExpired reports whether an ephemeral-class atom has outlived its TTL
// and must stop being recalled. Pinned atoms never expire; durable classes
// never expire via TTL (they leave through consolidation/eviction instead).
func AtomExpired(atom MemoryAtom, ttlDays int, now time.Time) bool {
	if atom.Pin || !ephemeralTypes[atom.Type] {
		return false
	}
	if atom.CreatedAt.IsZero() {
		// Legacy atoms without a creation time are treated as fresh so a
		// TTL never silently wipes them.
		return false
	}
	if ttlDays <= 0 {
		ttlDays = DefaultEphemeralTTLDays
	}
	deadline := atom.CreatedAt.AddDate(0, 0, ttlDays)
	return !now.Before(deadline)
}

// filterExpiredAtoms removes TTL-expired ephemeral atoms from a recall set.
func filterExpiredAtoms(atoms []MemoryAtom, ttlDays int) []MemoryAtom {
	now := time.Now().UTC()
	out := make([]MemoryAtom, 0, len(atoms))
	for _, a := range atoms {
		if AtomExpired(a, ttlDays, now) {
			continue
		}
		out = append(out, a)
	}
	return out
}
