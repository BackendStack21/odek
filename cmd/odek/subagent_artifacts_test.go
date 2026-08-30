package main

// TDD RED phase — sub-agent result artifacts M1: the core protocol.
// (SUBAGENT_RESULT_ARTIFACTS_PLAN.md)

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/artifact"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/session"
)

func writeArtifactFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func expectedSHA(t *testing.T, content string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func TestScanArtifacts_HappyPath(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "report.md", "# Report\nfindings here")
	writeArtifactFile(t, dir, "data.json", `{"k":1}`)

	refs, flags := scanArtifacts(dir, 1<<20)
	if len(refs) != 2 {
		t.Fatalf("want 2 refs, got %d (flags: %v)", len(refs), flags)
	}
	if len(flags) != 0 {
		t.Errorf("happy path must not flag: %v", flags)
	}
	byID := map[string]artifact.Ref{}
	for _, r := range refs {
		byID[r.ID] = r
	}
	rep := byID["report"]
	if rep.Schema != artifact.SchemaArtifactRef || rep.MediaType != "text/markdown" {
		t.Errorf("report ref wrong: %+v", rep)
	}
	if rep.SHA256 != expectedSHA(t, "# Report\nfindings here") {
		t.Errorf("runner must compute sha256 itself: %s", rep.SHA256)
	}
	if rep.SizeBytes == nil || *rep.SizeBytes != int64(len("# Report\nfindings here")) {
		t.Errorf("size mismatch: %+v", rep.SizeBytes)
	}
	if !strings.HasPrefix(rep.Summary, "# Report") {
		t.Errorf("summary should carry the first line: %q", rep.Summary)
	}
	if byID["data"].MediaType != "application/json" {
		t.Errorf("json media type: %+v", byID["data"])
	}
}

func TestScanArtifacts_SkipsNonRegular(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "real.txt", "hi")
	if err := os.Symlink(filepath.Join(dir, "real.txt"), filepath.Join(dir, "escape.link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0700); err != nil {
		t.Fatal(err)
	}
	refs, flags := scanArtifacts(dir, 1<<20)
	if len(refs) != 1 || refs[0].ID != "real" {
		t.Fatalf("only the regular file must be collected: %+v (%v)", refs, flags)
	}
	if len(flags) == 0 {
		t.Error("skipped entries must be flagged")
	}
}

func TestScanArtifacts_BudgetLargestFirst(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "small.txt", strings.Repeat("s", 100))
	writeArtifactFile(t, dir, "mid.txt", strings.Repeat("m", 200))
	writeArtifactFile(t, dir, "big.txt", strings.Repeat("b", 300))

	refs, flags := scanArtifacts(dir, 500) // big+mid fit, small drops
	ids := map[string]bool{}
	for _, r := range refs {
		ids[r.ID] = true
	}
	if !ids["big"] || !ids["mid"] || ids["small"] {
		t.Errorf("largest-first budget enforcement broken: %v", ids)
	}
	if len(flags) == 0 {
		t.Error("dropped artifact must be flagged")
	}
}

func TestScanArtifacts_CapRefs(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 70; i++ {
		writeArtifactFile(t, dir, strings.Repeat("f", 10)+string(rune('a'+i/26))+string(rune('a'+i%26))+".txt", "x")
	}
	refs, flags := scanArtifacts(dir, 1<<20)
	if len(refs) != 64 {
		t.Errorf("refs must cap at 64, got %d", len(refs))
	}
	if len(flags) == 0 {
		t.Error("cap overflow must be flagged")
	}
}

func TestTaskFileSpec_ArtifactRoot(t *testing.T) {
	spec, err := decodeTaskFileSpec([]byte(`{"goal":"g","artifact_root":"/tmp/x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if spec.ArtifactRoot != "/tmp/x" {
		t.Errorf("artifact_root not decoded: %+v", spec)
	}
}

func childResultJSON(artifacts string) string {
	s := `{"status":"success","summary":"done","tokens_used":5,"iterations":1,"duration_seconds":1`
	if artifacts != "" {
		s += `,"artifacts":` + artifacts
	}
	return s + `}`
}

func TestFormatTaskResult_ArtifactMetadataAndInline(t *testing.T) {
	dir := t.TempDir()
	content := "artifact body line\nsecond line"
	writeArtifactFile(t, dir, "report.md", content)

	ref := artifact.Ref{
		Schema: artifact.SchemaArtifactRef, ID: "report", MediaType: "text/markdown",
		URI: "file://" + filepath.Join(dir, "report.md"),
		SHA256: expectedSHA(t, content), SizeBytes: ptrInt64(int64(len(content))),
		Summary: "artifact body line",
	}
	b, _ := json.Marshal([]artifact.Ref{ref})

	got := formatTaskResult(childResultJSON(string(b)), dir)
	for _, want := range []string{
		"artifacts:",
		"report (text/markdown",
		"summary: done",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q\n---\n%s", want, got)
		}
	}
	if !strings.Contains(got, content) {
		t.Errorf("small text artifact must be inlined:\n%s", got)
	}
}

func TestFormatTaskResult_InvalidRefDropped(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "report.md", "real content")
	ref := artifact.Ref{
		Schema: artifact.SchemaArtifactRef, ID: "report", MediaType: "text/markdown",
		URI: "file://" + filepath.Join(dir, "report.md"),
		SHA256: strings.Repeat("0", 64), // wrong hash — tampered
	}
	b, _ := json.Marshal([]artifact.Ref{ref})

	got := formatTaskResult(childResultJSON(string(b)), dir)
	if strings.Contains(got, "artifacts:\n") && !strings.Contains(got, "dropped") {
		t.Errorf("tampered ref must be dropped with a flag:\n%s", got)
	}
	if strings.Contains(got, "real content") {
		t.Error("tampered artifact content must never be inlined")
	}
}

func TestFormatTaskResult_NoRootRejectsAll(t *testing.T) {
	ref := artifact.Ref{
		Schema: artifact.SchemaArtifactRef, ID: "report", MediaType: "text/markdown",
		URI: "file:///etc/passwd", SHA256: expectedSHA(t, "x"),
	}
	b, _ := json.Marshal([]artifact.Ref{ref})
	got := formatTaskResult(childResultJSON(string(b)), "")
	if !strings.Contains(got, "dropped") {
		t.Errorf("no configured root ⇒ fail-closed rejection:\n%s", got)
	}
	if strings.Contains(got, "file:///etc/passwd") {
		t.Error("raw paths must never render")
	}
}

func ptrInt64(v int64) *int64 { return &v }

// ── Session-cleanup cascade ──────────────────────────────────────────

func TestStore_Delete_CascadesArtifacts(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var cascaded []string
	store.OnDelete = func(id string) { cascaded = append(cascaded, id) }

	if err := store.Save(&session.Session{ID: "s1", Messages: []llm.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("s1"); err != nil {
		t.Fatal(err)
	}
	if len(cascaded) != 1 || cascaded[0] != "s1" {
		t.Errorf("Delete must fire the cascade hook: %v", cascaded)
	}
	if err := store.Delete("../escape"); err == nil {
		t.Error("invalid id must be rejected")
	}
	if len(cascaded) != 1 {
		t.Errorf("rejected ids must not cascade: %v", cascaded)
	}
}

func TestStore_Cleanup_CascadesArtifacts(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var cascaded []string
	store.OnDelete = func(id string) { cascaded = append(cascaded, id) }

	old := time.Now().Add(-48 * time.Hour)
	for _, id := range []string{"old1", "old2", "fresh"} {
		s := &session.Session{ID: id, Messages: []llm.Message{{Role: "user", Content: "x"}}, UpdatedAt: time.Now()}
		if id != "fresh" {
			s.UpdatedAt = old
		}
		if err := store.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	n, err := store.Cleanup(time.Now().Add(-24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 removed, got %d", n)
	}
	if len(cascaded) != 2 {
		t.Errorf("Cleanup (indexed path) must cascade per removed session: %v", cascaded)
	}
}
