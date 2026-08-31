package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// MaxArtifactBytes is the absolute ceiling on a single artifact file that
// Validate will process (stat or hash). Enforced at Stat time so a server
// that supplies sha256 but omits size_bytes cannot force an unbounded
// streaming hash (see Validate), and re-enforced at hash time: the file is
// read through a LimitReader capped at MaxArtifactBytes+1, so a file that
// grows between the stat and the hash is rejected instead of read
// unbounded (see fileSHA256).
const MaxArtifactBytes int64 = 64 << 20

// Validate checks an artifact ref fail-closed against the configured artifact
// roots and returns the resolved (symlink-evaluated) absolute path of the
// artifact file. The returned path is for local bookkeeping only (e.g. a
// future event log); it must never be rendered into the model-facing result.
//
// Every violation is an error — there is no partial acceptance:
//   - ref.Schema must match SchemaArtifactRef exactly
//   - id, uri, and media_type are required
//   - roots must be non-empty (empty roots ⇒ every ref is rejected)
//   - uri must be a file:// URI with no host, query, or fragment
//   - the path must be absolute and clean (no "..", "//", or "." elements;
//     percent-encoded traversal is decoded before this check)
//   - the path must exist and, after filepath.EvalSymlinks, stay inside one
//     of the roots (symlink escapes are rejected)
//   - the target must be a regular file
//   - the file must be at most MaxArtifactBytes (checked before hashing)
//   - size_bytes, when present, must equal the real file size
//   - sha256, when present, must be a lowercase hex digest matching the file
//
// The artifact content is only ever read to compute the verification hash; it
// is never returned to the caller.
func Validate(ref Ref, roots []string) (string, error) {
	if ref.Schema != SchemaArtifactRef {
		return "", fmt.Errorf("artifact schema %q does not match %q", ref.Schema, SchemaArtifactRef)
	}
	if ref.ID == "" {
		return "", fmt.Errorf("artifact ref is missing required field id")
	}
	if ref.URI == "" {
		return "", fmt.Errorf("artifact %q is missing required field uri", ref.ID)
	}
	if ref.MediaType == "" {
		return "", fmt.Errorf("artifact %q is missing required field media_type", ref.ID)
	}
	if len(roots) == 0 {
		return "", fmt.Errorf("artifact %q rejected: no artifact roots configured for this server", ref.ID)
	}

	u, err := url.Parse(ref.URI)
	if err != nil {
		return "", fmt.Errorf("artifact %q has an unparseable uri: %w", ref.ID, err)
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("artifact %q uses uri scheme %q; only file:// is accepted", ref.ID, u.Scheme)
	}
	if u.Host != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("artifact %q uri must be a plain file:// path (no host, userinfo, query, or fragment)", ref.ID)
	}
	// url.Parse has already percent-decoded u.Path, so encoded traversal
	// sequences (%2e%2e) are caught by the cleanliness check below.
	path := u.Path
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("artifact %q path %q is not absolute", ref.ID, path)
	}
	if filepath.Clean(path) != path {
		return "", fmt.Errorf("artifact %q path %q is not clean (contains .., //, or . elements)", ref.ID, path)
	}

	// Resolve symlinks on the artifact path. A missing file fails here, which
	// is the required fail-closed behavior for nonexistent artifacts.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("artifact %q file is not resolvable (missing or dangling symlink): %w", ref.ID, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("artifact %q: resolve absolute path: %w", ref.ID, err)
	}

	inside, err := insideRoots(resolved, roots)
	if err != nil {
		return "", fmt.Errorf("artifact %q: %w", ref.ID, err)
	}
	if !inside {
		return "", fmt.Errorf("artifact %q resolves outside every configured artifact root", ref.ID)
	}

	fi, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("artifact %q: stat: %w", ref.ID, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("artifact %q is not a regular file", ref.ID)
	}

	// Absolute size ceiling, checked at Stat time before any hashing
	// (audit 2026-08): a ref carrying sha256 but omitting size_bytes used
	// to force an unbounded streaming hash of whatever lives under the
	// server's roots — gigabytes of local I/O per tool call that no
	// per-server timeout bounds. 64 MiB matches the max_response_bytes
	// ceiling; larger outputs should not travel as single artifacts.
	if fi.Size() > MaxArtifactBytes {
		return "", fmt.Errorf("artifact %q is %d bytes; the absolute artifact cap is %d bytes", ref.ID, fi.Size(), MaxArtifactBytes)
	}

	if ref.SizeBytes != nil {
		if *ref.SizeBytes < 0 {
			return "", fmt.Errorf("artifact %q declares a negative size_bytes", ref.ID)
		}
		if fi.Size() != *ref.SizeBytes {
			return "", fmt.Errorf("artifact %q size mismatch: ref declares %d bytes, file has %d", ref.ID, *ref.SizeBytes, fi.Size())
		}
	}

	if ref.SHA256 != "" {
		if !isLowerHexSHA256(ref.SHA256) {
			return "", fmt.Errorf("artifact %q sha256 %q is not a lowercase hex SHA-256 digest", ref.ID, ref.SHA256)
		}
		sum, err := fileSHA256(resolved, fi.Size())
		if err != nil {
			return "", fmt.Errorf("artifact %q: hash: %w", ref.ID, err)
		}
		if sum != ref.SHA256 {
			return "", fmt.Errorf("artifact %q sha256 mismatch: ref declares %s, file hashes to %s", ref.ID, shortHash(ref.SHA256), shortHash(sum))
		}
	}

	return resolved, nil
}

// insideRoots reports whether resolved lies inside at least one root. Roots
// are resolved the same way as the artifact path (absolute + symlink
// evaluation) so a root that is itself a symlink still confines correctly.
// Roots that do not resolve (missing, dangling) are skipped — they cannot
// confine anything.
func insideRoots(resolved string, roots []string) (bool, error) {
	for _, root := range roots {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return false, fmt.Errorf("resolve artifact root %q: %w", root, err)
		}
		resolvedRoot, err := filepath.EvalSymlinks(absRoot)
		if err != nil {
			continue
		}
		if resolved == resolvedRoot || strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) {
			return true, nil
		}
	}
	return false, nil
}

// isLowerHexSHA256 reports whether s is exactly 64 lowercase hex characters.
func isLowerHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// fileSHA256 streams the file through a SHA-256 hasher, reading at most
// limit+1 bytes: content beyond limit is rejected, never hashed. The stat
// that capped the file size happened earlier — possibly with the file
// smaller than it is now — so the hash read must re-enforce the cap itself
// (audit 2026-08: a file that grew between the os.Stat and the open
// defeated MaxArtifactBytes via unbounded io.Copy). The content is used
// solely for verification and is never exposed to the model.
func fileSHA256(path string, limit int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return hashLimited(f, limit)
}

// hashLimited hashes r, reading at most limit+1 bytes. Reading more than
// limit is an error: the caller bounded the source by limit, so a longer
// read means the source grew (or was misbounded) — fail closed.
func hashLimited(r io.Reader, limit int64) (string, error) {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, limit+1))
	if err != nil {
		return "", err
	}
	if n > limit {
		return "", fmt.Errorf("content is at least %d bytes; the absolute artifact cap is %d bytes", n, limit)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
