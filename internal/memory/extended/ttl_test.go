package extended

import (
	"testing"
	"time"
)

// TestAtomExpired pins the per-class TTL: ephemeral atom types (intent,
// goal, error, question, file) expire after the configured TTL and stop
// being recalled, while durable types (preference, convention, fact,
// decision) never TTL out and pinned atoms are always exempt.
func TestAtomExpired(t *testing.T) {
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -30)
	cases := []struct {
		name string
		atom MemoryAtom
		want bool
	}{
		{"old error", MemoryAtom{Type: TypeError, CreatedAt: old}, true},
		{"old goal", MemoryAtom{Type: TypeGoal, CreatedAt: old}, true},
		{"old question", MemoryAtom{Type: TypeQuestion, CreatedAt: old}, true},
		{"old intent", MemoryAtom{Type: TypeIntent, CreatedAt: old}, true},
		{"old file", MemoryAtom{Type: TypeFile, CreatedAt: old}, true},
		{"old preference", MemoryAtom{Type: TypePreference, CreatedAt: old}, false},
		{"old convention", MemoryAtom{Type: TypeConvention, CreatedAt: old}, false},
		{"old fact", MemoryAtom{Type: TypeFact, CreatedAt: old}, false},
		{"old decision", MemoryAtom{Type: TypeDecision, CreatedAt: old}, false},
		{"pinned error", MemoryAtom{Type: TypeError, CreatedAt: old, Pin: true}, false},
		{"fresh error", MemoryAtom{Type: TypeError, CreatedAt: now}, false},
		{"zero created_at legacy", MemoryAtom{Type: TypeError}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AtomExpired(tc.atom, 14, now); got != tc.want {
				t.Errorf("AtomExpired(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestAtomExpiredTTLDaysFallback pins that a zero or negative TTL days
// value falls back to the default rather than disabling or inverting the
// TTL.
func TestAtomExpiredTTLDaysFallback(t *testing.T) {
	old := time.Now().UTC().AddDate(0, 0, -(DefaultEphemeralTTLDays + 1))
	atom := MemoryAtom{Type: TypeError, CreatedAt: old}
	if !AtomExpired(atom, 0, time.Now().UTC()) {
		t.Error("ttlDays=0 must fall back to the default TTL, expiring an old ephemeral atom")
	}
	if !AtomExpired(atom, -5, time.Now().UTC()) {
		t.Error("negative ttlDays must fall back to the default TTL")
	}
}

// TestRecallSkipsExpiredEphemeralAtoms pins that expired ephemeral atoms
// are not injected into the recall context.
func TestRecallSkipsExpiredEphemeralAtoms(t *testing.T) {
	old := time.Now().UTC().AddDate(0, 0, -30)
	atoms := []MemoryAtom{
		{ID: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d1", Text: "stale error", Type: TypeError, CreatedAt: old},
		{ID: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d2", Text: "durable convention", Type: TypeConvention, CreatedAt: old},
	}
	got := filterExpiredAtoms(atoms, 14)
	if len(got) != 1 || got[0].Text != "durable convention" {
		t.Fatalf("expected only durable atom to survive, got %+v", got)
	}
}
