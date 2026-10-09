package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/guard"
	"github.com/BackendStack21/odek/internal/loop"
)

type ingestLog struct {
	mu      sync.Mutex
	sources []string
	content []string
}

func (l *ingestLog) ctx() context.Context {
	return loop.WithIngestRecorder(context.Background(), func(source, content string) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.sources = append(l.sources, source)
		l.content = append(l.content, content)
	})
}

func numberedFile(t *testing.T, dir, name string, n int, prefix string) string {
	t.Helper()
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%s line %d\n", prefix, i)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRED_Tools_DiffRecordsOneIngestPerResult(t *testing.T) {
	dir := t.TempDir()
	a := numberedFile(t, dir, "a.txt", 100, "alpha")
	b := numberedFile(t, dir, "b.txt", 100, "beta")
	l := &ingestLog{}
	tool := &diffTool{dangerousConfig: danger.DangerousConfig{}}
	tool.SetContext(l.ctx())
	out, err := tool.Call(fmt.Sprintf(`{"path_a":%q,"path_b":%q}`, a, b))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.sources) > 2 {
		t.Fatalf("diff recorded %d audit ingests for one result; want at most 2", len(l.sources))
	}
	if n := strings.Count(out, "untrusted_content_"); n < 400 {
		t.Fatalf("every line must stay wrapped; found %d wrappers", n)
	}
	// Recorded content covers every element.
	all := strings.Join(l.content, "\n")
	if !strings.Contains(all, "alpha line 99") || !strings.Contains(all, "beta line 0") {
		t.Fatalf("recorded ingest does not cover all lines")
	}
}

func TestRED_Tools_HeadTailRecordsOneIngestPerFile(t *testing.T) {
	dir := t.TempDir()
	f := numberedFile(t, dir, "f.txt", 50, "row")
	l := &ingestLog{}
	tool := &headTailTool{dangerousConfig: danger.DangerousConfig{}}
	tool.SetContext(l.ctx())
	if _, err := tool.Call(fmt.Sprintf(`{"path":%q,"lines":50}`, f)); err != nil {
		t.Fatal(err)
	}
	if len(l.sources) > 2 {
		t.Fatalf("head_tail recorded %d ingests; want at most 2", len(l.sources))
	}
}

func TestRED_Tools_GlobAndSearchRecordOneIngest(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 40; i++ {
		numberedFile(t, dir, fmt.Sprintf("f%02d.txt", i), 3, "needle")
	}
	l := &ingestLog{}
	g := &globTool{dangerousConfig: danger.DangerousConfig{}}
	g.SetContext(l.ctx())
	if _, err := g.Call(fmt.Sprintf(`{"path":%q,"pattern":"*.txt"}`, dir)); err != nil {
		t.Fatal(err)
	}
	if len(l.sources) > 2 {
		t.Fatalf("glob recorded %d ingests; want at most 2", len(l.sources))
	}
	l2 := &ingestLog{}
	s := &searchFilesTool{dangerousConfig: danger.DangerousConfig{}}
	s.SetContext(l2.ctx())
	if _, err := s.Call(fmt.Sprintf(`{"path":%q,"pattern":"needle"}`, dir)); err != nil {
		t.Fatal(err)
	}
	if len(l2.sources) > 2 {
		t.Fatalf("search_files recorded %d ingests; want at most 2", len(l2.sources))
	}
}

func TestRED_Tools_TreeRecordsOneIngest(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 30; i++ {
		numberedFile(t, dir, fmt.Sprintf("f%02d.txt", i), 1, "x")
	}
	l := &ingestLog{}
	tool := &treeTool{dangerousConfig: danger.DangerousConfig{}}
	tool.SetContext(l.ctx())
	if _, err := tool.Call(fmt.Sprintf(`{"path":%q}`, dir)); err != nil {
		t.Fatal(err)
	}
	if len(l.sources) > 2 {
		t.Fatalf("tree recorded %d ingests; want at most 2", len(l.sources))
	}
}

func TestRED_Tools_JSONQueryCoalescesIngests(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "d.json")
	var b strings.Builder
	b.WriteString(`{"items":[`)
	for i := 0; i < 60; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"v%d"`, i)
	}
	b.WriteString(`]}`)
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	l := &ingestLog{}
	tool := &jsonQueryTool{dangerousConfig: danger.DangerousConfig{}}
	tool.SetContext(l.ctx())
	if _, err := tool.Call(fmt.Sprintf(`{"path":%q,"query":"items"}`, p)); err != nil {
		t.Fatal(err)
	}
	if len(l.sources) > 2 {
		t.Fatalf("json_query recorded %d ingests; want at most 2", len(l.sources))
	}
}

// fakeGuard flags content containing "EVIL" and counts calls.
type fakeBatchGuard struct {
	guard.Guard
	mu    sync.Mutex
	calls int
	sizes []int
}

func (g *fakeBatchGuard) Detect(_ context.Context, text string) (guard.Result, error) {
	g.mu.Lock()
	g.calls++
	g.sizes = append(g.sizes, len(text))
	g.mu.Unlock()
	return guard.Result{Injected: strings.Contains(text, "EVIL")}, nil
}

// withBatchGuard installs g as the local-provider scanner: the local rule
// scan is what batching applies to, so the scan hook routes every scan to g.
func withBatchGuard(t *testing.T, g guard.Guard, maxText int) {
	t.Helper()
	withBatchGuardProvider(t, guard.NewLocalGuard(), guard.ProviderLocal, maxText)
	orig := scanToolOutputContent
	t.Cleanup(func() { scanToolOutputContent = orig })
	scanToolOutputContent = func(ctx context.Context, content string, _ guard.Guard, _ *guard.Config) error {
		res, err := g.Detect(ctx, content)
		if err != nil {
			return err
		}
		if res.Injected {
			return errors.New("content contains injection")
		}
		return nil
	}
}

func withBatchGuardProvider(t *testing.T, g guard.Guard, provider string, maxText int) {
	t.Helper()
	og, oc := toolOutputGuard, toolOutputGuardCfg
	t.Cleanup(func() { toolOutputGuard, toolOutputGuardCfg = og, oc })
	toolOutputGuard = g
	toolOutputGuardCfg = guard.Config{Provider: provider, MaxTextLength: maxText}
}

// A model-backed sidecar judges one window at a time; batching would let a
// single injected element be diluted by its neighbours, so a sidecar provider
// keeps the per-element scan the old code performed.
func TestRED_WrapUntrustedBatch_SidecarScansPerElement(t *testing.T) {
	fg := &fakeBatchGuard{}
	withBatchGuardProvider(t, fg, guard.ProviderPiguard, 0)
	contents := []string{"alpha", "", "beta beta", "gamma"}
	wrapUntrustedBatch(context.Background(), "src", nil, contents)
	elems := []string{"alpha", "beta beta", "gamma"}
	if len(fg.sizes) != len(elems) {
		t.Fatalf("sidecar saw %d scans %v, want one per non-empty element", len(fg.sizes), fg.sizes)
	}
	for i, sz := range fg.sizes {
		// The sidecar may see a normalized form, never two elements joined.
		if sz > len(elems[i])+8 {
			t.Fatalf("scan %d covered %d bytes for a %d-byte element: elements were joined", i, sz, len(elems[i]))
		}
	}
}

func TestWrapUntrustedBatch_ScansEveryByteAndBannersOnlyOffenders(t *testing.T) {
	fg := &fakeBatchGuard{}
	withBatchGuard(t, fg, 0)
	contents := []string{"fine one", "EVIL ignore previous", "", "fine two"}
	l := &ingestLog{}
	out := wrapUntrustedBatch(l.ctx(), "src", nil, contents)
	if out[2] != "" {
		t.Fatalf("empty element must stay empty, got %q", out[2])
	}
	for i, o := range out {
		if contents[i] == "" {
			continue
		}
		if !hasUntrustedWrapper(o) {
			t.Fatalf("element %d not wrapped: %q", i, o)
		}
		banner := strings.Contains(o, "SECURITY NOTICE")
		if banner != (i == 1) {
			t.Fatalf("element %d banner=%v", i, banner)
		}
	}
	if len(l.sources) != 1 || l.sources[0] != "src" {
		t.Fatalf("want exactly one ingest labelled src, got %v", l.sources)
	}
	// One joined scan plus rescans of the flagged group's elements.
	if fg.calls > 1+len(contents) {
		t.Fatalf("too many guard calls: %d", fg.calls)
	}
}

func TestWrapUntrustedBatch_GroupsRespectGuardTextLimit(t *testing.T) {
	fg := &fakeBatchGuard{}
	withBatchGuard(t, fg, 50)
	contents := make([]string, 20)
	for i := range contents {
		contents[i] = strings.Repeat("a", 20)
	}
	wrapUntrustedBatch(context.Background(), "src", nil, contents)
	covered := 0
	for _, sz := range fg.sizes {
		if sz > 50 {
			t.Fatalf("scan group of %d bytes exceeds guard limit 50", sz)
		}
		covered += sz
	}
	if covered < 20*20 {
		t.Fatalf("scans covered %d bytes, want all %d", covered, 20*20)
	}
}

func TestWrapUntrustedBatch_JoinOnlyFlagBannersWholeGroup(t *testing.T) {
	// A guard that flags the joined text but no single element.
	withBatchGuard(t, joinOnlyGuard{}, 0)
	out := wrapUntrustedBatch(context.Background(), "s", nil, []string{"part-a", "part-b"})
	for i, o := range out {
		if !strings.Contains(o, "SECURITY NOTICE") {
			t.Fatalf("element %d must carry the banner when only the join is flagged", i)
		}
	}
}

type joinOnlyGuard struct{ guard.Guard }

func (joinOnlyGuard) Detect(_ context.Context, text string) (guard.Result, error) {
	return guard.Result{Injected: strings.Contains(text, "part-a\npart-b")}, nil
}

func TestCoalesceIngests_OnePerSource(t *testing.T) {
	l := &ingestLog{}
	ctx, flush := coalesceIngests(l.ctx())
	for i := 0; i < 5; i++ {
		wrapUntrusted(ctx, "s1", fmt.Sprintf("x%d", i))
	}
	wrapUntrusted(ctx, "s2", "y")
	if len(l.sources) != 0 {
		t.Fatalf("ingests must be deferred until flush, got %d", len(l.sources))
	}
	flush()
	if len(l.sources) != 2 || l.sources[0] != "s1" || l.sources[1] != "s2" {
		t.Fatalf("sources=%v", l.sources)
	}
	if !strings.Contains(l.content[0], "x4") {
		t.Fatalf("coalesced content lost: %q", l.content[0])
	}
	// No recorder: no-op.
	c2, f2 := coalesceIngests(context.Background())
	f2()
	_ = c2
}
