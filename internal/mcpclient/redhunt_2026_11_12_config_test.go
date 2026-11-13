package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// cfgHuntEnvelopeServer is a minimal Streamable HTTP MCP server whose
// tools/call result is an odek.tool-result/v1 envelope carrying one artifact
// ref built by ref.
func cfgHuntEnvelopeServer(t *testing.T, ref map[string]any) *httptest.Server {
	t.Helper()
	env, err := json.Marshal(map[string]any{
		"schema":    "odek.tool-result/v1",
		"text":      "ok",
		"artifacts": []any{ref},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": string(env)}},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// max_result_chars is documented as "an absolute cap on the model-facing
// result, including isError text and the message of a JSON-RPC error"
// (docs/EXTENSIONS.md), and both of those error channels are capped. The
// artifact-ref rejection channel is not: when a ref fails validation (here
// the default, fail-closed empty artifact_roots) CallTool returns an error
// that embeds the server-chosen ref id verbatim (%q), and the loop hands
// err.Error() to the model. A server that would be cut to 1000 chars on
// every other channel pushes a 50 000-char id (bounded only by
// max_response_bytes, 10 MiB by default) into the context through it —
// unsanitised by artifact.SanitizeText and unbounded by artifact.MaxFieldRunes.
func TestRED_ArtifactRejectionErrorRespectsResultCap(t *testing.T) {
	const limit = 1000
	srv := cfgHuntEnvelopeServer(t, map[string]any{
		"schema":     "odek.artifact-ref/v1",
		"id":         strings.Repeat("A", 50000),
		"uri":        "file:///tmp/x.txt",
		"media_type": "text/plain",
	})
	client, err := New("cfghunt", ServerConfig{URL: srv.URL, MaxResultChars: limit})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	_, err = client.CallTool(context.Background(), "export", `{}`)
	if err == nil {
		t.Fatal("expected the artifact ref to be rejected (no artifact_roots configured)")
	}
	if n := utf8.RuneCountInString(err.Error()); n > limit+256 {
		t.Errorf("artifact rejection error = %d chars of server-controlled text, want <= %d (max_result_chars=%d + prefix)", n, limit+256, limit)
	}
}
