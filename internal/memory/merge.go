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

// MergeDetector estimates whether a new fact entry overlaps with existing
// entries, using the configured embedding backend (RandomProjections by
// default — see textEmbedder). This avoids ~80% of LLM calls during
// merge-on-write.
//
// Lifecycle:
//  1. NewMergeDetector(dims) — creates the embedder
//  2. Fit(corpus) — prepares the embedder for the existing entries
//  3. Classify(entry) → action + similarIdx + similarity
//  4. After facts change → Fit(newCorpus) to refresh
//
// Thresholds control the classification:
//   - mergeThreshold: cosine above this → auto-merge (default 0.7)
//   - addThreshold: cosine below this → auto-add (default 0.3)
//   - Between thresholds: "judge" — requires LLM to decide
type MergeDetector struct {
	emb            textEmbedder
	corpus         []string
	vecs           []vector.Vector // precomputed embeddings of corpus
	mergeThreshold float32
	addThreshold   float32
}

// NewMergeDetector creates a MergeDetector with the given output
// dimensionality for the RP embedder. Pass 0 for default (256).
// Uses default thresholds (0.7 merge, 0.3 add).
func NewMergeDetector(dims int) *MergeDetector {
	return NewMergeDetectorWithThresholds(dims, MergeThreshold, AddThreshold)
}

// NewMergeDetectorWithThresholds creates a MergeDetector with custom thresholds
// over the default RandomProjections embedder.
func NewMergeDetectorWithThresholds(dims int, mergeThreshold, addThreshold float32) *MergeDetector {
	if dims <= 0 {
		dims = defaultOutputDim
	}
	return newMergeDetectorWithEmbedder(newRPTextEmbedder(dims), mergeThreshold, addThreshold)
}

// newMergeDetectorWithEmbedder creates a MergeDetector over an arbitrary
// embedding backend. The detector owns the embedder instance — RP backends
// are stateful per fitted corpus, so it must not be shared with other
// consumers (see textEmbedder).
func newMergeDetectorWithEmbedder(emb textEmbedder, mergeThreshold, addThreshold float32) *MergeDetector {
	if mergeThreshold <= 0 {
		mergeThreshold = MergeThreshold
	}
	if addThreshold <= 0 || addThreshold >= mergeThreshold {
		addThreshold = AddThreshold
	}
	return &MergeDetector{
		emb:            emb,
		mergeThreshold: mergeThreshold,
		addThreshold:   addThreshold,
	}
}

// Fit prepares the embedder and pre-computes embeddings for all corpus
// entries. Call whenever facts change (after add/replace/remove). On
// embedding failure the precomputed vectors are cleared, so Classify
// degrades to "nobody" (add without merge) rather than misclassifying.
func (m *MergeDetector) Fit(corpus []string) {
	m.corpus = make([]string, len(corpus))
	copy(m.corpus, corpus) // keep raw entries for merge/judge string logic

	if err := m.emb.fit(corpus); err != nil {
		m.vecs = nil
		return
	}
	vecs, err := m.emb.embedAll(corpus)
	if err != nil {
		m.vecs = nil
		return
	}
	m.vecs = vecs
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

	vec, err := m.emb.embed(entry)
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

// AppendEntry adds a single entry to the corpus. The embedder is refreshed so
// new tokens from the entry are available for future Classify calls; for
// stateless backends only the new entry costs an embedding call (cache).
func (m *MergeDetector) AppendEntry(entry string) {
	m.corpus = append(m.corpus, entry)
	if err := m.emb.fit(m.corpus); err != nil {
		m.vecs = append(m.vecs, nil)
		return
	}
	vec, err := m.emb.embed(entry)
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
	if err := m.emb.fit(m.corpus); err != nil {
		m.vecs[idx] = nil
		return
	}
	vec, err := m.emb.embed(entry)
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
