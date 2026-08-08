package mcpclient

// Contract tests for the odek-extension/v1 surface (docs/EXTENSIONS.md).
// They run against the mock extension server in testdata/artifact_server.go,
// built from source at test time via fakeServerPath (same idiom as the base
// fakeserver tests in client_test.go).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// artifactClient starts the mock extension server (FAKE_ARTIFACT_MODE=1) and
// returns a connected client with no artifact roots configured.
func artifactClient(t *testing.T, extraEnv map[string]string) *Client {
	t.Helper()
	return artifactClientWithRoots(t, nil, extraEnv)
}

// artifactClientWithRoots is artifactClient with the given artifact roots
// configured on the server config.
func artifactClientWithRoots(t *testing.T, roots []string, extraEnv map[string]string) *Client {
	t.Helper()
	env := map[string]string{"FAKE_ARTIFACT_MODE": "1"}
	for k, v := range extraEnv {
		env[k] = v
	}
	client, err := New("contract", ServerConfig{
		Command:       fakeServerPath(t),
		Env:           env,
		ArtifactRoots: roots,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestContract_VersionConstants(t *testing.T) {
	if ExtensionContractVersion != "odek-extension/v1" {
		t.Errorf("ExtensionContractVersion = %q, want %q", ExtensionContractVersion, "odek-extension/v1")
	}
	for name, got := range map[string]string{
		"SchemaToolResult":  SchemaToolResult,
		"SchemaArtifactRef": SchemaArtifactRef,
		"SchemaEvent":       SchemaEvent,
	} {
		if !strings.HasPrefix(got, "odek.") || !strings.HasSuffix(got, "/v1") {
			t.Errorf("%s = %q, want an odek.*/v1 schema name", name, got)
		}
	}
}

func TestContract_InitializeListCall(t *testing.T) {
	client := artifactClient(t, nil)

	tools, err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	want := []string{"echo", "large_result", "artifact_result", "bad_artifact", "slow", "error_result"}
	if len(tools) != len(want) {
		t.Fatalf("got %d tools, want %d (%v)", len(tools), len(want), want)
	}
	for i, name := range want {
		if tools[i].Name != name {
			t.Errorf("tools[%d].Name = %q, want %q", i, tools[i].Name, name)
		}
	}

	out, err := client.CallTool(context.Background(), "echo", `{"text":"hello extension"}`)
	if err != nil {
		t.Fatalf("CallTool echo: %v", err)
	}
	if out != "echo: hello extension" {
		t.Errorf("echo result = %q, want %q", out, "echo: hello extension")
	}
}

func TestContract_UnknownFieldsIgnored(t *testing.T) {
	// Server config: unknown fields must not break decoding (additive schema).
	var cfg ServerConfig
	raw := `{"command":"x","args":[],"env":{},"timeout_seconds":45,"max_response_bytes":1024,"future_field":{"nested":true}}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("ServerConfig with unknown fields: %v", err)
	}
	if cfg.Command != "x" {
		t.Errorf("cfg.Command = %q, want %q", cfg.Command, "x")
	}

	// Tool result envelope: the mock includes x_future_field (envelope level)
	// and the ref parsing must tolerate it — the call succeeds and renders
	// the compact model-facing form instead of failing on the unknown field.
	path := writeTempArtifact(t, "line1 ok\n")
	client := artifactClientWithRoots(t, []string{filepath.Dir(path)}, map[string]string{"FAKE_ARTIFACT_PATH": path})
	out, err := client.CallTool(context.Background(), "artifact_result", `{}`)
	if err != nil {
		t.Fatalf("CallTool artifact_result with unknown envelope fields: %v", err)
	}
	if !strings.Contains(out, `artifact "report-1"`) {
		t.Errorf("rendered result missing artifact metadata line: %q", out)
	}
}

func TestContract_BadToolNamesRejected(t *testing.T) {
	bad := []string{
		"",
		strings.Repeat("a", 65),
		"read file",  // space
		"a__b",       // double underscore (odek qualifier separator)
		"tool;rm",    // shell metachar
		"ünïcode",    // non-ASCII
		"tools/call", // path separator
	}
	for _, name := range bad {
		if err := validateToolName(name); err == nil {
			t.Errorf("validateToolName(%q) = nil, want error", name)
		}
	}
	good := []string{"echo", "large_result", "ci-test-results", "A1_-z"}
	for _, name := range good {
		if err := validateToolName(name); err != nil {
			t.Errorf("validateToolName(%q) = %v, want nil", name, err)
		}
	}
}

func TestContract_IsErrorPropagates(t *testing.T) {
	client := artifactClient(t, nil)
	_, err := client.CallTool(context.Background(), "error_result", `{}`)
	if err == nil {
		t.Fatal("expected error from error_result tool")
	}
	if !strings.Contains(err.Error(), "simulated tool failure") {
		t.Errorf("error = %q, want it to carry the server's isError message", err.Error())
	}
}

func TestContract_SlowCallTimesOut(t *testing.T) {
	client := artifactClient(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := client.CallTool(ctx, "slow", `{"seconds":10}`)
	if err == nil {
		t.Fatal("expected timeout error from slow tool")
	}
	if ctx.Err() == nil {
		t.Errorf("expected context deadline, got err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("slow call took %v; timeout was not enforced", elapsed)
	}
}

func TestContract_OversizedResponseFailsClosed(t *testing.T) {
	client := artifactClient(t, nil)
	// 11 MiB result exceeds the 10 MiB maxMCPResponseLine cap.
	_, err := client.CallTool(context.Background(), "large_result", `{"size":11534336}`)
	if err == nil {
		t.Fatal("expected error for oversized response")
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("error = %q, want it to mention the exceeded size limit", err.Error())
	}
}

func TestContract_ArtifactResultEnvelope(t *testing.T) {
	content := "FAIL pkg/a TestX\nok pkg/b 1.2s\n"
	path := writeTempArtifact(t, content)

	client := artifactClientWithRoots(t, []string{filepath.Dir(path)}, map[string]string{"FAKE_ARTIFACT_PATH": path})
	out, err := client.CallTool(context.Background(), "artifact_result", `{}`)
	if err != nil {
		t.Fatalf("CallTool artifact_result: %v", err)
	}

	// Model-facing rendering: the compact text plus one metadata line per
	// artifact (id, media type, size, short hash prefix, summary).
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])
	for _, want := range []string{
		"Analyzed 1284 test cases: 1280 passed, 4 failed.",
		`artifact "report-1"`,
		"text/plain",
		fmt.Sprintf("%d bytes", len(content)),
		"sha256 " + hash[:12],
		"Full CI test results (JUnit XML)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered result missing %q:\n%s", want, out)
		}
	}

	// The raw absolute path and the artifact content must never reach the
	// model-facing result.
	if strings.Contains(out, path) || strings.Contains(out, filepath.Base(path)) {
		t.Errorf("rendered result leaks the artifact path:\n%s", out)
	}
	if strings.Contains(out, "FAIL pkg/a TestX") {
		t.Errorf("rendered result leaks artifact content:\n%s", out)
	}
}

func TestContract_BadArtifactRejectedFailClosed(t *testing.T) {
	// WP3: artifact refs are validated before the result is rendered. A ref
	// pointing outside every configured root fails the whole call instead of
	// being delivered unvalidated (this flips the pre-WP3 fail-open pin).
	t.Run("outside configured roots", func(t *testing.T) {
		root := t.TempDir()
		outside := writeTempArtifact(t, "outside the allowed roots\n")
		client := artifactClientWithRoots(t, []string{root}, map[string]string{"FAKE_BAD_ARTIFACT_PATH": outside})
		_, err := client.CallTool(context.Background(), "bad_artifact", `{}`)
		if err == nil {
			t.Fatal("expected bad_artifact to fail closed")
		}
		for _, want := range []string{"contract", "bad_artifact", "evil-1", "outside"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), want)
			}
		}
	})

	t.Run("no roots configured", func(t *testing.T) {
		// Empty artifact_roots ⇒ every artifact ref is rejected, even for a
		// well-formed ref to a real file (WP2 fail-closed semantic).
		path := writeTempArtifact(t, "ok pkg/a 0.3s\n")
		client := artifactClient(t, map[string]string{"FAKE_ARTIFACT_PATH": path})
		_, err := client.CallTool(context.Background(), "artifact_result", `{}`)
		if err == nil {
			t.Fatal("expected artifact_result to fail closed with no configured roots")
		}
		if !strings.Contains(err.Error(), "no artifact roots") {
			t.Errorf("error = %q, want it to name the missing roots", err.Error())
		}
	})
}

func TestContract_TamperedArtifactRejected(t *testing.T) {
	// A ref whose declared hash or size does not match the real file fails
	// closed, even though the file sits inside a configured root.
	for name, tamper := range map[string]string{"hash mismatch": "hash", "size mismatch": "size"} {
		t.Run(name, func(t *testing.T) {
			path := writeTempArtifact(t, "ok pkg/a 0.3s\n")
			client := artifactClientWithRoots(t, []string{filepath.Dir(path)}, map[string]string{
				"FAKE_ARTIFACT_PATH":   path,
				"FAKE_ARTIFACT_TAMPER": tamper,
			})
			_, err := client.CallTool(context.Background(), "artifact_result", `{}`)
			if err == nil {
				t.Fatal("expected tampered artifact ref to fail closed")
			}
			if !strings.Contains(err.Error(), "mismatch") {
				t.Errorf("error = %q, want it to name the mismatch", err.Error())
			}
		})
	}
}

func TestContract_BadArtifactRootsRejected(t *testing.T) {
	// The configured root exists and the artifact file exists, but the file
	// lives outside the root: rejected. Also cover a symlink inside the root
	// pointing outside it (symlink escape).
	t.Run("file outside root", func(t *testing.T) {
		root := t.TempDir()
		outside := writeTempArtifact(t, "outside\n")
		client := artifactClientWithRoots(t, []string{root}, map[string]string{"FAKE_BAD_ARTIFACT_PATH": outside})
		if _, err := client.CallTool(context.Background(), "bad_artifact", `{}`); err == nil {
			t.Fatal("expected rejection for artifact outside the configured root")
		}
	})

	t.Run("symlink escape", func(t *testing.T) {
		root := t.TempDir()
		outside := writeTempArtifact(t, "escaped\n")
		link := filepath.Join(root, "linked-results.txt")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		client := artifactClientWithRoots(t, []string{root}, map[string]string{"FAKE_BAD_ARTIFACT_PATH": link})
		_, err := client.CallTool(context.Background(), "bad_artifact", `{}`)
		if err == nil {
			t.Fatal("expected rejection for a symlink escaping the configured root")
		}
		if !strings.Contains(err.Error(), "outside") {
			t.Errorf("error = %q, want it to name the root escape", err.Error())
		}
	})
}

// writeTempArtifact writes a neutral log-analysis fixture file and returns
// its path.
func writeTempArtifact(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ci-test-results.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}
