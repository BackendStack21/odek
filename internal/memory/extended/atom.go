package extended

import (
	"math"
	"time"

	"github.com/BackendStack21/go-vector/pkg/vector"
)

// MemoryAtom is the atomic unit of Extended Memory.
type MemoryAtom struct {
	ID          string      `json:"id"`
	Text        string      `json:"text"`
	SourceClass string      `json:"source_class"`
	Type        string      `json:"type"`
	CreatedAt   time.Time   `json:"created_at"`
	Context     AtomContext `json:"context,omitempty"`
	Pin         bool        `json:"pin,omitempty"`
	Confidence  float32     `json:"confidence,omitempty"`

	// Vector is the embedding of Text. It is not persisted directly; the
	// vector index is rebuilt from atom content on demand.
	Vector vector.Vector `json:"-"`
}

// AtomContext carries provenance metadata for an atom.
type AtomContext struct {
	SessionID      string   `json:"session_id,omitempty"`
	Turn           int      `json:"turn,omitempty"`
	Project        string   `json:"project,omitempty"`
	RelatedAtomIDs []string `json:"related_atom_ids,omitempty"`
}

// Source classes. The zero-trust boundary is external content.
const (
	SourceUserSaid       = "user_said"
	SourceInferred       = "inferred"
	SourceUserApproved   = "user_approved"
	SourceToolOutput     = "tool_output"
	SourceFileRead       = "file_read"
	SourceWeb            = "web"
	SourceMCP            = "mcp"
	SourceSubagent       = "subagent"
	SourceAgentGenerated = "agent_generated"
)

// Atom types.
const (
	TypeFact        = "fact"
	TypeObservation = "observation"
	TypePreference  = "preference"
	TypeIntent      = "intent"
)

// TrustBoost returns a multiplicative boost for high-trust source classes.
// External / generated sources receive a zero boost so they cannot be
// recalled without promotion.
func TrustBoost(sourceClass string) float32 {
	switch sourceClass {
	case SourceUserSaid, SourceUserApproved:
		return 1.0
	case SourceInferred:
		return 0.8
	default:
		return 0.0
	}
}

// IsTaintedSourceClass reports whether a source class originates outside the
// trust boundary.
func IsTaintedSourceClass(sourceClass string) bool {
	switch sourceClass {
	case SourceToolOutput, SourceFileRead, SourceWeb, SourceMCP, SourceSubagent, SourceAgentGenerated:
		return true
	default:
		return false
	}
}

// DecayFactor computes exponential time decay based on CreatedAt.
// halfLifeDays controls the decay rate; the default is 30 days.
func DecayFactor(createdAt time.Time, halfLifeDays int) float32 {
	if halfLifeDays <= 0 {
		halfLifeDays = 30
	}
	age := time.Since(createdAt)
	if age <= 0 {
		return 1.0
	}
	halfLife := time.Duration(halfLifeDays) * 24 * time.Hour
	ratio := float64(age) / float64(halfLife)
	score := math.Pow(0.5, ratio)
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return 0
	}
	return float32(score)
}

// RetentionScore combines confidence, trust boost, and time decay.
func RetentionScore(atom MemoryAtom, halfLifeDays int) float32 {
	conf := atom.Confidence
	if conf <= 0 {
		conf = 1.0
	}
	if conf > 1.0 {
		conf = 1.0
	}
	boost := TrustBoost(atom.SourceClass)
	if boost <= 0 {
		return 0
	}
	decay := DecayFactor(atom.CreatedAt, halfLifeDays)
	return conf * boost * decay
}

// NormalizeAtom sanitizes atom fields, applying safe defaults.
func NormalizeAtom(atom *MemoryAtom) {
	if atom.Type == "" {
		atom.Type = TypeObservation
	}
	if atom.SourceClass == "" {
		atom.SourceClass = SourceUserSaid
	}
	if atom.Confidence <= 0 {
		atom.Confidence = 1.0
	}
	if atom.Confidence > 1.0 {
		atom.Confidence = 1.0
	}
	if atom.CreatedAt.IsZero() {
		atom.CreatedAt = time.Now().UTC()
	}
}
