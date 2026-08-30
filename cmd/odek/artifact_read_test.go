package main

// TDD RED phase — M2 artifact_read (SUBAGENT_RESULT_ARTIFACTS_PLAN.md):
// validated artifact refs registered at collation become readable content
// via a parent-only built-in tool. The model supplies ONLY the id — path
// resolution is internal to the registry, so no model input ever reaches
// the filesystem as a path.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/artifact"
)

func regRef(t *testing.T, id, content string) (artifact.Ref, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, id+".md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	size := int64(len(content))
	ref := artifact.Ref{
		Schema: artifact.SchemaArtifactRef, ID: id, MediaType: "text/markdown",
		URI: "file://" + path, SHA256: expectedSHA(t, content), SizeBytes: &size,
	}
	return ref, path
}

func TestArtifactRegistry_RegisterLookupEvict(t *testing.T) {
	resetArtifactRegistryForTest()
	ref, path := regRef(t, "alpha", "alpha content")
	registerSubagentArtifact(artifactEntry{Ref: ref, Path: path, TaskIdx: 0})

	got, ok := lookupSubagentArtifact("alpha")
	if !ok || got.Path != path {
		t.Fatalf("lookup failed: %+v ok=%v", got, ok)
	}

	// Eviction: cap + 10 more pushes alpha out (oldest first).
	for i := 0; i < artifactRegistryCap+10; i++ {
		r, p := regRef(t, fmt.Sprintf("fill-%03d", i), "x")
		registerSubagentArtifact(artifactEntry{Ref: r, Path: p, TaskIdx: i})
	}
	if _, ok := lookupSubagentArtifact("alpha"); ok {
		t.Error("oldest entry must be evicted at cap")
	}
	if _, ok := lookupSubagentArtifact("fill-000"); ok {
		t.Error("second-oldest must be evicted too")
	}
}

func TestArtifactRegistry_DuplicateIDLastWins(t *testing.T) {
	resetArtifactRegistryForTest()
	r1, p1 := regRef(t, "dup", "first")
	registerSubagentArtifact(artifactEntry{Ref: r1, Path: p1, TaskIdx: 0})
	r2, p2 := regRef(t, "dup", "second")
	dup := registerSubagentArtifact(artifactEntry{Ref: r2, Path: p2, TaskIdx: 1})

	if !dup {
		t.Error("duplicate registration must be reported")
	}
	got, _ := lookupSubagentArtifact("dup")
	if got.Path != p2 || got.TaskIdx != 1 {
		t.Errorf("last-wins broken: %+v", got)
	}
}

func TestArtifactReadTool_HappyPath(t *testing.T) {
	resetArtifactRegistryForTest()
	content := "# Report\n" + strings.Repeat("detail ", 200)
	ref, path := regRef(t, "report", content)
	registerSubagentArtifact(artifactEntry{Ref: ref, Path: path, TaskIdx: 0})

	tool := &artifactReadTool{}
	tool.SetContext(t.Context())
	got, err := tool.Call(`{"id":"report"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "detail") {
		t.Errorf("content missing:\n%s", got)
	}
	if !strings.Contains(got, "report") || !strings.Contains(got, "text/markdown") {
		t.Errorf("metadata header missing:\n%s", got)
	}
	if strings.Contains(got, path) {
		t.Errorf("raw path must never render:\n%s", got)
	}
	if !strings.Contains(got, "untrusted") {
		t.Errorf("artifact content must be untrusted-wrapped:\n%s", got)
	}
}

func TestArtifactReadTool_OffsetLimit(t *testing.T) {
	resetArtifactRegistryForTest()
	content := strings.Repeat("A", 1000)
	ref, path := regRef(t, "blob", content)
	registerSubagentArtifact(artifactEntry{Ref: ref, Path: path})

	tool := &artifactReadTool{}
	tool.SetContext(t.Context())

	got, err := tool.Call(`{"id":"blob","offset":900,"limit":50}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, strings.Repeat("A", 50)) {
		t.Errorf("offset slice missing:\n%s", got)
	}
	if !strings.Contains(got, "TRUNCATED") {
		t.Errorf("must flag truncation when more remains:\n%s", got)
	}

	// Hard cap: limit above the max is clamped, not honored.
	if _, err := tool.Call(`{"id":"blob","limit":99999999}`); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactReadTool_UnknownIDListsAvailable(t *testing.T) {
	resetArtifactRegistryForTest()
	for _, id := range []string{"one", "two"} {
		ref, path := regRef(t, id, "x")
		registerSubagentArtifact(artifactEntry{Ref: ref, Path: path})
	}

	tool := &artifactReadTool{}
	tool.SetContext(t.Context())
	got, err := tool.Call(`{"id":"nope"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "one") || !strings.Contains(got, "two") {
		t.Errorf("unknown id must list available:\n%s", got)
	}
	// Traversal-shaped ids are just unknown — never paths.
	if _, err := tool.Call(`{"id":"../../etc/passwd"}`); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactReadTool_VanishedFile(t *testing.T) {
	resetArtifactRegistryForTest()
	ref, path := regRef(t, "ghost", "data")
	registerSubagentArtifact(artifactEntry{Ref: ref, Path: path})
	os.Remove(path)

	tool := &artifactReadTool{}
	tool.SetContext(t.Context())
	got, _ := tool.Call(`{"id":"ghost"}`)
	if !strings.Contains(got, "no longer available") {
		t.Errorf("vanished artifact must fail friendly:\n%s", got)
	}
}

func TestArtifactReadEnabled_Gate(t *testing.T) {
	if !artifactReadEnabled(toolConfig{}) {
		t.Error("top-level operator run must get artifact_read")
	}
	if artifactReadEnabled(toolConfig{SelfTrust: "trusted"}) {
		t.Error("sub-agents must NOT get artifact_read (parent-only)")
	}
	if artifactReadEnabled(toolConfig{SelfTrust: "untrusted"}) {
		t.Error("untrusted sub-agents must NOT get artifact_read")
	}
}

func TestRegisterTaskArtifacts_DuplicateNote(t *testing.T) {
	resetArtifactRegistryForTest()
	dir := t.TempDir()
	c1, c2 := "first body", "second body"
	p1 := filepath.Join(dir, "dup.md")
	p2 := filepath.Join(dir, "dup2.md")
	os.WriteFile(p1, []byte(c1), 0o600)
	os.WriteFile(p2, []byte(c2), 0o600)
	size1, size2 := int64(len(c1)), int64(len(c2))
	raw1 := fmt.Sprintf(`{"status":"success","summary":"ok","artifacts":[{"schema":%q,"id":"dup","uri":"file://%s","media_type":"text/markdown","sha256":%q,"size_bytes":%d}]}`,
		artifact.SchemaArtifactRef, p1, expectedSHA(t, c1), size1)
	raw2 := fmt.Sprintf(`{"status":"success","summary":"ok","artifacts":[{"schema":%q,"id":"dup","uri":"file://%s","media_type":"text/markdown","sha256":%q,"size_bytes":%d}]}`,
		artifact.SchemaArtifactRef, p2, expectedSHA(t, c2), size2)

	if notes := registerTaskArtifacts(raw1, dir, 0); len(notes) != 0 {
		t.Errorf("first registration must not note: %v", notes)
	}
	notes := registerTaskArtifacts(raw2, dir, 1)
	if len(notes) != 1 || !strings.Contains(notes[0], "duplicate") {
		t.Errorf("duplicate must produce a note: %v", notes)
	}
}
