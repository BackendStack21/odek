package extended

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/BackendStack21/go-vector/pkg/vector"
)

// consolidationPrompt asks the LLM to merge a group of near-duplicate atom
// texts into a single concise statement.
const consolidationPrompt = `You are a memory consolidation system. The following memory atoms are near-duplicates of the same fact. Merge them into a single concise statement that preserves all durable information. Return only the merged statement, nothing else.

%s`

// ConsolidateAtoms finds groups of live atoms that are near-duplicates
// (pairwise cosine similarity reaching consolidate_similarity_threshold),
// asks the LLM to merge each group into one atom, stores the merged atom
// through the normal add path (so the scan and the size cap apply), and
// removes the originals. Quarantined atoms are never touched. On any failure
// for a group (LLM error, empty/garbage response, scan rejection) the
// originals are kept untouched. It returns the number of groups merged.
func (em *ExtendedMemory) ConsolidateAtoms(ctx context.Context) (merged int, err error) {
	if em == nil || !em.Enabled() {
		return 0, fmt.Errorf("extended memory: disabled")
	}
	if em.llm == nil {
		return 0, fmt.Errorf("extended memory: consolidation requires an LLM")
	}
	threshold := em.cfg.ConsolidateSimilarityThreshold
	if threshold <= 0 {
		threshold = DefaultConfig().ConsolidateSimilarityThreshold
	}
	atoms, err := em.store.List()
	if err != nil {
		return 0, fmt.Errorf("extended memory: consolidate list atoms: %w", err)
	}
	if len(atoms) < 2 {
		return 0, nil
	}
	texts := make([]string, len(atoms))
	for i, a := range atoms {
		texts[i] = a.Text
	}
	// Embed the live corpus directly: the vector index may be stale, and
	// consolidation must compare the current atoms.
	vecs, err := em.index.embedTexts(texts)
	if err != nil {
		return 0, fmt.Errorf("extended memory: consolidate embed: %w", err)
	}
	for _, group := range groupBySimilarity(atoms, vecs, threshold) {
		if em.mergeGroup(ctx, group) {
			merged++
		}
	}
	return merged, nil
}

// groupBySimilarity greedily clusters atoms whose cosine similarity to the
// group's first member reaches threshold. Only groups with more than one
// member are returned.
func groupBySimilarity(atoms []MemoryAtom, vecs []vector.Vector, threshold float32) [][]MemoryAtom {
	used := make([]bool, len(atoms))
	var groups [][]MemoryAtom
	for i := range atoms {
		if used[i] || vecs[i] == nil {
			continue
		}
		group := []MemoryAtom{atoms[i]}
		for j := i + 1; j < len(atoms); j++ {
			if used[j] || vecs[j] == nil {
				continue
			}
			if cosine(vecs[i], vecs[j]) >= threshold {
				group = append(group, atoms[j])
				used[j] = true
			}
		}
		if len(group) > 1 {
			groups = append(groups, group)
			used[i] = true
		}
	}
	return groups
}

// mergeGroup consolidates one near-duplicate group. It reports whether the
// group was merged; on any failure the originals are kept.
func (em *ExtendedMemory) mergeGroup(ctx context.Context, group []MemoryAtom) bool {
	var b strings.Builder
	for i, a := range group {
		fmt.Fprintf(&b, "%d. %s\n", i+1, a.Text)
	}
	resp, err := em.llm.SimpleCall(ctx,
		"You are a memory consolidation system. Return only the merged statement.",
		fmt.Sprintf(consolidationPrompt, b.String()),
	)
	if err != nil {
		log.Printf("extended memory: consolidation LLM failed: %v", err)
		return false
	}
	text := strings.TrimSpace(resp)
	if text == "" {
		log.Printf("extended memory: consolidation returned empty text; keeping originals")
		return false
	}
	// Pre-scan the merged text: a rejection after removing the originals
	// would silently lose memories, so treat it as a failure up front.
	if err := em.scanContent(ctx, text); err != nil {
		log.Printf("extended memory: consolidated atom rejected by scan: %v", err)
		return false
	}

	// Base the merged atom on the highest-confidence group member (type,
	// pin, and confidence carry over) with a refreshed CreatedAt and the
	// union of the group's outward associations.
	merged := group[0]
	inGroup := make(map[string]bool, len(group))
	for _, a := range group {
		inGroup[a.ID] = true
		if a.Confidence > merged.Confidence {
			merged = a
		}
	}
	var related []string
	for _, a := range group {
		for _, id := range a.Context.RelatedAtomIDs {
			if !inGroup[id] && !slices.Contains(related, id) {
				related = append(related, id)
			}
		}
	}
	id, err := generateAtomID()
	if err != nil {
		log.Printf("extended memory: consolidate generate id: %v", err)
		return false
	}
	merged.ID = id
	merged.Text = text
	merged.CreatedAt = time.Now().UTC()
	merged.Context.RelatedAtomIDs = related

	// Validation passed: remove the originals, then store the merged atom
	// through the normal add path so the size cap applies. Removing first
	// also prevents semantic dedup from collapsing the merged atom back
	// into one of its originals.
	for _, a := range group {
		if err := em.store.Remove(a.ID); err != nil {
			log.Printf("extended memory: consolidate remove original %s: %v", a.ID, err)
		}
		em.assoc.RemoveAtom(a.ID)
	}
	if err := em.addAtoms(ctx, []MemoryAtom{merged}, false); err != nil {
		log.Printf("extended memory: consolidate store merged atom: %v", err)
		return false
	}
	_ = em.assoc.Persist()
	return true
}
