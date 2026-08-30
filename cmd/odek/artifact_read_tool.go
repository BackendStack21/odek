package main

// artifact_read (M2) — parent-only built-in that resolves a registered
// artifact id to validated content. The model supplies ONLY the id (plus an
// optional byte offset/limit); path resolution happens exclusively through
// the collation-time registry, so no model input ever reaches the
// filesystem as a path. Content is returned inside the untrusted boundary
// like every other child-derived tool result, and ingested into the audit
// log by the standard per-call recorder.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/BackendStack21/odek"
)

const (
	// artifactReadDefaultLimit is the per-call byte budget.
	artifactReadDefaultLimit = 64 << 10 // 64 KiB
	// artifactReadMaxLimit is the hard per-call cap; larger requests clamp.
	artifactReadMaxLimit = 256 << 10 // 256 KiB
)

// artifactReadTool reads registered sub-agent result artifacts by id.
type artifactReadTool struct {
	ctxTool
}

var _ odek.Tool = (*artifactReadTool)(nil)

func (t *artifactReadTool) Name() string { return "artifact_read" }

func (t *artifactReadTool) Description() string {
	return `Read the full content of a sub-agent result artifact registered this session. Use it when a delegate_tasks result references artifacts whose inlined preview is missing or truncated.

- id: the artifact id from the delegate_tasks result (required)
- offset: byte offset to start reading from (default 0)
- limit: max bytes to return per call (default 65536; hard cap 262144)

Paths are resolved internally from the session registry — never pass file paths. Parent-side tool only.`
}

func (t *artifactReadTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "string",
				"description": "Artifact id from the delegate_tasks result metadata.",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Byte offset to start reading from (default 0).",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Max bytes per call (default 65536, hard cap 262144).",
			},
		},
		"required": []string{"id"},
	}
}

// artifactReadArgs is the tool input contract.
type artifactReadArgs struct {
	ID     string `json:"id"`
	Offset int64  `json:"offset"`
	Limit  int64  `json:"limit"`
}

func (t *artifactReadTool) Call(args string) (string, error) {
	var in artifactReadArgs
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return fmt.Sprintf(`{"error":"parse failed: %v"}`, err), nil
	}
	if in.ID == "" {
		return `{"error":"id is required"}`, nil
	}
	if in.Offset < 0 {
		in.Offset = 0
	}
	if in.Limit <= 0 {
		in.Limit = artifactReadDefaultLimit
	}
	if in.Limit > artifactReadMaxLimit {
		in.Limit = artifactReadMaxLimit
	}

	entry, ok := lookupSubagentArtifact(in.ID)
	if !ok {
		return fmt.Sprintf(`{"error":"unknown artifact id %q — registered artifacts: %s"}`, in.ID, artifactIDList()), nil
	}

	// Re-verify at read time: the janitor backstop or a session delete may
	// have removed the subtree since collation.
	info, err := os.Stat(entry.Path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Sprintf(`{"error":"artifact %q is no longer available (removed by cleanup)"}`, in.ID), nil
	}
	if in.Offset >= info.Size() {
		return fmt.Sprintf(`{"error":"artifact %q is %d bytes; offset %d is past the end"}`, in.ID, info.Size(), in.Offset), nil
	}

	f, err := os.Open(entry.Path)
	if err != nil {
		return fmt.Sprintf(`{"error":"artifact %q unreadable: %v"}`, in.ID, err), nil
	}
	defer f.Close()
	if _, err := f.Seek(in.Offset, io.SeekStart); err != nil {
		return fmt.Sprintf(`{"error":"artifact %q seek failed: %v"}`, in.ID, err), nil
	}
	// Read one extra byte to detect truncation without a second stat.
	data, err := io.ReadAll(io.LimitReader(f, in.Limit+1))
	if err != nil {
		return fmt.Sprintf(`{"error":"artifact %q read failed: %v"}`, in.ID, err), nil
	}
	truncated := int64(len(data)) > in.Limit
	if truncated {
		data = data[:in.Limit]
	}

	size := int64(0)
	if entry.Ref.SizeBytes != nil {
		size = *entry.Ref.SizeBytes
	}
	shaPrefix := entry.Ref.SHA256
	if len(shaPrefix) > 12 {
		shaPrefix = shaPrefix[:12]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "artifact %s (%s, %d bytes, sha256:%s) — bytes %d..%d of %d",
		entry.Ref.ID, entry.Ref.MediaType, size, shaPrefix, in.Offset, in.Offset+int64(len(data)), info.Size())
	if truncated {
		b.WriteString(" — TRUNCATED, call again with offset to continue")
	}
	b.WriteString("\n\n")
	b.Write(data)

	// Child-derived content: inside the untrusted boundary, recorded by the
	// per-call audit ingest like every other tool result.
	return wrapUntrusted(t.toolCtx(), "artifact_read", b.String()), nil
}
