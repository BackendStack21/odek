package main

// Regression tests for the 2026-08 audit resource-cap findings: vision had
// no input size cap, diff's LCS table could allocate ~800 MB at exactly
// 10K×10K lines, and math_eval recursion depth was unbounded (uncatchable
// stack-exhaustion on multi-MB inputs).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

// TestAudit_VisionRejectsHugeFile pins the input cap: transcribe enforces
// 10 MiB on audio, but vision handed any file size straight to
// llama-mtmd-cli/ffmpeg — a multi-gigabyte "image" meant host memory/disk
// DoS with no approval.
func TestAudit_VisionRejectsHugeFile(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "huge.png")
	f, err := os.OpenFile(big, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxFileReadBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	tool := newVisionTool(danger.DangerousConfig{}, config.VisionConfig{})
	out, _ := tool.Call(`{"path":` + fmt.Sprintf("%q", big) + `}`)
	var r struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}
	if !strings.Contains(r.Error, "too large") {
		t.Fatalf("vision accepted an oversized file, error = %q", r.Error)
	}
}

// TestAudit_DiffProductCap pins the LCS cell-count bound: the per-side cap
// (≤10000 lines each) still allowed 10000×10000 ≈ 800 MB of table. 2500×2500
// lines (≈50 MB of cells) must be rejected by the product cap.
func TestAudit_DiffProductCap(t *testing.T) {
	dir := t.TempDir()
	mkLines := func(name string, n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString("x\n")
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(b.String()), 0644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := mkLines("a.txt", 2500)
	b := mkLines("b.txt", 2500)

	tool := &diffTool{}
	out, _ := tool.Call(`{"path_a":` + fmt.Sprintf("%q", a) + `,"path_b":` + fmt.Sprintf("%q", b) + `}`)
	var r struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}
	if !strings.Contains(r.Error, "too large") {
		t.Fatalf("diff accepted a %d-cell comparison, error = %q", 2501*2501, r.Error)
	}
}

// TestAudit_MathEvalDepthCap pins the recursion bound: go/parser and
// evalNode recurse once per ParenExpr with no depth limit — a deep enough
// expression killed the whole agent process with an uncatchable fatal
// stack overflow. Depth 10000 must be rejected before parsing.
func TestAudit_MathEvalDepthCap(t *testing.T) {
	deep := strings.Repeat("(", 10000) + "1" + strings.Repeat(")", 10000)
	tool := &mathEvalTool{}
	out, _ := tool.Call(`{"expression":` + fmt.Sprintf("%q", deep) + `}`)
	var r struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}
	if !strings.Contains(r.Error, "too deeply nested") && !strings.Contains(r.Error, "too large") {
		t.Fatalf("math_eval accepted depth 10000, error = %q", r.Error)
	}
	// Normal nesting still evaluates.
	out, _ = tool.Call(`{"expression":"((1+2)*3)"}`)
	if !strings.Contains(out, "9") {
		t.Fatalf("math_eval broke normal expressions: %s", out)
	}
}

// TestAudit_ChildEnvWithoutSecrets pins the sub-agent env scrub: children
// used to inherit every KEY=VALUE from ~/.odek/secrets.env via
// os.Environ() — the FD handoff protected only the primary API key.
func TestAudit_ChildEnvWithoutSecrets(t *testing.T) {
	t.Setenv("AUDIT_SECRET_A", "x")
	t.Setenv("AUDIT_SECRET_B", "y")
	t.Setenv("AUDIT_KEEP_ME", "z")

	got := childEnvWithout([]string{"AUDIT_SECRET_A", "AUDIT_SECRET_B"})
	var sawSecretA, sawSecretB, sawKeep bool
	for _, kv := range got {
		switch {
		case strings.HasPrefix(kv, "AUDIT_SECRET_A="):
			sawSecretA = true
		case strings.HasPrefix(kv, "AUDIT_SECRET_B="):
			sawSecretB = true
		case strings.HasPrefix(kv, "AUDIT_KEEP_ME="):
			sawKeep = true
		}
	}
	if sawSecretA || sawSecretB {
		t.Error("child env still contains secrets.env-injected variables")
	}
	if !sawKeep {
		t.Error("child env dropped an unrelated variable")
	}
}

// TestAudit_DelegateContextNoRace pins the 2026-08 data-race fix: the loop
// runs parallel tool calls against the SAME tool instance and calls
// SetContext from each goroutine. delegateTasksTool used to store the
// context in a bare field (race on t.ctx); it now embeds the mutexed
// ctxTool like every other context-aware tool. Run with -race to verify.
func TestAudit_DelegateContextNoRace(t *testing.T) {
	tool := &delegateTasksTool{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); tool.SetContext(ctx) }()
		go func() { defer wg.Done(); _ = tool.toolCtx() }()
	}
	wg.Wait()
}
