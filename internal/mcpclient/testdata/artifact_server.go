package main

// Mock "extension contract" MCP server, built from source at test time just
// like the base fakeserver (see client_test.go's fakeServerPath). It is
// enabled by setting FAKE_ARTIFACT_MODE=1 in the server environment and
// exposes the fixture tools used by the odek-extension/v1 contract tests:
//
//   echo            — returns its "text" argument verbatim
//   large_result    — returns a text result of "size" bytes (for limit tests)
//   artifact_result — returns an odek.tool-result/v1 envelope with a file://
//                     artifact ref (path from FAKE_ARTIFACT_PATH, sha256 and
//                     size computed from the real file)
//   bad_artifact    — returns an envelope whose artifact ref points outside
//                     any plausible allowed root (default
//                     /nonexistent/outside-allowed-roots.log, overridable via
//                     FAKE_BAD_ARTIFACT_PATH)
//   slow            — sleeps "seconds" (default 30) before answering
//   error_result    — returns a result with isError: true
//
// All fixtures are neutral (log-analysis style) and contain no secrets.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

var artifactMode = os.Getenv("FAKE_ARTIFACT_MODE") != ""

// artifactToolDefs is the tools/list payload served in artifact mode.
var artifactToolDefs = []map[string]any{
	{
		"name":        "echo",
		"description": "Echo the text argument back",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"text": map[string]any{"type": "string"}},
		},
	},
	{
		"name":        "large_result",
		"description": "Return a text result of the requested size in bytes",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"size": map[string]any{"type": "integer"}},
		},
	},
	{
		"name":        "artifact_result",
		"description": "Summarize a log file and return the full report as an artifact ref",
		"inputSchema": map[string]any{"type": "object"},
	},
	{
		"name":        "bad_artifact",
		"description": "Return an artifact ref outside the allowed roots",
		"inputSchema": map[string]any{"type": "object"},
	},
	{
		"name":        "slow",
		"description": "Sleep for the requested number of seconds before answering",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"seconds": map[string]any{"type": "integer"}},
		},
	},
	{
		"name":        "error_result",
		"description": "Always return a tool-level error (isError: true)",
		"inputSchema": map[string]any{"type": "object"},
	},
}

// textResult marshals a standard MCP tool result with a single text item.
func textResult(text string, isError bool) json.RawMessage {
	res, _ := json.Marshal(map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
		"isError": isError,
	})
	return res
}

// toolEnvelope builds an odek.tool-result/v1 envelope as a JSON string,
// suitable as the text of a single content item. Unknown extra fields are
// included deliberately to prove consumers ignore them.
func toolEnvelope(text string, artifacts []map[string]any) string {
	env := map[string]any{
		"schema":    "odek.tool-result/v1",
		"text":      text,
		"artifacts": artifacts,
		// Unknown field — consumers must ignore it (contract compat rule).
		"x_future_field": "ignored",
	}
	data, _ := json.Marshal(env)
	return string(data)
}

// artifactRef builds an odek.artifact-ref/v1 object for a real file on disk,
// computing size and sha256 from the actual content.
func artifactRef(id, path, summary string) map[string]any {
	ref := map[string]any{
		"schema":     "odek.artifact-ref/v1",
		"id":         id,
		"uri":        "file://" + path,
		"media_type": "text/plain",
		"summary":    summary,
	}
	if data, err := os.ReadFile(path); err == nil {
		sum := sha256.Sum256(data)
		ref["sha256"] = hex.EncodeToString(sum[:])
		ref["size_bytes"] = len(data)
	}
	return ref
}

// handleArtifactCall dispatches a tools/call request in artifact mode.
// It returns false if the tool name is unknown (caller emits a JSON-RPC error).
func handleArtifactCall(req request) bool {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeResp(req.ID, nil, &rpcError{Code: -32602, Message: "invalid params"})
		return true
	}
	var args map[string]any
	if len(params.Arguments) > 0 {
		json.Unmarshal(params.Arguments, &args)
	}

	switch params.Name {
	case "echo":
		text, _ := args["text"].(string)
		writeResp(req.ID, textResult("echo: "+text, false), nil)

	case "large_result":
		size := 1024
		if s, ok := args["size"].(float64); ok && s > 0 {
			size = int(s)
		}
		writeResp(req.ID, textResult(strings.Repeat("x", size), false), nil)

	case "artifact_result":
		path := os.Getenv("FAKE_ARTIFACT_PATH")
		if path == "" {
			writeResp(req.ID, textResult("FAKE_ARTIFACT_PATH not set", true), nil)
			return true
		}
		ref := artifactRef("report-1", path, "Full CI test results (JUnit XML)")
		// Test knobs: corrupt a verifiable field (WP3 fail-closed tests) or
		// override the envelope text (envelope truncation tests).
		switch os.Getenv("FAKE_ARTIFACT_TAMPER") {
		case "hash":
			ref["sha256"] = strings.Repeat("0", 64)
		case "size":
			if n, ok := ref["size_bytes"].(int); ok {
				ref["size_bytes"] = n + 1
			}
		}
		text := os.Getenv("FAKE_ARTIFACT_TEXT")
		if text == "" {
			text = "Analyzed 1284 test cases: 1280 passed, 4 failed. Full report attached as artifact report-1."
		}
		env := toolEnvelope(text, []map[string]any{ref})
		writeResp(req.ID, textResult(env, false), nil)

	case "bad_artifact":
		path := os.Getenv("FAKE_BAD_ARTIFACT_PATH")
		if path == "" {
			path = "/nonexistent/outside-allowed-roots.log"
		}
		ref := map[string]any{
			"schema":     "odek.artifact-ref/v1",
			"id":         "evil-1",
			"uri":        "file://" + path,
			"media_type": "text/plain",
			"summary":    "Ref outside every configured artifact root",
		}
		env := toolEnvelope("Analysis complete, see artifact evil-1.", []map[string]any{ref})
		writeResp(req.ID, textResult(env, false), nil)

	case "slow":
		secs := 30
		if s, ok := args["seconds"].(float64); ok && s > 0 {
			secs = int(s)
		}
		time.Sleep(time.Duration(secs) * time.Second)
		writeResp(req.ID, textResult("slow: done", false), nil)

	case "error_result":
		writeResp(req.ID, textResult("simulated tool failure: log file is empty", true), nil)

	default:
		writeResp(req.ID, nil, &rpcError{Code: -32602, Message: fmt.Sprintf("unknown tool: %s", params.Name)})
	}
	return true
}
