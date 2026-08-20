package mcpclient

// Tests for the odek-extension/v1 per-server timeout and limit fields
// (WP2 — see docs/EXTENSIONS.md and odek-ext.md). Behavioral tests run
// against the mock extension server in testdata/artifact_server.go via the
// same fakeServerPath build idiom as the other client tests.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// artifactClientWithLimits starts the mock extension server with the given
// limit fields set on its ServerConfig.
func artifactClientWithLimits(t *testing.T, cfg ServerConfig, extraEnv map[string]string) *Client {
	t.Helper()
	env := map[string]string{"FAKE_ARTIFACT_MODE": "1"}
	for k, v := range extraEnv {
		env[k] = v
	}
	cfg.Command = fakeServerPath(t)
	cfg.Env = env
	client, err := New("limited", cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestLimits_DefaultsUnchanged(t *testing.T) {
	timeout, maxResp, maxChars, warnings, err := normalizeLimits("srv", ServerConfig{})
	if err != nil {
		t.Fatalf("normalizeLimits: %v", err)
	}
	// Zero is the "no per-server override" sentinel: call() falls back to
	// DefaultTimeout (30s) at call time.
	if timeout != 0 {
		t.Errorf("timeout for zero config = %v, want 0 (DefaultTimeout fallback)", timeout)
	}
	if DefaultTimeout != 30*time.Second {
		t.Errorf("DefaultTimeout = %v, want 30s", DefaultTimeout)
	}
	if maxResp != 10<<20 {
		t.Errorf("default max response bytes = %d, want %d (10 MiB)", maxResp, 10<<20)
	}
	if maxChars != DefaultMaxResultChars {
		t.Errorf("default max result chars = %d, want %d", maxChars, DefaultMaxResultChars)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings for zero config: %v", warnings)
	}
}

func TestLimits_CustomValuesHonored(t *testing.T) {
	timeout, maxResp, maxChars, warnings, err := normalizeLimits("srv", ServerConfig{
		TimeoutSeconds:   120,
		MaxResponseBytes: 2 << 20,
		MaxResultChars:   100000,
	})
	if err != nil {
		t.Fatalf("normalizeLimits: %v", err)
	}
	if timeout != 120*time.Second {
		t.Errorf("timeout = %v, want 120s", timeout)
	}
	if maxResp != 2<<20 {
		t.Errorf("max response bytes = %d, want %d", maxResp, 2<<20)
	}
	if maxChars != 100000 {
		t.Errorf("max result chars = %d, want 100000", maxChars)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
}

func TestLimits_TimeoutAboveCapClampedWithWarning(t *testing.T) {
	timeout, _, _, warnings, err := normalizeLimits("slow-srv", ServerConfig{TimeoutSeconds: 7200})
	if err != nil {
		t.Fatalf("normalizeLimits: %v", err)
	}
	if timeout != time.Duration(MaxTimeoutSeconds)*time.Second {
		t.Errorf("timeout = %v, want clamp to %ds", timeout, MaxTimeoutSeconds)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly 1 clamp warning", warnings)
	}
	if !strings.Contains(warnings[0], "slow-srv") || !strings.Contains(warnings[0], "clamped") {
		t.Errorf("warning %q must name the server and the clamp", warnings[0])
	}
}

func TestLimits_ResultCharsAboveCapClampedWithWarning(t *testing.T) {
	_, _, maxChars, warnings, err := normalizeLimits("chatty-srv", ServerConfig{MaxResultChars: 5_000_000})
	if err != nil {
		t.Fatalf("normalizeLimits: %v", err)
	}
	if maxChars != MaxResultCharsCap {
		t.Errorf("max result chars = %d, want clamp to %d", maxChars, MaxResultCharsCap)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "chatty-srv") {
		t.Errorf("warnings = %v, want 1 warning naming the server", warnings)
	}
}

func TestLimits_ResponseBytesAboveCeilingRejected(t *testing.T) {
	_, _, _, _, err := normalizeLimits("greedy-srv", ServerConfig{MaxResponseBytes: MaxResponseBytesCeiling + 1})
	if err == nil {
		t.Fatal("expected error for max_response_bytes above the 64 MiB ceiling")
	}
	for _, want := range []string{"greedy-srv", "ceiling"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err.Error(), want)
		}
	}

	// Exactly at the ceiling is allowed.
	if _, _, _, _, err := normalizeLimits("srv", ServerConfig{MaxResponseBytes: MaxResponseBytesCeiling}); err != nil {
		t.Errorf("max_response_bytes at the ceiling should be accepted, got: %v", err)
	}
}

func TestLimits_NegativeValuesRejected(t *testing.T) {
	for name, cfg := range map[string]ServerConfig{
		"timeout":        {TimeoutSeconds: -1},
		"response_bytes": {MaxResponseBytes: -1},
		"result_chars":   {MaxResultChars: -1},
	} {
		if _, _, _, _, err := normalizeLimits("srv", cfg); err == nil {
			t.Errorf("normalizeLimits with negative %s: expected error", name)
		}
	}
}

func TestLimits_DefaultTimeoutGlobalNotMutated(t *testing.T) {
	before := DefaultTimeout
	client := artifactClientWithLimits(t, ServerConfig{TimeoutSeconds: 5}, nil)
	_ = client
	if DefaultTimeout != before {
		t.Errorf("DefaultTimeout mutated by per-server config: %v -> %v", before, DefaultTimeout)
	}
}

func TestLimits_CustomTimeoutHonored(t *testing.T) {
	client := artifactClientWithLimits(t, ServerConfig{TimeoutSeconds: 1}, nil)

	// No caller deadline: the per-server timeout must kick in.
	start := time.Now()
	_, err := client.CallTool(context.Background(), "slow", `{"seconds":10}`)
	if err == nil {
		t.Fatal("expected timeout error from slow tool")
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Errorf("slow call took %v; per-server 1s timeout was not enforced", elapsed)
	}
}

func TestLimits_CustomResponseCapHonored(t *testing.T) {
	const cap = 64 * 1024
	client := artifactClientWithLimits(t, ServerConfig{MaxResponseBytes: cap}, nil)

	_, err := client.CallTool(context.Background(), "large_result", `{"size":262144}`)
	if err == nil {
		t.Fatal("expected error for response exceeding the custom cap")
	}
	for _, want := range []string{"limited", "exceeded", "65536"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q (server, limit breach, configured limit)", err.Error(), want)
		}
	}
}

func TestLimits_ResultCharsTruncationMarker(t *testing.T) {
	const limit = 1000
	client := artifactClientWithLimits(t, ServerConfig{MaxResultChars: limit}, nil)

	out, err := client.CallTool(context.Background(), "large_result", `{"size":5000}`)
	if err != nil {
		t.Fatalf("valid oversized result must not error: %v", err)
	}
	if !strings.Contains(out, "result truncated") {
		t.Errorf("truncation marker missing from result: %.200q...", out)
	}
	for _, want := range []string{`"limited"`, `"large_result"`, "max_result_chars=1000", "5000 chars"} {
		if !strings.Contains(out, want) {
			t.Errorf("truncation notice missing %q: %.300q...", want, out)
		}
	}
	if n := utf8.RuneCountInString(out); n > limit {
		t.Errorf("truncated result = %d chars, want <= %d", n, limit)
	}
}

func TestLimits_ErrorChannelResultCap(t *testing.T) {
	// A tool-level error must not bypass max_result_chars: the isError text
	// is capped exactly like a successful result, with the same structured
	// truncation notice (previously only the success path was capped).
	const limit = 1000
	client := artifactClientWithLimits(t, ServerConfig{MaxResultChars: limit},
		map[string]string{"FAKE_ERROR_SIZE": "5000"})

	_, err := client.CallTool(context.Background(), "error_result", `{}`)
	if err == nil {
		t.Fatal("expected error from error_result tool")
	}
	for _, want := range []string{"result truncated", `"error_result"`, "max_result_chars=1000", "5000 chars"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("capped error missing %q: %s", want, err.Error())
		}
	}
	// The "mcpclient …: tool … returned error:" envelope prefix is the only
	// text allowed outside the cap.
	if n := utf8.RuneCountInString(err.Error()); n > limit+64 {
		t.Errorf("error string = %d chars, want <= %d (cap + envelope prefix)", n, limit+64)
	}
}

func TestLimits_EnvelopeTruncationRetainsArtifacts(t *testing.T) {
	path := writeTempArtifact(t, "ok pkg/a 0.3s\n")
	client := artifactClientWithLimits(t,
		ServerConfig{MaxResultChars: 300, ArtifactRoots: []string{filepath.Dir(path)}},
		map[string]string{
			"FAKE_ARTIFACT_PATH": path,
			// Pad the envelope text past the 300-char cap so the text is
			// truncated; the artifact metadata line must survive.
			"FAKE_ARTIFACT_TEXT": strings.Repeat("analysis chunk\n", 40),
		},
	)

	out, err := client.CallTool(context.Background(), "artifact_result", `{}`)
	if err != nil {
		t.Fatalf("CallTool artifact_result: %v", err)
	}

	// WP3: the model-facing form of an envelope is the rendered compact text
	// plus metadata lines — not the envelope JSON. An oversized envelope text
	// gets the structured truncation notice while the artifact metadata (the
	// resolvable reference) is retained.
	if !strings.Contains(out, "result truncated") {
		t.Errorf("truncation marker missing from rendered result: %.200q...", out)
	}
	if !strings.Contains(out, `"limited"`) || !strings.Contains(out, `"artifact_result"`) {
		t.Errorf("truncation notice must name server and tool: %.200q...", out)
	}
	if !strings.Contains(out, `artifact "report-1"`) {
		t.Errorf("artifact metadata line missing after truncation: %.200q...", out)
	}
	if strings.Contains(out, path) {
		t.Errorf("rendered result leaks the absolute artifact path: %.200q...", out)
	}
}

func TestLimits_ResultWithinCapUnchanged(t *testing.T) {
	client := artifactClientWithLimits(t, ServerConfig{MaxResultChars: 100000}, nil)

	out, err := client.CallTool(context.Background(), "echo", `{"text":"small"}`)
	if err != nil {
		t.Fatalf("CallTool echo: %v", err)
	}
	if out != "echo: small" {
		t.Errorf("result = %q, want %q (no marker, no alteration)", out, "echo: small")
	}
}
