package main

// Sub-agent result artifacts — M1 core protocol
// (SUBAGENT_RESULT_ARTIFACTS_PLAN.md).
//
// Layout: ~/.odek/artifacts/<session_id>/<task_id>/<file>. The parent
// creates the per-task dir and passes the child-visible path in the task
// envelope; the child runner scans it at exit and builds
// odek.artifact-ref/v1 refs (hashes/sizes measured by the RUNNER, never
// model-fabricated); the parent validates fail-closed against the per-task
// root before anything reaches the model context. Session cleanup cascades:
// internal/session's OnDelete hook removes the session subtree (wired by
// enableArtifactCascade), and the maintenance janitor backstops with an
// age-based sweep.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BackendStack21/odek/internal/artifact"
	"github.com/BackendStack21/odek/internal/maintenance"
)

const (
	// artifactsDirName is the artifacts home under the odek dir.
	artifactsDirName = "artifacts"
	// maxArtifactTaskBudget caps the total artifact bytes one task may
	// deliver (largest-first drop at scan time).
	maxArtifactTaskBudget = int64(64 << 20) // 64 MiB
	// maxInlineArtifactBytes caps text artifacts inlined into the parent
	// summary; larger ones stay file-backed until artifact_read (M2).
	maxInlineArtifactBytes = 32 << 10 // 32 KiB
	// unfiledSessionDir holds tasks whose parent session id is unknown.
	// Aliases the maintenance package's bucket constant so the filer and
	// the sweeper can never disagree on the name.
	unfiledSessionDir = maintenance.UnfiledArtifactsDir
	// maxArtifactSummaryLine caps the per-artifact one-line summary.
	maxArtifactSummaryLine = 120
)

// artifactIDRe constrains artifact ids (derived from filenames) to a safe
// charset: ids are model-influenced content and must not smuggle separators
// or traversal into renders or paths.
var artifactIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// sessionIDRe mirrors the session-id charset enforced by
// session.ValidateSessionID for cascade path derivation.
var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// artifactsHome returns <home>/.odek/artifacts.
func artifactsHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("artifacts home: %w", err)
	}
	return filepath.Join(home, ".odek", artifactsDirName), nil
}

// taskArtifactDir returns (and creates, 0700) the per-task artifact
// directory under the given root. A valid parent session id files under
// <root>/<sid>/ so the session-delete cascade owns the subtree; anything
// else falls back to the defensive "unfiled" bucket, which the janitor
// sweeps at task granularity (internal/maintenance sweepArtifacts).
func taskArtifactDir(root, parentSessionID, taskID string) (string, error) {
	sid := parentSessionID
	if !sessionIDRe.MatchString(sid) {
		sid = unfiledSessionDir
	}
	dir := filepath.Join(root, sid, taskID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("artifact dir: %w", err)
	}
	return dir, nil
}

// mediaTypeFor maps a filename extension to a coarse media type. Unknown
// extensions are application/octet-stream (never inlined).
func mediaTypeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt", ".log":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	case ".html", ".htm":
		return "text/html"
	case ".yaml", ".yml":
		return "text/yaml"
	case ".xml":
		return "text/xml"
	default:
		return "application/octet-stream"
	}
}

// sanitizeArtifactID derives a safe ref id from a filename stem. Unusable
// stems fall back to "artifact-<n>" (caller assigns), signaled by "".
func sanitizeArtifactID(name string) string {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	stem = strings.TrimSpace(stem)
	if !artifactIDRe.MatchString(stem) {
		return ""
	}
	return stem
}

// scanArtifacts builds odek.artifact-ref/v1 refs from the task artifact
// dir: top-level regular files only (symlinks resolved away by design —
// a symlinked artifact is an escape attempt and is skipped with a flag).
// sha256 and size are measured by the runner. Enforces
// artifact.MaxArtifactsPerEnvelope and the per-task byte budget
// (largest files first). Flags explain every drop; the child's summary
// carries them so the parent sees why an artifact is missing.
func scanArtifacts(dir string, budget int64) ([]artifact.Ref, []string) {
	var refs []artifact.Ref
	var flags []string
	if dir == "" {
		return nil, nil
	}

	type candidate struct {
		path string
		name string
		size int64
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("[artifact] scan failed: %v", err)}
	}

	var cands []candidate
	for _, e := range entries {
		if e.IsDir() {
			flags = append(flags, fmt.Sprintf("[artifact] skipped %q: directories are not collected", e.Name()))
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			flags = append(flags, fmt.Sprintf("[artifact] skipped %q: not a regular file", e.Name()))
			continue
		}
		if info.Size() > artifact.MaxArtifactBytes {
			flags = append(flags, fmt.Sprintf("[artifact] dropped %q: %d bytes exceeds the per-artifact cap", e.Name(), info.Size()))
			continue
		}
		cands = append(cands, candidate{path: filepath.Join(dir, e.Name()), name: e.Name(), size: info.Size()})
	}

	// Largest first so the budget keeps the biggest deliverables.
	sort.Slice(cands, func(i, j int) bool { return cands[i].size > cands[j].size })

	var used int64
	for _, c := range cands {
		if len(refs) >= artifact.MaxArtifactsPerEnvelope {
			flags = append(flags, fmt.Sprintf("[artifact] dropped %q: ref cap (%d) reached", c.name, artifact.MaxArtifactsPerEnvelope))
			continue
		}
		if used+c.size > budget {
			flags = append(flags, fmt.Sprintf("[artifact] dropped %q: task budget (%d bytes) exhausted", c.name, budget))
			continue
		}
		f, err := os.Open(c.path)
		if err != nil {
			flags = append(flags, fmt.Sprintf("[artifact] skipped %q: %v", c.name, err))
			continue
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			flags = append(flags, fmt.Sprintf("[artifact] skipped %q: hash failed: %v", c.name, err))
			continue
		}
		used += c.size

		id := sanitizeArtifactID(c.name)
		if id == "" {
			id = fmt.Sprintf("artifact-%d", len(refs)+1)
		}
		ref := artifact.Ref{
			Schema:    artifact.SchemaArtifactRef,
			ID:        id,
			URI:       "file://" + c.path,
			MediaType: mediaTypeFor(c.name),
			SHA256:    hex.EncodeToString(h.Sum(nil)),
			SizeBytes: &c.size,
			Summary:   firstLine(c.path, maxArtifactSummaryLine),
		}
		refs = append(refs, ref)
	}
	return refs, flags
}

// firstLine reads the first line of a file, capped. Best-effort: an
// unreadable file yields an empty summary (the ref stays valid).
func firstLine(path string, cap int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, cap*4)
	n, _ := f.Read(buf)
	if n == 0 {
		return ""
	}
	line := string(buf[:n])
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "\r"))
	runes := []rune(line)
	if len(runes) > cap {
		runes = runes[:cap]
	}
	return string(runes)
}
