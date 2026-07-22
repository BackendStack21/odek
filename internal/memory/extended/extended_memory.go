package extended

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/embedding"
	"github.com/BackendStack21/odek/internal/guard"
)

// ExtendedMemory orchestrates atom storage, embedding, extraction, recall,
// and eviction for the Extended Memory subsystem.
type ExtendedMemory struct {
	cfg        Config
	store      *AtomStore
	index      *atomVectorIndex
	extractor  *Extractor
	recall     *Recall
	evictor    Evictor
	quarantine *Quarantine
	userModel  *UserModel
	assoc      *Associations
	predictor  *Predictor
	llm        LLMClient

	guard    guard.Guard
	guardCfg guard.Config

	dir      string
	mu       sync.RWMutex
	session  string
	project  string
	lastUser string

	// testCapBytes overrides cfg.MaxSizeMB in tests. 0 means use cfg.
	testCapBytes int64

	closeOnce      sync.Once
	pendingWg      sync.WaitGroup
	userStateTurns int

	recentUserMessages []string
	recentMu           sync.Mutex

	// lastFollowUps holds the high-confidence predicted intents captured
	// during the most recent recall, surfaced as follow-up suggestions.
	followUpsMu   sync.Mutex
	lastFollowUps []PredictedIntent

	// nudgeMu serializes TakeNudges so concurrent takers cannot both spend
	// the same daily budget.
	nudgeMu sync.Mutex

	inferenceMu  sync.Mutex
	closed       bool
	inferRunning bool

	// stats holds the atomic recall-failure counters backing Stats().
	stats recallStats
}

const recentUserMessageLimit = 10

// New creates an ExtendedMemory instance rooted at dir.
func New(dir string, llm LLMClient, cfg Config) *ExtendedMemory {
	cfg = Resolve(cfg)
	store := NewAtomStore(dir)
	newEmb := func() embedding.TextEmbedder {
		return embedding.New(cfg.Embedding, vectorDim)
	}
	index := newAtomVectorIndex(dir, newEmb, func() ([]MemoryAtom, error) {
		return store.List()
	})
	em := &ExtendedMemory{
		cfg:        cfg,
		dir:        dir,
		store:      store,
		index:      index,
		extractor:  NewExtractor(llm, cfg),
		recall:     NewRecall(store, index, llm, cfg),
		evictor:    newEvictor(cfg),
		quarantine: NewQuarantine(dir),
		userModel:  NewUserModelWithStore(dir, llm, cfg),
		assoc:      NewAssociationsWithDir(dir),
		predictor:  NewPredictor(llm, cfg),
		llm:        llm,
	}
	em.recall.SetPredictor(em.predictor)
	em.recall.SetFollowUpSink(em.setLastFollowUps)
	em.recall.stats = &em.stats
	_ = em.userModel.Load()
	em.quarantine.SetTTLDays(cfg.QuarantineTTLDays)
	if removed, err := em.quarantine.EvictExpired(cfg.QuarantineTTLDays); err != nil {
		log.Printf("extended memory: startup quarantine eviction failed: %v", err)
	} else if removed > 0 {
		log.Printf("extended memory: evicted %d expired quarantined atom(s) at startup", removed)
	}
	return em
}

// Enabled reports whether Extended Memory is active.
func (em *ExtendedMemory) Enabled() bool {
	return em != nil && em.cfg.Enabled != nil && *em.cfg.Enabled
}

// SetGuard installs the shared prompt-injection detector and propagates it to
// the user-model and recall sub-components. The extractor is deliberately not
// guarded: atoms are scanned once at persistence time in addAtom, which
// quarantines rejections for human review instead of dropping them.
func (em *ExtendedMemory) SetGuard(g guard.Guard, cfg guard.Config) {
	if em == nil {
		return
	}
	em.guard = g
	em.guardCfg = cfg
	if em.userModel != nil {
		em.userModel.SetGuard(g, cfg)
	}
	if em.recall != nil {
		em.recall.SetGuard(g, cfg)
	}
}

// scanContent runs the guard against an extended-memory-scoped input.
func (em *ExtendedMemory) scanContent(ctx context.Context, content string) error {
	if err := guard.ScanContentWithScope(ctx, content, em.guard, &em.guardCfg, "memory"); err != nil {
		return fmt.Errorf("extended memory: %v", err)
	}
	return nil
}

// SetSessionContext sets the current session and project identifiers.
func (em *ExtendedMemory) SetSessionContext(sessionID, project string) {
	if em == nil {
		return
	}
	em.mu.Lock()
	defer em.mu.Unlock()
	em.session = sessionID
	em.project = project
}

// setLastFollowUps replaces the captured follow-up suggestions. It is the
// sink wired into Recall so every recall refreshes the list; a nil slice
// clears it.
func (em *ExtendedMemory) setLastFollowUps(intents []PredictedIntent) {
	em.followUpsMu.Lock()
	defer em.followUpsMu.Unlock()
	em.lastFollowUps = intents
}

// LastFollowUps returns a copy of the predicted follow-up intents captured
// during the most recent recall, or nil when none were captured.
func (em *ExtendedMemory) LastFollowUps() []PredictedIntent {
	if em == nil {
		return nil
	}
	em.followUpsMu.Lock()
	defer em.followUpsMu.Unlock()
	out := make([]PredictedIntent, len(em.lastFollowUps))
	copy(out, em.lastFollowUps)
	return out
}

// AddAtom manually adds an atom. Manual adds are treated as user-approved.
func (em *ExtendedMemory) AddAtom(ctx context.Context, atom MemoryAtom) error {
	return em.addAtoms(ctx, []MemoryAtom{atom}, false)
}

// addAtoms is the persistence path for all atoms. The guard scan runs before
// anything is stored, regardless of trust boundary. A scan rejection does NOT
// drop the atom: it is quarantined with a scan_rejected reason so a human can
// review guard false positives (odek memory extended quarantine/promote)
// instead of silently losing memories. skipScan is reserved for PromoteAtom,
// where a human has explicitly approved the atom after review — without the
// bypass, a guard-rejected atom could never be promoted.
//
// The vector index is marked dirty once for the whole batch and association
// building runs only after every atom is stored, so a turn that extracts M
// atoms costs a single index rebuild instead of M.
func (em *ExtendedMemory) addAtoms(ctx context.Context, atoms []MemoryAtom, skipScan bool) error {
	if em == nil || !em.Enabled() {
		return fmt.Errorf("extended memory: disabled")
	}
	dedupThreshold := float32(0)
	if em.cfg.SemanticDedupThreshold != nil {
		dedupThreshold = *em.cfg.SemanticDedupThreshold
	}
	if dedupThreshold > 0 {
		// Refresh the index once for the whole batch so semantic dedup
		// searches see the pre-batch corpus. This is a no-op when the index
		// is already fresh, so the one-rebuild-per-batch invariant (the
		// single markDirty after the adds) is preserved.
		em.index.ensureFresh()
	}
	var firstErr error
	stored := make([]MemoryAtom, 0, len(atoms))
	for _, atom := range atoms {
		NormalizeAtom(&atom)
		if atom.SourceClass == SourceUserSaid {
			// Manual addition through the tool/API is explicitly approved.
			atom.SourceClass = SourceUserApproved
		}
		if atom.ID == "" {
			id, err := generateAtomID()
			if err != nil {
				return fmt.Errorf("extended memory: generate id: %w", err)
			}
			atom.ID = id
		}

		em.mu.RLock()
		atom.Context.SessionID = em.session
		atom.Context.Project = em.project
		em.mu.RUnlock()

		// Security scan before persistence, regardless of trust boundary.
		if !skipScan {
			if err := em.scanContent(ctx, atom.Text); err != nil {
				reason := "scan_rejected: " + err.Error()
				if len(reason) > 200 {
					reason = reason[:200]
				}
				// Quarantine counts toward max_size_mb, so the cap must be
				// enforced on this path too, or a rejection storm can wedge
				// the store (the evictor only evicts trusted atoms).
				if cerr := em.enforceCap(ctx, projectedAtomSize(atom)); cerr != nil {
					log.Printf("extended memory: cap enforcement failed: %v", cerr)
					if firstErr == nil {
						firstErr = cerr
					}
					continue
				}
				if qerr := em.quarantine.StoreWithReason(atom, reason); qerr != nil {
					log.Printf("extended memory: quarantine store failed: %v", qerr)
					if firstErr == nil {
						firstErr = qerr
					}
					continue
				}
				log.Printf("extended memory: atom quarantined after guard rejection: %v", err)
				continue
			}
		}

		incoming := projectedAtomSize(atom)
		if err := em.enforceCap(ctx, incoming); err != nil {
			log.Printf("extended memory: cap enforcement failed: %v", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		// Tainted atoms go to quarantine instead of the live store.
		if IsTaintedSourceClass(atom.SourceClass) {
			if err := em.quarantine.Store(atom); err != nil {
				log.Printf("extended memory: quarantine store failed: %v", err)
				if firstErr == nil {
					firstErr = err
				}
			}
			continue
		}

		if err := em.ensureDir(); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// Exact-match dedup: a re-stated fact refreshes the existing live atom
		// (new CreatedAt, higher confidence, original ID) instead of
		// appending a duplicate with a fresh random ID. When no exact match
		// exists, a semantic tier refreshes a near-duplicate live atom whose
		// similarity reaches semantic_dedup_threshold.
		if existing, ok, err := em.findDuplicateAtom(atom); err != nil {
			log.Printf("extended memory: dedup lookup failed: %v", err)
		} else if ok {
			atom = refreshDuplicate(existing, atom)
		} else if dedupThreshold > 0 {
			if existing, found := em.findSemanticDuplicate(atom, dedupThreshold, stored); found {
				atom = refreshDuplicate(existing, atom)
			}
		}
		if err := em.store.Add(atom, em.cfg.AtomMaxChars); err != nil {
			log.Printf("extended memory: atom store add failed: %v", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		em.userModel.Update(atom)
		stored = append(stored, atom)
	}
	if len(stored) == 0 {
		return firstErr
	}

	// One index invalidation for the whole batch: the first association
	// search below triggers a single rebuild that already sees every new
	// atom, and subsequent searches reuse the fresh index.
	em.index.markDirty()
	if em.cfg.AssociationsEnabled != nil && *em.cfg.AssociationsEnabled {
		for _, atom := range stored {
			em.buildAssociations(atom)
			atom.Context.RelatedAtomIDs = em.assoc.Related(atom.ID)
			// Metadata-only update: must NOT re-dirty the index.
			if err := em.store.Add(atom, em.cfg.AtomMaxChars); err != nil {
				log.Printf("extended memory: association context update failed: %v", err)
			}
		}
		_ = em.assoc.Persist()
	}
	return firstErr
}

// refreshDuplicate merges an incoming atom into the existing live atom it
// duplicates: the original ID is kept, CreatedAt is refreshed, and the higher
// confidence wins.
func refreshDuplicate(existing, incoming MemoryAtom) MemoryAtom {
	existing.CreatedAt = incoming.CreatedAt
	if incoming.Confidence > existing.Confidence {
		existing.Confidence = incoming.Confidence
	}
	return existing
}

// findSemanticDuplicate returns the live atom most similar to atom when its
// cosine similarity reaches threshold. The vector index (refreshed once per
// batch) covers atoms stored before this batch; atoms stored earlier in the
// same batch are not yet indexed, so they are compared by embedding both
// texts directly.
func (em *ExtendedMemory) findSemanticDuplicate(atom MemoryAtom, threshold float32, batchStored []MemoryAtom) (MemoryAtom, bool) {
	best := threshold
	var match MemoryAtom
	found := false
	for _, c := range em.index.searchCurrent(atom.Text, 1) {
		if c.Score < best {
			continue
		}
		existing, err := em.store.Get(c.ID)
		if err != nil {
			continue // stale index entry (evicted/removed atom)
		}
		best, match, found = c.Score, existing, true
	}
	for _, prev := range batchStored {
		if score := em.index.similarity(prev.Text, atom.Text); score >= best {
			best, match, found = score, prev, true
		}
	}
	return match, found
}

// findDuplicateAtom returns the live atom with the same normalized text as
// atom, if any. Exact normalized match only — no similarity search.
func (em *ExtendedMemory) findDuplicateAtom(atom MemoryAtom) (MemoryAtom, bool, error) {
	atoms, err := em.store.List()
	if err != nil {
		return MemoryAtom{}, false, err
	}
	want := normalizeAtomText(atom.Text)
	for _, a := range atoms {
		if normalizeAtomText(a.Text) == want {
			return a, true, nil
		}
	}
	return MemoryAtom{}, false, nil
}

// UserStateStyle returns the inferred style state for style mirroring, or nil
// if style mirroring is disabled or no style has been inferred.
func (em *ExtendedMemory) UserStateStyle() *StyleState {
	if em == nil || !em.Enabled() || em.userModel == nil {
		return nil
	}
	if em.cfg.StyleMirroringEnabled == nil || !*em.cfg.StyleMirroringEnabled {
		return nil
	}
	style := em.userModel.State().Style
	if styleEmpty(style) {
		return nil
	}
	return &style
}

// projectedAtomSize estimates the on-disk bytes this atom will consume.
func projectedAtomSize(atom MemoryAtom) int64 {
	return int64(len(atom.Text)) + 256
}

// AddAtoms adds multiple atoms in one call. It batches embeddings indirectly
// by marking the index dirty once at the end. Per-atom failures are logged
// and tolerated: the remaining atoms are still stored.
func (em *ExtendedMemory) AddAtoms(ctx context.Context, atoms []MemoryAtom) error {
	if em == nil || !em.Enabled() {
		return fmt.Errorf("extended memory: disabled")
	}
	if len(atoms) == 0 {
		return nil
	}
	if err := em.addAtoms(ctx, atoms, false); err != nil {
		log.Printf("extended memory: batch add incomplete: %v", err)
	}
	return nil
}

// buildAssociations links a new atom to related atoms.
func (em *ExtendedMemory) buildAssociations(atom MemoryAtom) {
	if em.assoc == nil {
		return
	}
	atoms, err := em.store.List()
	if err != nil {
		log.Printf("extended memory: list atoms for associations failed: %v", err)
		return
	}
	// Temporal: adjacent turns in the same session.
	for _, other := range atoms {
		if other.ID == atom.ID {
			continue
		}
		if other.Context.SessionID == "" || other.Context.SessionID != atom.Context.SessionID {
			continue
		}
		if abs(other.Context.Turn-atom.Context.Turn) <= 2 {
			em.assoc.Link(atom.ID, other.ID)
		}
	}
	// Task: same project and durable task-related types.
	taskTypes := map[string]bool{TypeGoal: true, TypeDecision: true, TypeConvention: true, TypeIntent: true}
	if atom.Context.Project != "" && taskTypes[atom.Type] {
		for _, other := range atoms {
			if other.ID == atom.ID {
				continue
			}
			if other.Context.Project == atom.Context.Project && taskTypes[other.Type] {
				em.assoc.Link(atom.ID, other.ID)
			}
		}
	}
	// Semantic: top-K cosine neighbours.
	k := em.cfg.AssociationSemanticTopK
	if k > 0 && em.index != nil {
		candidates := em.index.search(atom.Text, k+1)
		for _, c := range candidates {
			if c.ID == atom.ID {
				continue
			}
			if c.Score >= em.cfg.SemanticSearchMinScore {
				em.assoc.Link(atom.ID, c.ID)
			}
		}
	}
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// inferUserState runs the background user-model inference goroutine.
func (em *ExtendedMemory) inferUserState(ctx context.Context) {
	if em == nil || em.userModel == nil {
		return
	}
	if err := em.userModel.Infer(ctx); err != nil {
		log.Printf("extended memory: user-state inference failed: %v", err)
	}
}

// triggerBackgroundInference starts a goroutine to infer the user model if
// the turn interval is reached or the focus shifted. Only one inference runs
// at a time: concurrent triggers are coalesced so overlapping Infer runs
// cannot pile up duplicate pending-review entries.
func (em *ExtendedMemory) triggerBackgroundInference() {
	if em == nil || !em.Enabled() || em.userModel == nil || !em.userModel.Enabled() {
		return
	}
	em.inferenceMu.Lock()
	if em.closed || em.inferRunning {
		em.inferenceMu.Unlock()
		return
	}
	em.inferRunning = true
	em.pendingWg.Add(1)
	em.inferenceMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	go func() {
		defer func() {
			cancel()
			em.inferenceMu.Lock()
			em.inferRunning = false
			em.inferenceMu.Unlock()
			em.pendingWg.Done()
		}()
		em.inferUserState(ctx)
	}()
}

// ConfirmPendingReview applies a pending review to the user model.
func (em *ExtendedMemory) ConfirmPendingReview(id string) error {
	if em == nil || !em.Enabled() {
		return fmt.Errorf("extended memory: disabled")
	}
	if em.userModel == nil {
		return fmt.Errorf("extended memory: user model not initialized")
	}
	return em.userModel.ConfirmPendingReview(id)
}

// RejectPendingReview removes a pending review from the user model.
func (em *ExtendedMemory) RejectPendingReview(id string) error {
	if em == nil || !em.Enabled() {
		return fmt.Errorf("extended memory: disabled")
	}
	if em.userModel == nil {
		return fmt.Errorf("extended memory: user model not initialized")
	}
	return em.userModel.RejectPendingReview(id)
}

// ListPendingReview lists pending user-model inferences.
func (em *ExtendedMemory) ListPendingReview() ([]PendingReview, error) {
	if em == nil {
		return nil, nil
	}
	if em.userModel == nil {
		return nil, nil
	}
	return em.userModel.ListPendingReview(), nil
}

// FormatUserStateContext returns formatted user-model context.
func (em *ExtendedMemory) FormatUserStateContext(ctx context.Context) string {
	if em == nil || !em.Enabled() || em.userModel == nil {
		return ""
	}
	return em.userModel.Summary()
}

// ReturnAfterBreak generates a resume summary from recent atoms and the user
// model. It returns empty string if there is no data or the feature is disabled.
func (em *ExtendedMemory) ReturnAfterBreak(ctx context.Context) string {
	if em == nil || !em.Enabled() || em.userModel == nil || em.llm == nil {
		return ""
	}
	if em.cfg.ProactiveReturnAfterBreak == nil || !*em.cfg.ProactiveReturnAfterBreak {
		return ""
	}
	atoms, err := em.store.List()
	if err != nil {
		log.Printf("extended memory: return after break list failed: %v", err)
		return ""
	}
	// Use the most recent trusted atoms (up to 5). List returns newest first.
	var recent []MemoryAtom
	for i := 0; i < len(atoms) && len(recent) < 5; i++ {
		if IsTaintedSourceClass(atoms[i].SourceClass) {
			continue
		}
		recent = append(recent, atoms[i])
	}
	if len(recent) == 0 {
		return ""
	}
	state := em.userModel.State()
	stateJSON, _ := json.Marshal(state)
	recentJSON, _ := json.Marshal(recent)
	prompt := fmt.Sprintf(`You are a context-resumption system. The user is returning after a break. Summarize where they left off and what the next likely step is, based on the recent atoms and user model. Be concise (1-2 sentences). Return only the summary.

Recent atoms: %s
User model: %s`, recentJSON, stateJSON)
	resp, err := em.llm.SimpleCall(ctx,
		"You are a context-resumption system. Return only a concise summary.",
		prompt,
	)
	if err != nil {
		log.Printf("extended memory: return after break LLM failed: %v", err)
		return ""
	}
	resp = strings.TrimSpace(resp)
	if resp == "" {
		return ""
	}
	return "\n═══ WHERE YOU LEFT OFF ═══\n" + resp + "\n─────────────────────────\n"
}

var pronounRE = regexp.MustCompile(`(?i)\b(it|that|this|them|those)\b`)

// AnaphoraResolve replaces the first pronoun in a user message with the
// most likely antecedent from recent trusted atoms when the semantic score
// is high enough. It returns the resolved message and true when a replacement
// happened, otherwise the original message and false.
func (em *ExtendedMemory) AnaphoraResolve(ctx context.Context, msg string) (string, bool) {
	if em == nil || !em.Enabled() || em.recall == nil || em.cfg.AnaphoraResolutionEnabled == nil || !*em.cfg.AnaphoraResolutionEnabled {
		return msg, false
	}
	if !pronounRE.MatchString(msg) {
		return msg, false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// queryAtomsScored already filters by min score and ranks by the blended
	// similarity/retention score, so the top candidate is the antecedent.
	scored, err := em.recall.queryAtomsScored(ctx, msg, false)
	if err != nil || len(scored) == 0 {
		return msg, false
	}

	loc := pronounRE.FindStringIndex(msg)
	if loc == nil {
		return msg, false
	}
	resolved := msg[:loc[0]] + scored[0].atom.Text + msg[loc[1]:]
	if err := em.scanContent(context.Background(), resolved); err != nil {
		log.Printf("extended memory: anaphora resolution rejected by scan: %v", err)
		return msg, false
	}
	return resolved, true
}

// openLoopTypes are the atom types that represent unresolved threads: user
// questions that went unanswered and stated goals/intentions.
var openLoopTypes = map[string]bool{
	TypeQuestion: true,
	TypeGoal:     true,
	TypeIntent:   true,
}

// OpenLoops returns trusted question/goal/intent atoms, newest first, capped
// at limit (limit <= 0 returns all). It lists the store directly instead of
// running a semantic query: open loops are a recency-ordered data view, so
// the embedding search, min-score filtering, and LLM rerank of the recall
// pipeline would only add cost without improving the answer.
func (em *ExtendedMemory) OpenLoops(ctx context.Context, limit int) ([]MemoryAtom, error) {
	if em == nil || !em.Enabled() {
		return nil, nil
	}
	atoms, err := em.store.List() // newest first
	if err != nil {
		return nil, fmt.Errorf("extended memory: open loops list: %w", err)
	}
	out := make([]MemoryAtom, 0, len(atoms))
	for _, atom := range atoms {
		if !openLoopTypes[atom.Type] || IsTaintedSourceClass(atom.SourceClass) {
			continue
		}
		out = append(out, atom)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// SearchAtoms performs an explicit semantic search and returns ranked atoms.
func (em *ExtendedMemory) SearchAtoms(ctx context.Context, query string) ([]MemoryAtom, error) {
	if em == nil {
		return nil, nil
	}
	atoms, err := em.recall.queryAtoms(ctx, query)
	if err != nil {
		log.Printf("extended memory: search_atoms failed: %v", err)
		return nil, err
	}
	for i := range atoms {
		atoms[i].Context.RelatedAtomIDs = em.assoc.Related(atoms[i].ID)
	}
	return atoms, nil
}

// ForgetAtom removes an atom by ID from both the live store and quarantine.
// An atom that exists in only one of them is still reported as forgotten; an
// error is returned only when the ID is found in neither.
func (em *ExtendedMemory) ForgetAtom(id string) error {
	if em == nil || !em.Enabled() {
		return fmt.Errorf("extended memory: disabled")
	}
	storeErr := em.store.Remove(id)
	quarantineErr := em.quarantine.Forget(id)
	if storeErr != nil && quarantineErr != nil {
		return storeErr
	}
	em.assoc.RemoveAtom(id)
	_ = em.assoc.Persist()
	em.index.markDirty()
	return nil
}

// PromoteAtom moves an atom from quarantine into the live store with
// SourceUserApproved. This is the human-gated escape hatch for tainted and
// guard-rejected atoms. The guard rescan is skipped: the human review IS the
// approval, and a rescan would reject guard false positives again.
func (em *ExtendedMemory) PromoteAtom(id string) error {
	if em == nil || !em.Enabled() {
		return fmt.Errorf("extended memory: disabled")
	}
	atom, err := em.quarantine.Promote(id)
	if err != nil {
		return err
	}
	atom.SourceClass = SourceUserApproved
	if err := em.addAtoms(context.Background(), []MemoryAtom{atom}, true); err != nil {
		return err
	}
	_ = em.quarantine.Forget(id)
	em.index.markDirty()
	return nil
}

// PinAtom pins a live atom by ID so it is never evicted.
func (em *ExtendedMemory) PinAtom(id string) error {
	if em == nil || !em.Enabled() {
		return fmt.Errorf("extended memory: disabled")
	}
	return em.store.Pin(id, true)
}

// FormatExtendedContext returns formatted Extended Memory context for the
// query, or empty string if nothing matches or Extended Memory is disabled.
func (em *ExtendedMemory) FormatExtendedContext(ctx context.Context, query string) string {
	if em == nil || !em.Enabled() {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	em.recentMu.Lock()
	recent := make([]string, len(em.recentUserMessages))
	copy(recent, em.recentUserMessages)
	em.recentMu.Unlock()

	context, err := em.recall.Query(ctx, query, recent, em.userModel.State())
	if err != nil {
		log.Printf("extended memory: format context failed: %v", err)
		return ""
	}
	return context
}

// FormatContext is an alias for FormatExtendedContext.
func (em *ExtendedMemory) FormatContext(ctx context.Context, query string) string {
	return em.FormatExtendedContext(ctx, query)
}

// OnUserMessage extracts atoms from a user message and stores them.
func (em *ExtendedMemory) OnUserMessage(ctx AtomContext, msg string) {
	if em == nil || !em.Enabled() {
		return
	}

	em.mu.Lock()
	em.lastUser = msg
	if ctx.SessionID != "" {
		em.session = ctx.SessionID
	}
	if ctx.Project != "" {
		em.project = ctx.Project
	}
	em.userStateTurns++
	triggerInference := em.userModel.Enabled() && (em.userStateTurns%em.cfg.UserStateTurnInterval == 0 || em.userModel.FocusChanged())
	if em.userModel.FocusChanged() {
		em.userModel.ResetFocusChanged()
	}
	em.mu.Unlock()

	em.recentMu.Lock()
	em.recentUserMessages = append(em.recentUserMessages, msg)
	if len(em.recentUserMessages) > recentUserMessageLimit {
		em.recentUserMessages = em.recentUserMessages[len(em.recentUserMessages)-recentUserMessageLimit:]
	}
	em.recentMu.Unlock()

	if em.cfg.AutoExtractPerTurn == nil || !*em.cfg.AutoExtractPerTurn {
		return
	}

	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	atoms, err := em.extractor.Extract(c, msg)
	if err != nil {
		log.Printf("extended memory: user message extraction failed: %v", err)
		return
	}
	for i := range atoms {
		atoms[i].Context = ctx
	}
	if err := em.addAtoms(c, atoms, false); err != nil {
		log.Printf("extended memory: batch atom add failed: %v", err)
	}

	if triggerInference {
		em.triggerBackgroundInference()
	}
}

// enforceCap evicts atoms if adding newBytes would exceed max_size_mb.
func (em *ExtendedMemory) enforceCap(ctx context.Context, newBytes int64) error {
	var maxBytes int64
	if em.testCapBytes > 0 {
		maxBytes = em.testCapBytes
	} else {
		maxBytes = int64(em.cfg.MaxSizeMB) * 1024 * 1024
	}
	if maxBytes <= 0 {
		return nil
	}

	// Evict expired quarantine entries first.
	if removed, err := em.quarantine.EvictExpired(em.cfg.QuarantineTTLDays); err != nil {
		log.Printf("extended memory: quarantine eviction failed: %v", err)
	} else if removed > 0 {
		log.Printf("extended memory: evicted %d expired quarantined atom(s)", removed)
	}

	quarantineSize, err := em.quarantine.Size()
	if err != nil {
		log.Printf("extended memory: quarantine size failed: %v", err)
		quarantineSize = 0
	}

	atoms, err := em.store.List()
	if err != nil {
		log.Printf("extended memory: list atoms for eviction failed: %v", err)
		return fmt.Errorf("extended memory: list atoms: %w", err)
	}
	sized := buildSizedAtoms(em.store, atoms)
	// Include amortized vector cost in each atom's footprint.
	for i := range sized {
		sized[i].size += vectorCost(len(atoms))
	}

	var existingEffective int64
	for _, s := range sized {
		existingEffective += s.size
	}
	total := existingEffective + quarantineSize + newBytes

	if total <= maxBytes {
		return nil
	}

	need := total - maxBytes + 4096 // headroom
	before := len(atoms)
	ids, _, ok := em.evictor.SelectForEviction(sized, need)
	if !ok {
		return fmt.Errorf("extended memory: cannot free %s; all atoms are pinned or no evictable atoms exist", sizeLabel(need))
	}
	for _, id := range ids {
		_ = em.store.Remove(id)
		em.assoc.RemoveAtom(id)
	}
	if len(ids) > 0 {
		em.index.markDirty()
		_ = em.assoc.Persist()
		log.Printf("extended memory: evicted %d atom(s) to stay under %s cap", len(ids), sizeLabel(maxBytes))
		// Trigger background compaction if we removed more than 10%.
		if float64(len(ids)) > 0.1*float64(before) {
			em.index.Compact()
		}
	}

	return nil
}

// SetEmbedderFactory overrides the embedder factory used by the vector index.
func (em *ExtendedMemory) SetEmbedderFactory(fn func() embedding.TextEmbedder) {
	if em == nil || em.index == nil {
		return
	}
	em.index.newEmb = fn
}

// SetEmbedder overrides the active embedder used by the vector index.
func (em *ExtendedMemory) SetEmbedder(emb embedding.TextEmbedder) {
	if em == nil || em.index == nil {
		return
	}
	em.index.emb = emb
}

// MarkDirty marks the vector index as needing a rebuild.
func (em *ExtendedMemory) MarkDirty() {
	if em == nil || em.index == nil {
		return
	}
	em.index.markDirty()
}

// Compact triggers a background compaction of the vector index.
func (em *ExtendedMemory) Compact() {
	if em == nil {
		return
	}
	em.index.Compact()
}

// Close waits for background operations to finish. It is safe to call
// multiple times.
func (em *ExtendedMemory) Close() error {
	if em == nil {
		return nil
	}
	em.closeOnce.Do(func() {
		em.inferenceMu.Lock()
		em.closed = true
		em.inferenceMu.Unlock()
		em.index.Wait()
		em.pendingWg.Wait()
	})
	return nil
}

// Size returns the current on-disk size of the Extended Memory store.
func (em *ExtendedMemory) Size() int64 {
	if em == nil {
		return 0
	}
	storeSize, _ := em.store.Size()
	quarantineSize, _ := em.quarantine.Size()
	indexSize := em.index.Size()
	return storeSize + quarantineSize + indexSize
}

// List returns all stored atoms (trusted only; quarantined atoms are separate).
func (em *ExtendedMemory) List() ([]MemoryAtom, error) {
	if em == nil {
		return nil, nil
	}
	return em.store.List()
}

// ListQuarantine returns all quarantined atoms.
func (em *ExtendedMemory) ListQuarantine() ([]MemoryAtom, error) {
	if em == nil {
		return nil, nil
	}
	return em.quarantine.List()
}

// ListQuarantineEntries returns all quarantined atoms with their review
// metadata (quarantine time and reason).
func (em *ExtendedMemory) ListQuarantineEntries() ([]QuarantinedAtom, error) {
	if em == nil {
		return nil, nil
	}
	return em.quarantine.ListEntries()
}

// ensureDir creates the Extended Memory directory with restricted permissions.
func (em *ExtendedMemory) ensureDir() error {
	return os.MkdirAll(em.dir, 0700)
}
