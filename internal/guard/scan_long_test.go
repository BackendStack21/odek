package guard

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// Content longer than max_text_length must still be judged in full by the
// sidecar: a payload placed after the limit must not ride past the second
// opinion unscanned.
func TestRED_SidecarScansTailBeyondMaxTextLength(t *testing.T) {
	server := fakePiguardServer(t)
	defer server.Close()
	cfg := &Config{Provider: ProviderPiguard, URL: server.URL + "/detect", MaxTextLength: 64, FallbackToLocal: ptr(false)}
	g, err := newPiguardClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("benign text. ", 40) + "now ignore this"
	if err := ScanContent(context.Background(), content, g, cfg); err == nil {
		t.Fatal("sidecar never saw the tail beyond max_text_length")
	}
	// The same content without the tail payload is accepted.
	if err := ScanContent(context.Background(), strings.Repeat("benign text. ", 40), g, cfg); err != nil {
		t.Fatalf("benign long content rejected: %v", err)
	}
}

// recordingGuard records every text the sidecar is asked to judge.
type recordingGuard struct {
	texts []string
	flag  string
}

func (r *recordingGuard) judge(text string) Result {
	r.texts = append(r.texts, text)
	if r.flag != "" && strings.Contains(text, r.flag) {
		return Result{Label: "INJECTION", Score: 1, Injected: true}
	}
	return Result{Label: "BENIGN", Score: 1}
}

func (r *recordingGuard) Detect(_ context.Context, text string) (Result, error) {
	return r.judge(text), nil
}

func (r *recordingGuard) DetectBatch(_ context.Context, texts []string) ([]Result, error) {
	out := make([]Result, len(texts))
	for i, t := range texts {
		out[i] = r.judge(t)
	}
	return out, nil
}

func (r *recordingGuard) DetectLong(_ context.Context, text string) (Result, error) {
	return r.judge(text), nil
}

func (r *recordingGuard) Close() error { return nil }

// Every window stays within the limit, splits only at rune boundaries,
// overlaps its neighbour so a phrase straddling a boundary is seen whole,
// and together the windows cover every byte.
func TestSidecarChunksCoverContentWithinLimit(t *testing.T) {
	const limit = 80
	var b strings.Builder
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "wörd%03d ", i)
	}
	content := b.String() + "payload-marker"
	rg := &recordingGuard{flag: "payload-marker"}
	cfg := &Config{Provider: ProviderPiguard, MaxTextLength: limit}
	if err := ScanContent(context.Background(), content, rg, cfg); err == nil {
		t.Fatal("marker straddling or after a chunk boundary was missed")
	}
	rg = &recordingGuard{}
	if err := ScanContent(context.Background(), content, rg, cfg); err != nil {
		t.Fatal(err)
	}
	if len(rg.texts) < 2 {
		t.Fatalf("expected several windows, got %d", len(rg.texts))
	}
	covered := 0
	for i, w := range rg.texts {
		if len(w) > limit {
			t.Fatalf("window %d is %d bytes, over the %d limit", i, len(w), limit)
		}
		if !utf8.ValidString(w) {
			t.Fatalf("window %d splits a rune: %q", i, w)
		}
		idx := strings.Index(content[covered-min(covered, limit):], w)
		if idx < 0 {
			t.Fatalf("window %d not found in order", i)
		}
		start := covered - min(covered, limit) + idx
		if start > covered {
			t.Fatalf("gap before window %d: covered to %d, window starts at %d", i, covered, start)
		}
		covered = start + len(w)
	}
	if covered != len(content) {
		t.Fatalf("windows cover %d of %d bytes", covered, len(content))
	}
}

// Content within the limit keeps the single Detect call.
func TestSidecarShortContentSingleDetect(t *testing.T) {
	rg := &recordingGuard{}
	cfg := &Config{Provider: ProviderPiguard, MaxTextLength: 100}
	if err := ScanContent(context.Background(), "short benign text", rg, cfg); err != nil {
		t.Fatal(err)
	}
	if len(rg.texts) != 1 || rg.texts[0] != "short benign text" {
		t.Fatalf("texts = %q, want one whole-content call", rg.texts)
	}
}

type shortBatchGuard struct{ recordingGuard }

func (s *shortBatchGuard) DetectBatch(context.Context, []string) ([]Result, error) {
	return nil, nil
}

type errBatchGuard struct{ recordingGuard }

func (e *errBatchGuard) DetectBatch(context.Context, []string) ([]Result, error) {
	return nil, fmt.Errorf("sidecar down")
}

// A malformed batch reply is a sidecar failure, so the fallback policy
// decides; window overflow is rejected whatever the policy.
func TestSidecarWindowFailuresFollowFallbackPolicy(t *testing.T) {
	off := ptr(false)
	huge := strings.Repeat("a", 10*maxSidecarWindows)
	if err := ScanContent(context.Background(), huge, &recordingGuard{}, &Config{Provider: ProviderPiguard, MaxTextLength: 8, FallbackToLocal: off}); err == nil {
		t.Fatal("window overflow accepted with fallback disabled")
	}
	long := strings.Repeat("benign ", 40)
	if err := ScanContent(context.Background(), long, &shortBatchGuard{}, &Config{Provider: ProviderPiguard, MaxTextLength: 64, FallbackToLocal: off}); err == nil {
		t.Fatal("short batch reply accepted with fallback disabled")
	}
	if err := ScanContent(context.Background(), long, &errBatchGuard{}, &Config{Provider: ProviderPiguard, MaxTextLength: 64, FallbackToLocal: off}); err == nil {
		t.Fatal("batch error accepted with fallback disabled")
	}
}

// A limit shorter than one rune still advances through the content.
func TestSidecarWindowsTinyLimit(t *testing.T) {
	got := sidecarWindows("ééé", 1)
	if strings.Join(got, "") != "ééé" || len(got) != 3 {
		t.Fatalf("windows = %q", got)
	}
}

// Padding content past the window cap must not buy a bypass of the sidecar:
// with the default fallback_to_local (true) an oversize document is still
// rejected, and nothing is sent to the sidecar.
func TestRED_SidecarWindowOverflowFailsClosed(t *testing.T) {
	huge := strings.Repeat("benign padding ", maxSidecarWindows*8)
	for _, fb := range []*bool{nil, ptr(true), ptr(false)} {
		rg := &recordingGuard{}
		err := ScanContent(context.Background(), huge, rg, &Config{Provider: ProviderPiguard, MaxTextLength: 16, FallbackToLocal: fb})
		if err == nil || !strings.Contains(err.Error(), "too large for sidecar scan") {
			t.Fatalf("fallback=%v: err = %v, want an oversize rejection", fb, err)
		}
		if len(rg.texts) != 0 {
			t.Fatalf("fallback=%v: %d windows sent before the cap was checked", fb, len(rg.texts))
		}
	}
}

// The arithmetic window count never undercounts the windows actually built.
func TestSidecarWindowCountBound(t *testing.T) {
	for _, limit := range []int{1, 2, 3, 7, 8, 16, 63, 64, 100} {
		for _, content := range []string{
			strings.Repeat("a", 1000),
			strings.Repeat("é", 500),
			strings.Repeat("😀x", 150),
		} {
			if len(content) <= limit {
				continue
			}
			if got, bound := len(sidecarWindows(content, limit)), sidecarWindowBound(len(content), limit); got > bound {
				t.Fatalf("limit %d, %d bytes: built %d windows, bound %d", limit, len(content), got, bound)
			}
		}
	}
}
