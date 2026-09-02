package main

// artifact_read (M2) — parent-only built-in that resolves a registered
// artifact id to validated content. The model supplies ONLY the id (plus an
// optional byte offset/limit); path resolution happens exclusively through
// the collation-time registry, so no model input ever reaches the
// filesystem as a path. Content is returned inside the untrusted boundary
// like every other child-derived tool result, and ingested into the audit
// log by the standard per-call recorder.
//
// TOCTOU hardening (2026-08 audit): collation-time artifact.Validate used to
// be the only gate, and the read path re-opened the file BY PATH with plain
// os.Stat/os.Open — both follow symlinks, and the stored sha256 was never
// re-checked. A same-user process could swap the artifact file for a symlink
// after collation and artifact_read would stream any readable file outside
// all roots into the parent context. verifyArtifactWindow now re-opens with
// O_NOFOLLOW + Lstat and re-hashes the served bytes against the ref's
// recorded sha256, fail-closed.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/artifact"
)

const (
	// artifactReadDefaultLimit is the per-call byte budget.
	artifactReadDefaultLimit = 64 << 10 // 64 KiB
	// artifactReadMaxLimit is the hard per-call cap; larger requests clamp.
	artifactReadMaxLimit = 256 << 10 // 256 KiB
)

// artifactPastEndError reports offset >= total; it carries the verified file
// size so the caller can render the friendly message with real numbers.
type artifactPastEndError struct{ total int64 }

func (e *artifactPastEndError) Error() string {
	return fmt.Sprintf("offset is past the end of %d bytes", e.total)
}

// shortDigest renders a digest prefix for error messages (keeps foreign
// digests out of long-form context noise).
func shortDigest(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}

// verifyArtifactWindow re-opens a registered artifact with read-time TOCTOU
// hardening and returns ONLY the requested [offset, offset+limit) window —
// and only after the whole file verifies:
//
//  1. os.Lstat rejects a symlinked final component without following it;
//  2. the open itself uses O_NOFOLLOW (unix), closing the Lstat→open race
//     on the final path component;
//  3. f.Stat inspects the OPEN inode (regular file, size within maxBytes,
//     still matching the ref's declared size_bytes) — later path swaps
//     cannot affect the open handle;
//  4. one bounded sequential pass streams SHA-256 over EVERY byte of the
//     current contents while capturing only the requested window, so the
//     served bytes are always a subset of the hashed bytes;
//  5. the digest must equal the ref's recorded sha256 — on any mismatch
//     nothing is returned (fail-closed on every doubt: missing sha256,
//     lstat/open/stat errors, size drift, digest mismatch).
//
// maxBytes bounds both the accepted file size and the streamed I/O.
// total is the verified file size (meaningful even on error); truncated
// reports that more bytes follow the returned window. Shared with
// renderArtifacts (subagent_tool.go), which inlines small text artifacts
// through the same gate.
func verifyArtifactWindow(path string, ref artifact.Ref, maxBytes, offset, limit int64) (window []byte, total int64, truncated bool, err error) {
	if ref.SHA256 == "" {
		// Without a recorded digest there is nothing to verify the current
		// contents against — a post-collation swap would be undetectable.
		return nil, 0, false, fmt.Errorf("ref carries no sha256; contents cannot be verified at read time")
	}
	// Lstat the final path component WITHOUT following it.
	info, err := os.Lstat(path)
	if err != nil {
		return nil, 0, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, false, fmt.Errorf("artifact path is a symlink; file was likely swapped after registration")
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("artifact path is not a regular file")
	}
	// O_NOFOLLOW makes the open itself refuse a final-component symlink
	// swapped in between the Lstat and the open.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, 0, false, err
	}
	defer f.Close()
	// fstat sees the inode the handle points at, not whatever the path
	// names now.
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, false, err
	}
	if !fi.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("open handle is not a regular file")
	}
	if fi.Size() > maxBytes {
		return nil, 0, false, fmt.Errorf("file is %d bytes; above the %d byte cap", fi.Size(), maxBytes)
	}
	if ref.SizeBytes != nil && fi.Size() != *ref.SizeBytes {
		return nil, 0, false, fmt.Errorf("file size changed since registration: ref declares %d bytes, file has %d", *ref.SizeBytes, fi.Size())
	}

	// Single bounded pass: hash every byte, capture the window.
	h := sha256.New()
	capture := limit + 1 // +1 byte detects truncation without a second stat
	skip := offset
	if skip < 0 {
		skip = 0
	}
	buf := make([]byte, 64<<10)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if total+int64(n) > maxBytes {
				return nil, total, false, fmt.Errorf("file grew past the %d byte cap while reading", maxBytes)
			}
			chunk := buf[:n]
			h.Write(chunk)
			if skip >= int64(n) {
				skip -= int64(n)
			} else {
				start := int(skip)
				skip = 0
				take := n - start
				if room := int(capture) - len(window); take > room {
					take = room
				}
				if take > 0 {
					window = append(window, chunk[start:start+take]...)
				}
			}
			total += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, total, false, rerr
		}
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != ref.SHA256 {
		return nil, total, false, fmt.Errorf("sha256 mismatch: ref declares %s, file hashes to %s", shortDigest(ref.SHA256), shortDigest(sum))
	}
	if offset >= total {
		return nil, total, false, &artifactPastEndError{total: total}
	}
	truncated = int64(len(window)) > limit
	if truncated {
		window = window[:limit]
	}
	return window, total, truncated, nil
}

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

	// Re-verify at read time (TOCTOU): the file is re-opened with symlink
	// protection and re-hashed against the ref's recorded sha256 before any
	// byte is served. MaxArtifactBytes mirrors the collation-time ceiling,
	// so a legit artifact always passes and a swapped-in bigger file is
	// rejected without reading it.
	data, total, truncated, err := verifyArtifactWindow(entry.Path, entry.Ref, artifact.MaxArtifactBytes, in.Offset, in.Limit)
	if err != nil {
		var pastEnd *artifactPastEndError
		switch {
		case errors.As(err, &pastEnd):
			return fmt.Sprintf(`{"error":"artifact %q is %d bytes; offset %d is past the end"}`, in.ID, pastEnd.total, in.Offset), nil
		case os.IsNotExist(err):
			// The janitor backstop or a session delete may have removed
			// the subtree since collation.
			return fmt.Sprintf(`{"error":"artifact %q is no longer available (removed by cleanup)"}`, in.ID), nil
		default:
			return fmt.Sprintf(`{"error":"artifact %q failed read-time verification: %v"}`, in.ID, err), nil
		}
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
	fmt.Fprintf(&b, "artifact %s (%s, %d bytes, sha256:%s) — task %d — bytes %d..%d of %d",
		entry.Ref.ID, entry.Ref.MediaType, size, shaPrefix, entry.TaskIdx+1, in.Offset, in.Offset+int64(len(data)), total)
	if truncated {
		b.WriteString(" — TRUNCATED, call again with offset to continue")
	}
	b.WriteString("\n\n")
	b.Write(data)

	// Child-derived content: inside the untrusted boundary, recorded by the
	// per-call audit ingest like every other tool result.
	return wrapUntrusted(t.toolCtx(), "artifact_read", b.String()), nil
}
