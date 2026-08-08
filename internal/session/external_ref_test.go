package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llm"
)

func validRef() ExternalRef {
	return ExternalRef{
		Kind:      "ci_run",
		URI:       "https://ci.example.test/runs/4821",
		CreatedBy: "ci-orchestrator",
		ReadOnly:  true,
	}
}

func TestExternalRefValidate(t *testing.T) {
	longKind := strings.Repeat("k", 65)
	longURI := strings.Repeat("u", 2049)
	longCreatedBy := strings.Repeat("c", 129)

	cases := []struct {
		name    string
		mutate  func(*ExternalRef)
		wantErr string // empty = valid
	}{
		{"valid", func(*ExternalRef) {}, ""},
		{"valid kind charset", func(r *ExternalRef) { r.Kind = "a0_-z9" }, ""},
		{"kind empty", func(r *ExternalRef) { r.Kind = "" }, "kind must be 1-64"},
		{"kind too long", func(r *ExternalRef) { r.Kind = longKind }, "kind must be 1-64"},
		{"kind uppercase", func(r *ExternalRef) { r.Kind = "CI_Run" }, "only lowercase ASCII"},
		{"kind space", func(r *ExternalRef) { r.Kind = "ci run" }, "only lowercase ASCII"},
		{"kind slash", func(r *ExternalRef) { r.Kind = "ci/run" }, "only lowercase ASCII"},
		{"kind unicode", func(r *ExternalRef) { r.Kind = "cí" }, "only lowercase ASCII"},
		{"uri empty", func(r *ExternalRef) { r.URI = "" }, "uri must be 1-2048"},
		{"uri too long", func(r *ExternalRef) { r.URI = longURI }, "uri must be 1-2048"},
		{"uri newline", func(r *ExternalRef) { r.URI = "app://x\ny" }, "control character"},
		{"uri null byte", func(r *ExternalRef) { r.URI = "app://x\x00y" }, "control character"},
		{"uri tab", func(r *ExternalRef) { r.URI = "app://x\ty" }, "control character"},
		{"uri del", func(r *ExternalRef) { r.URI = "app://x\x7fy" }, "control character"},
		{"created_by empty", func(r *ExternalRef) { r.CreatedBy = "" }, "created_by must be 1-128"},
		{"created_by too long", func(r *ExternalRef) { r.CreatedBy = longCreatedBy }, "created_by must be 1-128"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validRef()
			tc.mutate(&r)
			err := r.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestExternalRefDedupe(t *testing.T) {
	sess := &Session{}
	r := validRef()
	r2 := validRef()
	r2.URI = "https://ci.example.test/runs/9999"

	added, err := sess.AddExternalRefs(r, r, r2, r)
	if err != nil {
		t.Fatalf("AddExternalRefs: %v", err)
	}
	if added != 2 {
		t.Fatalf("expected 2 added, got %d", added)
	}
	if len(sess.ExternalRefs) != 2 {
		t.Fatalf("expected 2 stored refs, got %d", len(sess.ExternalRefs))
	}
	// Same (kind, uri) but a different created_by is NOT a duplicate.
	r3 := validRef()
	r3.CreatedBy = "other-app"
	added, err = sess.AddExternalRefs(r3)
	if err != nil {
		t.Fatalf("AddExternalRefs: %v", err)
	}
	if added != 1 || len(sess.ExternalRefs) != 3 {
		t.Fatalf("expected created_by variant to be added (added=%d total=%d)", added, len(sess.ExternalRefs))
	}
}

func TestExternalRefCreatedAtStamped(t *testing.T) {
	sess := &Session{}
	if _, err := sess.AddExternalRefs(validRef()); err != nil {
		t.Fatalf("AddExternalRefs: %v", err)
	}
	if sess.ExternalRefs[0].CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be stamped on add")
	}
	// An explicit CreatedAt is preserved.
	explicit := validRef()
	explicit.URI = "app://workflow/123"
	explicit.CreatedAt = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	if _, err := sess.AddExternalRefs(explicit); err != nil {
		t.Fatalf("AddExternalRefs: %v", err)
	}
	if !sess.ExternalRefs[1].CreatedAt.Equal(explicit.CreatedAt) {
		t.Fatalf("explicit CreatedAt not preserved: %v", sess.ExternalRefs[1].CreatedAt)
	}
}

func TestExternalRefRoundTrip(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewStoreWithDir: %v", err)
	}
	sess, err := store.Create([]llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "do the thing"},
	}, "test-model", "do the thing")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ref := validRef()
	if _, err := sess.AddExternalRefs(ref); err != nil {
		t.Fatalf("AddExternalRefs: %v", err)
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.ExternalRefs) != 1 {
		t.Fatalf("expected 1 ref after round-trip, got %d", len(loaded.ExternalRefs))
	}
	got := loaded.ExternalRefs[0]
	if got.Kind != ref.Kind || got.URI != ref.URI || got.CreatedBy != ref.CreatedBy || got.ReadOnly != ref.ReadOnly {
		t.Fatalf("ref mismatch after round-trip: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("CreatedAt lost in round-trip")
	}
}

// TestExternalRefsSurviveAppendAndSaveNoIndex mirrors the `odek continue`
// persistence paths: Append (final save) and SaveNoIndex (per-turn persist).
func TestExternalRefsSurviveAppendAndSaveNoIndex(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewStoreWithDir: %v", err)
	}
	sess, err := store.Create([]llm.Message{{Role: "user", Content: "task"}}, "m", "task")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := sess.AddExternalRefs(validRef()); err != nil {
		t.Fatalf("AddExternalRefs: %v", err)
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Append path (final save of a turn).
	if err := store.Append(sess.ID, []llm.Message{{Role: "assistant", Content: "done"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.ExternalRefs) != 1 || loaded.ExternalRefs[0].URI != validRef().URI {
		t.Fatalf("refs lost across Append: %+v", loaded.ExternalRefs)
	}

	// SaveNoIndex path (per-turn persist callback), adding a second ref the
	// way `odek continue --external-ref` does.
	newRef := ExternalRef{Kind: "dashboard", URI: "https://dash.example.test/board/7", CreatedBy: "ops"}
	added, err := loaded.AddExternalRefs(newRef)
	if err != nil {
		t.Fatalf("AddExternalRefs: %v", err)
	}
	if added != 1 {
		t.Fatalf("expected 1 added, got %d", added)
	}
	if err := store.SaveNoIndex(loaded); err != nil {
		t.Fatalf("SaveNoIndex: %v", err)
	}
	reloaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reloaded.ExternalRefs) != 2 {
		t.Fatalf("expected 2 refs after continue-style add, got %d", len(reloaded.ExternalRefs))
	}
}

// TestExternalRefsSurviveTrim forces the write-path size-cap trim and checks
// the refs survive the session-file rewrite.
func TestExternalRefsSurviveTrim(t *testing.T) {
	old := MaxSessionFileBytes
	MaxSessionFileBytes = 4096
	t.Cleanup(func() { MaxSessionFileBytes = old })

	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewStoreWithDir: %v", err)
	}
	msgs := []llm.Message{{Role: "system", Content: "sys"}}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, llm.Message{Role: "user", Content: fmt.Sprintf("turn %d: %s", i, strings.Repeat("x", 800))})
	}
	sess, err := store.Create(msgs, "m", "big")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := sess.AddExternalRefs(validRef()); err != nil {
		t.Fatalf("AddExternalRefs: %v", err)
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Messages) >= len(msgs) {
		t.Fatalf("expected trim to drop messages, still have %d of %d", len(loaded.Messages), len(msgs))
	}
	if len(loaded.ExternalRefs) != 1 || loaded.ExternalRefs[0].URI != validRef().URI {
		t.Fatalf("refs lost across trim: %+v", loaded.ExternalRefs)
	}
}

// TestOldFormatSessionLoads verifies backward compatibility: a session file
// written before external_refs existed must load unchanged.
func TestOldFormatSessionLoads(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatalf("NewStoreWithDir: %v", err)
	}
	id := "20260101-0123456789abcdef0123456789abcdef"
	raw := `{"id":"` + id + `","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z",` +
		`"model":"old-model","turns":1,"task":"old task","sandbox":false,` +
		`"messages":[{"role":"user","content":"hello"}]}`
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(raw), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	sess, err := store.Load(id)
	if err != nil {
		t.Fatalf("Load old-format session: %v", err)
	}
	if sess.ExternalRefs != nil {
		t.Fatalf("expected nil ExternalRefs, got %+v", sess.ExternalRefs)
	}
	if sess.Task != "old task" || len(sess.Messages) != 1 {
		t.Fatalf("old-format session fields mangled: %+v", sess)
	}
	// Re-saving an old session must not invent refs either.
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if strings.Contains(string(data), "external_refs") {
		t.Fatal("external_refs should stay omitted (omitempty) when empty")
	}
}

func TestAddExternalRefsRejectsInvalid(t *testing.T) {
	sess := &Session{}
	bad := validRef()
	bad.Kind = "UPPER"
	if _, err := sess.AddExternalRefs(bad); err == nil {
		t.Fatal("expected validation error for uppercase kind")
	}
	if len(sess.ExternalRefs) != 0 {
		t.Fatal("invalid ref must not be stored")
	}
}
