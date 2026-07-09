package memory

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/BackendStack21/go-vector/pkg/vector"
	"github.com/BackendStack21/odek/internal/embedding"
	"github.com/BackendStack21/odek/internal/memory/extended"
)

func TestMemoryToolName(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), nil, DefaultMemoryConfig())
	tool := NewMemoryTool(mm)

	if tool.Name() != "memory" {
		t.Errorf("expected 'memory', got %q", tool.Name())
	}
}

func TestMemoryToolSchema(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), nil, DefaultMemoryConfig())
	tool := NewMemoryTool(mm)

	schema := tool.Schema()
	if schema == nil {
		t.Fatal("schema is nil")
	}
}

func TestMemoryToolAddAndRead(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), nil, DefaultMemoryConfig())
	tool := NewMemoryTool(mm)

	// Add
	result, err := tool.Call(`{"action":"add","target":"user","content":"User likes Go"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "true") {
		t.Errorf("expected success, got %q", result)
	}

	// Read
	result, err = tool.Call(`{"action":"read"}`)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatal(err)
	}
	if !parsed.Success {
		t.Errorf("expected success, got %q", result)
	}
	if !strings.Contains(parsed.Message, "User Profile") {
		t.Errorf("expected User Profile section, got %q", parsed.Message)
	}
}

func TestMemoryToolReplace(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), nil, DefaultMemoryConfig())
	tool := NewMemoryTool(mm)

	tool.Call(`{"action":"add","target":"user","content":"User likes Go"}`)
	result, err := tool.Call(`{"action":"replace","target":"user","old_text":"Go","content":"User prefers Rust"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "true") {
		t.Errorf("expected success, got %q", result)
	}

	// Verify via read
	user, _, _ := mm.ReadFacts()
	if !strings.Contains(user, "Rust") {
		t.Errorf("expected Rust, got %q", user)
	}
}

func TestMemoryToolRemove(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), nil, DefaultMemoryConfig())
	tool := NewMemoryTool(mm)

	tool.Call(`{"action":"add","target":"user","content":"entry to remove"}`)
	result, err := tool.Call(`{"action":"remove","target":"user","old_text":"to remove"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "true") {
		t.Errorf("expected success, got %q", result)
	}

	user, _, _ := mm.ReadFacts()
	if user != "" {
		t.Errorf("expected empty after remove, got %q", user)
	}
}

func TestMemoryToolMissingContent(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), nil, DefaultMemoryConfig())
	tool := NewMemoryTool(mm)

	result, err := tool.Call(`{"action":"add","target":"user"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "false") {
		t.Errorf("expected failure, got %q", result)
	}
}

func TestMemoryToolMissingOldText(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), nil, DefaultMemoryConfig())
	tool := NewMemoryTool(mm)

	result, err := tool.Call(`{"action":"remove","target":"user"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "false") {
		t.Errorf("expected failure, got %q", result)
	}
}

func TestMemoryToolBadAction(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), nil, DefaultMemoryConfig())
	tool := NewMemoryTool(mm)

	result, err := tool.Call(`{"action":"nonexistent"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "false") {
		t.Errorf("expected failure, got %q", result)
	}
}

func TestMemoryToolSearch(t *testing.T) {
	dir := t.TempDir()
	llm := &mockLLM{
		responses: map[string]string{
			"sess-001": "found auth fix",
			"sess-002": "query results",
		},
	}

	// Pre-populate episodes directly
	es := NewEpisodeStore(dir, func(query string, episodes []EpisodeMeta) ([]EpisodeMeta, error) {
		return episodes, nil
	})
	es.Write("sess-001", "fixed auth token validation", 5)

	mm := NewMemoryManager(dir, llm, DefaultMemoryConfig())
	mm.episodes = es

	tool := NewMemoryTool(mm)
	result, err := tool.Call(`{"action":"search","query":"auth"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "sess-001") {
		t.Errorf("expected sess-001 in results, got %q", result)
	}
}

func TestMemoryToolConsolidate(t *testing.T) {
	dir := t.TempDir()
	llm := &mockLLM{
		responses: map[string]string{
			"Consolidate": `["Merged fact one", "Merged fact two"]`,
		},
	}
	mm := NewMemoryManager(dir, llm, DefaultMemoryConfig())
	tool := NewMemoryTool(mm)

	tool.Call(`{"action":"add","target":"env","content":"Project uses Go 1.22"}`)
	tool.Call(`{"action":"add","target":"env","content":"Uses chi router"}`)

	result, err := tool.Call(`{"action":"consolidate","target":"env"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "true") {
		t.Errorf("expected success, got %q", result)
	}
}

func TestMemoryToolReturnsJSON(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), nil, DefaultMemoryConfig())
	tool := NewMemoryTool(mm)

	result, err := tool.Call(`{"action":"read"}`)
	if err != nil {
		t.Fatal(err)
	}
	// Should be valid JSON
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Errorf("result must be valid JSON: %v", err)
	}

	_, hasSuccess := parsed["success"]
	_, hasMessage := parsed["message"]
	if !hasSuccess || !hasMessage {
		t.Errorf("result should have 'success' and 'message' fields, got keys: %v", parsed)
	}
}

func TestMemoryToolDescription(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), nil, DefaultMemoryConfig())
	tool := NewMemoryTool(mm)
	desc := tool.Description()
	if desc == "" {
		t.Error("expected non-empty description")
	}
	if !strings.Contains(desc, "memory") {
		t.Errorf("description should mention memory, got %q", desc)
	}
}

func TestMemoryToolViewEpisode(t *testing.T) {
	dir := t.TempDir()
	es := NewEpisodeStore(dir, nil)
	es.Write("sess-view", "full episode content here", 5)

	mm := NewMemoryManager(dir, nil, DefaultMemoryConfig())
	mm.episodes = es
	tool := NewMemoryTool(mm)

	// View existing episode
	result, err := tool.Call(`{"action":"view","target":"episodes","query":"sess-view"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "true") {
		t.Errorf("expected success, got %q", result)
	}
	if !strings.Contains(result, "full episode content here") {
		t.Errorf("expected episode content, got %q", result)
	}

	// View missing episode
	result, err = tool.Call(`{"action":"view","target":"episodes","query":"nonexistent"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "false") {
		t.Errorf("expected failure for missing episode, got %q", result)
	}

	// View with wrong target
	result, err = tool.Call(`{"action":"view","target":"user","query":"sess-view"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "false") {
		t.Errorf("expected failure for non-episodes target, got %q", result)
	}

	// View with empty query
	result, err = tool.Call(`{"action":"view","target":"episodes"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "false") {
		t.Errorf("expected failure for empty query, got %q", result)
	}
}

func TestMemoryToolAddReturnsEntries(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), nil, DefaultMemoryConfig())
	tool := NewMemoryTool(mm)

	result, err := tool.Call(`{"action":"add","target":"user","content":"fact one"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "true") {
		t.Errorf("expected success, got %q", result)
	}
	if !strings.Contains(result, `"entries"`) {
		t.Errorf("expected entries field in response, got %q", result)
	}
	if !strings.Contains(result, "fact one") {
		t.Errorf("expected fact in entries, got %q", result)
	}
}

func TestMergeEntriesWithLLM(t *testing.T) {
	// Test LLM path: mock returns a merged entry
	mock := &mockLLM{
		responses: map[string]string{
			"Merge these two": "The user prefers short responses",
		},
	}
	got := mergeEntries(mock, "User likes terse answers", "User prefers short replies")
	if got != "The user prefers short responses" {
		t.Errorf("mergeEntries with LLM = %q, want %q", got, "The user prefers short responses")
	}

	// Test fallback when LLM returns empty
	mock2 := &mockLLM{responses: map[string]string{}}
	got2 := mergeEntries(mock2, "User likes Go", "User likes Python")
	if got2 != "User likes Go. User likes Python" {
		t.Errorf("mergeEntries fallback = %q, want concatenation", got2)
	}

	// Test with nil LLM (should concatenate)
	got3 := mergeEntries(nil, "A", "B")
	if got3 != "A. B" {
		t.Errorf("mergeEntries nil LLM = %q, want 'A. B'", got3)
	}
}

func TestMemoryToolAddAtom(t *testing.T) {
	cfg := DefaultMemoryConfig()
	cfg.Extended = &extended.Config{Enabled: boolPtr(true)}
	mm := NewMemoryManager(t.TempDir(), nil, cfg)
	mm.InitExtended(nil, t.TempDir())
	tool := NewMemoryTool(mm)

	result, err := tool.Call(`{"action":"add_atom","content":"User prefers Go","atom_type":"preference","confidence":0.9}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "true") {
		t.Errorf("expected success, got %q", result)
	}
	atoms, _ := mm.extended.List()
	if len(atoms) != 1 {
		t.Fatalf("expected 1 atom, got %d", len(atoms))
	}
	if atoms[0].SourceClass != extended.SourceUserApproved {
		t.Errorf("source class = %q, want %q", atoms[0].SourceClass, extended.SourceUserApproved)
	}
}

func TestMemoryToolSearchAndForgetAtom(t *testing.T) {
	cfg := DefaultMemoryConfig()
	cfg.Extended = &extended.Config{
		Enabled:                boolPtr(true),
		SemanticSearchMinScore: 0.0,
	}
	mm := NewMemoryManager(t.TempDir(), nil, cfg)
	mm.InitExtended(nil, t.TempDir())
	mm.extended.SetEmbedderFactory(func() embedding.TextEmbedder { return newTestEmbedder(256) })
	mm.extended.SetEmbedder(newTestEmbedder(256))
	mm.extended.MarkDirty()
	tool := NewMemoryTool(mm)

	mm.extended.AddAtom(nil, extended.MemoryAtom{Text: "Project uses Postgres", SourceClass: extended.SourceUserApproved, Type: extended.TypeFact})

	result, err := tool.Call(`{"action":"search_atoms","query":"Postgres"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Postgres") {
		t.Errorf("expected Postgres in result, got %q", result)
	}

	atoms, _ := mm.extended.List()
	id := atoms[0].ID
	result, err = tool.Call(`{"action":"forget_atom","atom_id":"` + id + `"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "true") {
		t.Errorf("expected success, got %q", result)
	}
	atoms, _ = mm.extended.List()
	if len(atoms) != 0 {
		t.Errorf("expected 0 atoms after forget, got %d", len(atoms))
	}
}

// testEmbedder is a tiny deterministic embedding backend for tests.
type testEmbedder struct {
	dims int
}

func newTestEmbedder(dims int) *testEmbedder { return &testEmbedder{dims: dims} }
func (e *testEmbedder) Fit(corpus []string) error { return nil }
func (e *testEmbedder) Embed(text string) (vector.Vector, error) {
	vec := make(vector.Vector, e.dims)
	for _, c := range strings.ToLower(text) {
		idx := int(c) % e.dims
		vec[idx] += 1.0
	}
	return vec, nil
}
func (e *testEmbedder) EmbedAll(texts []string) ([]vector.Vector, error) {
	out := make([]vector.Vector, len(texts))
	for i, t := range texts {
		vec, err := e.Embed(t)
		if err != nil {
			return nil, err
		}
		out[i] = vec
	}
	return out, nil
}
func (e *testEmbedder) Fingerprint() string { return fmt.Sprintf("test/%d", e.dims) }
func (e *testEmbedder) SaveState(path string) {}
func (e *testEmbedder) LoadState(path string) bool { return false }
