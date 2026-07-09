package extended

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/embedding"
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
	llm        LLMClient

	dir      string
	mu       sync.RWMutex
	session  string
	project  string
	lastUser string

	// testCapBytes overrides cfg.MaxSizeMB in tests. 0 means use cfg.
	testCapBytes int64
}

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
	return &ExtendedMemory{
		cfg:        cfg,
		dir:        dir,
		store:      store,
		index:      index,
		extractor:  NewExtractor(llm, cfg),
		recall:     NewRecall(store, index, llm, cfg),
		evictor:    newEvictor(cfg),
		quarantine: NewQuarantine(dir),
		userModel:  NewUserModel(),
		assoc:      NewAssociations(),
		llm:        llm,
	}
}

// Enabled reports whether Extended Memory is active.
func (em *ExtendedMemory) Enabled() bool {
	return em != nil && em.cfg.Enabled != nil && *em.cfg.Enabled
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

// AddAtom manually adds an atom. Manual adds are treated as user-approved.
func (em *ExtendedMemory) AddAtom(ctx context.Context, atom MemoryAtom) error {
	if em == nil {
		return fmt.Errorf("extended memory: disabled")
	}
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

	// Tainted atoms go to quarantine instead of the live store.
	if IsTaintedSourceClass(atom.SourceClass) {
		if err := em.quarantine.Store(atom); err != nil {
			log.Printf("extended memory: quarantine store failed: %v", err)
			return err
		}
		em.enforceCap(ctx)
		return nil
	}

	if err := ScanContent(atom.Text); err != nil {
		return err
	}
	if err := em.ensureDir(); err != nil {
		return err
	}
	if err := em.enforceCap(ctx); err != nil {
		log.Printf("extended memory: cap enforcement failed: %v", err)
		return err
	}
	if err := em.store.Add(atom, em.cfg.AtomMaxChars); err != nil {
		log.Printf("extended memory: atom store add failed: %v", err)
		return err
	}
	em.index.markDirty()
	em.userModel.Update(atom)
	return nil
}

// AddAtoms adds multiple atoms in one call. It batches embeddings indirectly
// by marking the index dirty once at the end.
func (em *ExtendedMemory) AddAtoms(ctx context.Context, atoms []MemoryAtom) error {
	if em == nil {
		return fmt.Errorf("extended memory: disabled")
	}
	if len(atoms) == 0 {
		return nil
	}
	for _, atom := range atoms {
		if err := em.AddAtom(ctx, atom); err != nil {
			log.Printf("extended memory: batch add failed for atom %s: %v", atom.ID, err)
		}
	}
	em.index.markDirty()
	return nil
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
	return atoms, nil
}

// ForgetAtom removes an atom by ID from both the live store and quarantine.
func (em *ExtendedMemory) ForgetAtom(id string) error {
	if em == nil {
		return fmt.Errorf("extended memory: disabled")
	}
	if err := em.store.Remove(id); err != nil {
		_ = em.quarantine.Forget(id)
		return err
	}
	em.index.markDirty()
	return nil
}

// FormatExtendedContext returns formatted Extended Memory context for the
// query, or empty string if nothing matches or Extended Memory is disabled.
func (em *ExtendedMemory) FormatExtendedContext(query string) string {
	if em == nil || !em.Enabled() {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	context, err := em.recall.Query(ctx, query)
	if err != nil {
		log.Printf("extended memory: format context failed: %v", err)
		return ""
	}
	return context
}

// FormatContext is an alias for FormatExtendedContext.
func (em *ExtendedMemory) FormatContext(ctx context.Context, query string) string {
	return em.FormatExtendedContext(query)
}

// OnUserMessage extracts atoms from a user message and stores them.
func (em *ExtendedMemory) OnUserMessage(ctx AtomContext, msg string) {
	if em == nil || !em.Enabled() {
		return
	}
	if em.cfg.AutoExtractPerTurn == nil || !*em.cfg.AutoExtractPerTurn {
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
	em.mu.Unlock()

	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	atoms, err := em.extractor.Extract(c, msg)
	if err != nil {
		log.Printf("extended memory: user message extraction failed: %v", err)
		return
	}
	for _, atom := range atoms {
		atom.Context = ctx
		_ = em.AddAtom(c, atom)
	}
}

// enforceCap evicts atoms if adding newBytes would exceed max_size_mb.
func (em *ExtendedMemory) enforceCap(ctx context.Context) error {
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

	storeSize, err := em.store.Size()
	if err != nil {
		log.Printf("extended memory: store size failed: %v", err)
		storeSize = 0
	}
	quarantineSize, err := em.quarantine.Size()
	if err != nil {
		log.Printf("extended memory: quarantine size failed: %v", err)
		quarantineSize = 0
	}
	indexSize := em.index.Size()
	total := storeSize + quarantineSize + indexSize

	if total <= maxBytes {
		return nil
	}

	need := total - maxBytes + 4096 // headroom
	atoms, err := em.store.List()
	if err != nil {
		log.Printf("extended memory: list atoms for eviction failed: %v", err)
		return nil
	}
	sized := buildSizedAtoms(em.store, atoms)
	// Include amortized vector cost in each atom's footprint.
	for i := range sized {
		sized[i].size += vectorCost(len(atoms))
	}

	before := len(atoms)
	ids := em.evictor.SelectForEviction(sized, need)
	for _, id := range ids {
		_ = em.store.Remove(id)
		em.index.markDirty()
	}
	if len(ids) > 0 {
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

// ensureDir creates the Extended Memory directory with restricted permissions.
func (em *ExtendedMemory) ensureDir() error {
	return os.MkdirAll(em.dir, 0700)
}
