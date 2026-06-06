package memory

import (
	"math"

	"github.com/BackendStack21/go-vector/pkg/vector"
)

// MergeThresholds for merge-on-write classification.
const (
	// MergeThreshold is the cosine similarity threshold above which entries
	// are considered duplicates and auto-merged.
	MergeThreshold float32 = 0.7

	// AddThreshold is the cosine similarity below which entries are
	// considered distinct and auto-added without LLM judgment.
	AddThreshold float32 = 0.3

	// defaultOutputDim is the default RP dimensionality.
	defaultOutputDim = 256
)

// MergeDetector uses RandomProjections to quickly estimate whether a new
// fact entry overlaps with existing entries. This avoids ~80% of LLM calls
// during merge-on-write.
//
// Lifecycle:
//  1. NewMergeDetector(dims) — creates RP embedder
//  2. Fit(corpus) — builds vocabulary from existing entries
//  3. Classify(entry) → action + similarIdx + similarity
//  4. After facts change → Fit(newCorpus) to rebuild vocabulary
//
// Thresholds control the classification:
//   - mergeThreshold: cosine above this → auto-merge (default 0.7)
//   - addThreshold: cosine below this → auto-add (default 0.3)
//   - Between thresholds: "judge" — requires LLM to decide
type MergeDetector struct {
	rp             *vector.RandomProjections
	corpus         []string
	vecs           []vector.Vector // precomputed embeddings of corpus
	dims           int
	mergeThreshold float32
	addThreshold   float32
}

// NewMergeDetector creates a MergeDetector with the given output
// dimensionality for the RP embedder. Pass 0 for default (256).
// Uses default thresholds (0.7 merge, 0.3 add).
func NewMergeDetector(dims int) *MergeDetector {
	return NewMergeDetectorWithThresholds(dims, MergeThreshold, AddThreshold)
}

// NewMergeDetectorWithThresholds creates a MergeDetector with custom thresholds.
func NewMergeDetectorWithThresholds(dims int, mergeThreshold, addThreshold float32) *MergeDetector {
	if dims <= 0 {
		dims = defaultOutputDim
	}
	if mergeThreshold <= 0 {
		mergeThreshold = MergeThreshold
	}
	if addThreshold <= 0 || addThreshold >= mergeThreshold {
		addThreshold = AddThreshold
	}
	return &MergeDetector{
		rp:             vector.NewRandomProjections(dims),
		dims:           dims,
		mergeThreshold: mergeThreshold,
		addThreshold:   addThreshold,
	}
}

// Fit builds the RP vocabulary and pre-computes embeddings for all
// corpus entries. Call whenever facts change (after add/replace/remove).
func (m *MergeDetector) Fit(corpus []string) {
	m.corpus = make([]string, len(corpus))
	copy(m.corpus, corpus) // keep raw entries for merge/judge string logic

	// Fit and embed featurized text so RP sees normalised tokens + bigrams.
	// m.corpus stores the raw strings; only the go-vector boundary is featurized.
	feat := featurizeAll(corpus)
	m.rp.Fit(feat)

	m.vecs = make([]vector.Vector, len(corpus))
	for i, f := range feat {
		vec, err := m.rp.Embed(f)
		if err != nil {
			continue
		}
		m.vecs[i] = vec
	}
}

// Classify returns the merge decision for a new entry vs the fitted corpus.
//
// Returns:
//   - action: "merge" | "add" | "judge" | "nobody"
//   - similarIdx: index of the most similar corpus entry (for merge/judge)
//   - similarity: cosine similarity [0, 1]
//
// "nobody" means the corpus is empty — there's nothing to compare against.
func (m *MergeDetector) Classify(entry string) (action string, similarIdx int, similarity float32) {
	if len(m.corpus) == 0 || len(m.vecs) == 0 {
		return "nobody", -1, 0
	}

	vec, err := m.rp.Embed(featurizeForEmbedding(entry))
	if err != nil {
		return "nobody", -1, 0
	}

	// Find the most similar corpus entry
	bestSim := float32(-1)
	bestIdx := -1
	for i, cv := range m.vecs {
		if cv == nil {
			continue
		}
		sim := vector.Cosine(vec, cv)
		if math.IsNaN(float64(sim)) {
			sim = 0
		}
		if sim > bestSim {
			bestSim = sim
			bestIdx = i
		}
	}

	if bestIdx < 0 {
		return "nobody", -1, 0
	}

	similarity = bestSim
	similarIdx = bestIdx

	switch {
	case bestSim >= m.mergeThreshold:
		return "merge", bestIdx, bestSim
	case bestSim <= m.addThreshold:
		return "add", bestIdx, bestSim
	default:
		return "judge", bestIdx, bestSim
	}
}

// AppendEntry adds a single entry to the corpus. Only the new entry is embedded,
// avoiding a full re-embed of all existing entries. The RP vocabulary is still
// refreshed so new tokens from the entry are available for future Classify calls.
func (m *MergeDetector) AppendEntry(entry string) {
	m.corpus = append(m.corpus, entry)
	m.rp.Fit(featurizeAll(m.corpus))
	vec, err := m.rp.Embed(featurizeForEmbedding(entry))
	if err != nil {
		vec = nil
	}
	m.vecs = append(m.vecs, vec)
}

// ReplaceEntry replaces an entry at the given index. Only the changed entry is
// re-embedded, avoiding a full re-embed of all existing entries.
func (m *MergeDetector) ReplaceEntry(idx int, entry string) {
	if idx < 0 || idx >= len(m.corpus) {
		return
	}
	m.corpus[idx] = entry
	m.rp.Fit(featurizeAll(m.corpus))
	vec, err := m.rp.Embed(featurizeForEmbedding(entry))
	if err != nil {
		vec = nil
	}
	m.vecs[idx] = vec
}

// Corpus returns the current corpus (for inspection).
func (m *MergeDetector) Corpus() []string {
	out := make([]string, len(m.corpus))
	copy(out, m.corpus)
	return out
}
