package main

// RED-first tests for SUBAGENT_ARTIFACT_DELIVERY_PLAN.md v2 (proposals
// A+A2, B+B2, C, D1, F). Contract pins for the two-channel result-delivery
// model: headline cap + artifact channel, visible truncation marker,
// probe-increment artifact-id aliasing with provenance, inline byte budget,
// and the per-run registry floor.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/artifact"
	"github.com/BackendStack21/odek/internal/llm"
)

// ── A: parent tool description carries the two-channel contract ──────

func TestDelegateTasksDescription_ArtifactChannel(t *testing.T) {
	desc := (&delegateTasksTool{}).Description()
	for _, want := range []string{
		"Result delivery",
		"Headline:",
		"~2000 characters",
		"Artifacts:",
		"32 KB",
		"artifact_read(id)",
		"flat file in your artifact dir",
		"trailing … means it was cut",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("Description() missing %q — the parent LLM cannot learn the artifact protocol", want)
		}
	}
}

// ── A2: guidance schema nudge ─────────────────────────────────────────

func TestDelegateTasksSchema_GuidanceNudge(t *testing.T) {
	schema, ok := (&delegateTasksTool{}).Schema().(map[string]any)
	if !ok {
		t.Fatal("Schema() must be a map")
	}
	tasks, ok := schema["properties"].(map[string]any)["tasks"].(map[string]any)
	if !ok {
		t.Fatal("schema missing tasks object")
	}
	items := tasks["items"].(map[string]any)["properties"].(map[string]any)
	guidance, ok := items["guidance"].(map[string]any)
	if !ok {
		t.Fatal("schema missing tasks[].guidance")
	}
	desc, _ := guidance["description"].(string)
	if !strings.Contains(desc, "flat file in your artifact dir") {
		t.Errorf("guidance schema description missing artifact-delivery nudge: %q", desc)
	}
}

// ── B: child-side note rewrite ────────────────────────────────────────

func TestChildArtifactNote_V2(t *testing.T) {
	note := childArtifactNote(".odek-artifacts/task-abc")
	for _, want := range []string{
		"Result delivery:",
		"~2000 characters",
		"content beyond the cap is lost",
		"If your result fits in a short paragraph, just answer.",
		"FLAT file",
		"no subdirectories",
		"nested files are discarded",
		"short headline: status, artifact file names, key decisions",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("childArtifactNote missing %q", want)
		}
	}
	// Trust invariants from the v1 pin: relative staging path only.
	if !strings.Contains(note, ".odek-artifacts/task-abc") {
		t.Errorf("note missing relative staging path: %s", note)
	}
	if strings.Contains(note, "/.odek/") {
		t.Errorf("note must not leak the canonical host path: %s", note)
	}
}

// ── B2: identity closing line aligned with the headline shape ────────

func TestSubagentIdentity_HeadlineShape(t *testing.T) {
	if !strings.Contains(subagentIdentity, "End with a short headline: status, artifact file names, key decisions.") {
		t.Error("subagentIdentity must end with the short-headline contract")
	}
	if !strings.Contains(subagentIdentity, "The files carry the detail.") {
		t.Error("subagentIdentity must tell the child the files carry the detail")
	}
	if strings.Contains(subagentIdentity, "Report what you built, what files changed") {
		t.Error("subagentIdentity still asks for a fat multi-part report — contradicts the headline shape")
	}
}

// ── C: truncateWithLen + truncation marker ────────────────────────────

func TestTruncateWithLen(t *testing.T) {
	s, total := truncateWithLen("short", 2048)
	if s != "short" || total != 5 {
		t.Errorf("under cap: got (%q, %d)", s, total)
	}
	long := strings.Repeat("a", 3000)
	s, total = truncateWithLen(long, 2048)
	if len([]rune(s)) != subagentHeadlineMaxRunes+1 { // cap + ellipsis
		t.Errorf("cut length = %d runes, want %d+ellipsis", len([]rune(s)), subagentHeadlineMaxRunes)
	}
	if total != 3000 {
		t.Errorf("original rune count = %d, want 3000", total)
	}
	if !strings.HasSuffix(s, "…") {
		t.Error("cut string must end with the ellipsis")
	}
	// Multibyte safety: rune-indexed cut must not split runes.
	multi := strings.Repeat("🚀", 1000) // 4000 bytes, 1000 runes
	s, total = truncateWithLen(multi, 10)
	if total != 1000 {
		t.Errorf("multibyte total = %d, want 1000", total)
	}
	if got := len([]rune(s)); got != 11 {
		t.Errorf("multibyte cut = %d runes, want 10+ellipsis", got)
	}
}

func TestFormatTaskResult_TruncationMarker(t *testing.T) {
	summary := strings.Repeat("a", 2100)
	raw := fmt.Sprintf(`{"status":"success","summary":%q,"summary_truncated":true,"summary_runes":3000}`, summary)

	got := formatTaskResultDetailed(raw, 0, true)
	if !strings.Contains(got, "headline truncated (2048 of 3000 runes shown)") {
		t.Errorf("marker missing:\n%s", got)
	}
	if !strings.Contains(got, "fetch artifacts via artifact_read or re-run with a narrower goal") {
		t.Errorf("next-action hint missing:\n%s", got)
	}

	got = formatTaskResultDetailed(raw, 0, false)
	if strings.Contains(got, "artifact_read") {
		t.Error("hint must be conditional: mid-tree parents have no artifact_read")
	}
	if !strings.Contains(got, "headline truncated (2048 of 3000 runes shown)") {
		t.Errorf("marker must render even without the tool:\n%s", got)
	}
	if !strings.Contains(got, "re-run with a narrower goal") {
		t.Errorf("fallback next-action missing:\n%s", got)
	}

	// Untruncated results carry no marker.
	plain := `{"status":"success","summary":"tiny","summary_runes":4}`
	if got = formatTaskResultDetailed(plain, 0, true); strings.Contains(got, "headline truncated") {
		t.Errorf("marker must not appear for untruncated summaries:\n%s", got)
	}
}

func TestSubagentResult_OmitEmptyTruncationFields(t *testing.T) {
	plain, err := json.Marshal(subagentResult{Status: "success", Summary: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "summary_truncated") || strings.Contains(string(plain), "summary_runes") {
		t.Errorf("omitempty violated — old clients must see identical JSON:\n%s", plain)
	}
	marked, err := json.Marshal(subagentResult{Status: "success", Summary: "x", SummaryTruncated: true, SummaryRunes: 3000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(marked), `"summary_truncated":true`) || !strings.Contains(string(marked), `"summary_runes":3000`) {
		t.Errorf("truncation fields missing from envelope:\n%s", marked)
	}
}

func TestExtractSummaryInfo(t *testing.T) {
	long := strings.Repeat("b", 3000)
	msgs := []llm.Message{{Role: "assistant", Content: long}}
	s, total, truncated := extractSummaryInfo(msgs)
	if !truncated || total != 3000 || len([]rune(s)) != subagentHeadlineMaxRunes+1 {
		t.Errorf("extractSummaryInfo = (runes %d, total %d, truncated %v)", len([]rune(s)), total, truncated)
	}
	s, total, truncated = extractSummaryInfo([]llm.Message{{Role: "assistant", Content: "hi"}})
	if truncated || total != 2 || s != "hi" {
		t.Errorf("short: = (%q, %d, %v)", s, total, truncated)
	}
}

// ── D1: probe-increment aliasing + provenance ─────────────────────────

func refForFile(t *testing.T, dir, name, content string) artifact.Ref {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	size := int64(len(content))
	return artifact.Ref{Schema: artifact.SchemaArtifactRef, ID: strings.TrimSuffix(name, filepath.Ext(name)), URI: "file://" + p, MediaType: "text/markdown", SHA256: hex.EncodeToString(sum[:]), SizeBytes: &size}
}

func TestRegisterArtifactAlias_FirstWinsAndAlias(t *testing.T) {
	resetArtifactRegistryForTest()
	dir := t.TempDir()
	r0 := refForFile(t, dir, "report.md", "first")
	r1 := refForFile(t, dir, "report.md", "second")
	r1.ID = "report"

	id, dup := registerSubagentArtifact(artifactEntry{Ref: r0, Path: strings.TrimPrefix(r0.URI, "file://"), TaskIdx: 0})
	if dup || id != "report" {
		t.Errorf("first registration: got (%q, %v), want (report, false)", id, dup)
	}
	id, dup = registerSubagentArtifact(artifactEntry{Ref: r1, Path: strings.TrimPrefix(r1.URI, "file://"), TaskIdx: 1})
	if !dup {
		t.Error("duplicate must be flagged")
	}
	if id != "report.t2" {
		t.Errorf("alias = %q, want report.t2 (task 1 → .t<taskIdx+1>)", id)
	}
	if e, ok := lookupSubagentArtifact("report"); !ok || e.TaskIdx != 0 {
		t.Errorf("first-wins violated: lookup(report) = (%+v, %v)", e, ok)
	}
	if e, ok := lookupSubagentArtifact("report.t2"); !ok || e.TaskIdx != 1 {
		t.Errorf("alias lookup: (%+v, %v)", e, ok)
	}
}

func TestRegisterArtifactAlias_NoEvictionOfLiveEntries(t *testing.T) {
	resetArtifactRegistryForTest()
	dir := t.TempDir()
	// Task 0 claims the plain id first (first-wins owner).
	first := refForFile(t, dir, "report.md", "first")
	first.ID = "report"
	if id, dup := registerSubagentArtifact(artifactEntry{Ref: first, Path: strings.TrimPrefix(first.URI, "file://"), TaskIdx: 0}); dup || id != "report" {
		t.Fatalf("first registration: got (%q, %v)", id, dup)
	}
	// A real file legitimately named report.t2 (artifactIDRe allows dots).
	real := refForFile(t, dir, "report.t2.md", "real stem")
	real.ID = "report.t2"
	if id, dup := registerSubagentArtifact(artifactEntry{Ref: real, Path: strings.TrimPrefix(real.URI, "file://"), TaskIdx: 2}); dup || id != "report.t2" {
		t.Fatalf("real stem registration: got (%q, %v)", id, dup)
	}
	dup1 := refForFile(t, dir, "report.md", "dup")
	dup1.ID = "report"
	alias, dup := registerSubagentArtifact(artifactEntry{Ref: dup1, Path: strings.TrimPrefix(dup1.URI, "file://"), TaskIdx: 1})
	if !dup {
		t.Error("duplicate must be flagged")
	}
	if alias != "report.t3" {
		t.Errorf("alias must probe past the taken report.t2, got %q", alias)
	}
	// The plain id still belongs to task 0; the real stem was never evicted.
	if e, ok := lookupSubagentArtifact("report"); !ok || e.TaskIdx != 0 {
		t.Errorf("first-wins owner displaced: (%+v, %v)", e, ok)
	}
	if e, ok := lookupSubagentArtifact("report.t2"); !ok || e.TaskIdx != 2 {
		t.Errorf("live entry evicted by aliasing: (%+v, %v)", e, ok)
	}
}

func TestLookupEffectiveArtifactID(t *testing.T) {
	resetArtifactRegistryForTest()
	dir := t.TempDir()
	r := refForFile(t, dir, "dup.md", "x")
	r.ID = "dup"
	registerSubagentArtifact(artifactEntry{Ref: r, Path: strings.TrimPrefix(r.URI, "file://"), TaskIdx: 0})
	r2 := refForFile(t, dir, "dup2.md", "y")
	r2.ID = "dup"
	alias, _ := registerSubagentArtifact(artifactEntry{Ref: r2, Path: strings.TrimPrefix(r2.URI, "file://"), TaskIdx: 1})
	if eff, ok := lookupEffectiveArtifactID("dup", 0); !ok || eff != "dup" {
		t.Errorf("task 0 effective id: (%q, %v)", eff, ok)
	}
	if eff, ok := lookupEffectiveArtifactID("dup", 1); !ok || eff != alias {
		t.Errorf("task 1 effective id: (%q, %v), want alias %q", eff, ok, alias)
	}
	if _, ok := lookupEffectiveArtifactID("missing", 3); ok {
		t.Error("unknown orig id must miss")
	}
}

func TestArtifactRead_Provenance(t *testing.T) {
	resetArtifactRegistryForTest()
	dir := t.TempDir()
	r := refForFile(t, dir, "prov.md", "provenance body")
	registerSubagentArtifact(artifactEntry{Ref: r, Path: strings.TrimPrefix(r.URI, "file://"), TaskIdx: 1})
	tool := &artifactReadTool{}
	tool.SetContext(t.Context())
	got, _ := tool.Call(fmt.Sprintf(`{"id":%q}`, r.ID))
	if !strings.Contains(got, "task 2") {
		t.Errorf("artifact_read output missing owning-task provenance:\n%s", got)
	}
}

func TestFormatTaskResult_ArtifactProvenanceLine(t *testing.T) {
	resetArtifactRegistryForTest()
	dir := t.TempDir()
	c1, c2 := "first body", "second body"
	p1 := filepath.Join(dir, "dup.md")
	p2 := filepath.Join(dir, "dup2.md")
	os.WriteFile(p1, []byte(c1), 0o600)
	os.WriteFile(p2, []byte(c2), 0o600)
	refJSON := func(p, content string) string {
		sum := sha256.Sum256([]byte(content))
		return fmt.Sprintf(`{"schema":%q,"id":"dup","uri":"file://%s","media_type":"text/markdown","sha256":%q,"size_bytes":%d}`,
			artifact.SchemaArtifactRef, p, hex.EncodeToString(sum[:]), len(content))
	}
	registerTaskArtifacts(fmt.Sprintf(`{"status":"success","artifacts":[%s]}`, refJSON(p1, c1)), dir, 0)
	raw2 := fmt.Sprintf(`{"status":"success","artifacts":[%s]}`, refJSON(p2, c2))
	registerTaskArtifacts(raw2, dir, 1)

	got := formatTaskResultDetailed(raw2, 1, true, dir)
	if !strings.Contains(got, "report") && !strings.Contains(got, "dup.t2") {
		t.Errorf("render must show the effective (aliased) id:\n%s", got)
	}
	if !strings.Contains(got, "task 2") {
		t.Errorf("artifact line missing owning-task provenance:\n%s", got)
	}
}

// ── F: inline byte budget (largest-first) ─────────────────────────────

func TestRenderArtifacts_InlineBudget(t *testing.T) {
	resetArtifactRegistryForTest()
	dir := t.TempDir()
	var refs []string
	sizes := []int{30 << 10, 29 << 10, 28 << 10, 27 << 10, 26 << 10, 25 << 10, 24 << 10, 23 << 10}
	for i, size := range sizes {
		content := strings.Repeat(string(rune('a'+i)), size)
		p := filepath.Join(dir, fmt.Sprintf("f%d.md", i))
		os.WriteFile(p, []byte(content), 0o600)
		sum := sha256.Sum256([]byte(content))
		refs = append(refs, fmt.Sprintf(`{"schema":%q,"id":"f%d","uri":"file://%s","media_type":"text/markdown","sha256":%q,"size_bytes":%d}`,
			artifact.SchemaArtifactRef, i, p, hex.EncodeToString(sum[:]), size))
	}
	raw := fmt.Sprintf(`{"status":"success","artifacts":[%s]}`, strings.Join(refs, ","))

	got := formatTaskResultDetailed(raw, 0, true, dir)
	blocks := strings.Count(got, "--- artifact:")
	// Budget 128 KiB, largest-first: 30+29+28+27 = 114 KiB inlined; the
	// 26 KiB artifact would push past the budget → metadata line only.
	if blocks != 4 {
		t.Errorf("inlined %d artifacts, want 4 under the 128 KiB per-call budget:\n%s", blocks, got)
	}
	for i := range sizes {
		if !strings.Contains(got, fmt.Sprintf("- f%d (", i)) {
			t.Errorf("artifact f%d lost its metadata line (budget must degrade, never drop)", i)
		}
	}
}

// ── F: per-run registry floor ─────────────────────────────────────────

func TestArtifactRegistry_Floor(t *testing.T) {
	resetArtifactRegistryForTest()
	// Baseline: without a run mark, cap is enforced.
	fill := func(prefix string, n, taskIdx int) {
		for i := 0; i < n; i++ {
			e := artifactEntry{TaskIdx: taskIdx}
			e.Ref = artifact.Ref{ID: fmt.Sprintf("%s%d", prefix, i)}
			e.Path = "/tmp/nonexistent"
			registerSubagentArtifact(e)
		}
	}
	fill("a", 600, 0)
	if _, total := listSubagentArtifactIDs(); total != artifactRegistryCap {
		t.Fatalf("baseline cap: live = %d, want %d", total, artifactRegistryCap)
	}

	// With an active run mark, the current run's entries survive intact.
	resetArtifactRegistryForTest()
	mark := beginArtifactRegistryRun()
	fill("b", 600, 0)
	_, total := listSubagentArtifactIDs()
	if total != 600 {
		t.Errorf("floor violated: live = %d, want 600 (current-run entries must not evict)", total)
	}
	endArtifactRegistryRun(mark)
	fill("c", 1, 0)
	if _, total = listSubagentArtifactIDs(); total != artifactRegistryCap {
		t.Errorf("after run end: live = %d, want %d (eviction must resume)", total, artifactRegistryCap)
	}
}
