package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzParseEnvelope feeds arbitrary tool-result text into ParseEnvelope and
// asserts it never panics, never treats non-envelope input as an envelope,
// and that any successfully parsed envelope renders without paths or
// envelope-schema markers leaking into the model-facing output.
func FuzzParseEnvelope(f *testing.F) {
	seeds := []string{
		`{"schema":"odek.tool-result/v1","text":"summary","artifacts":[{"schema":"odek.artifact-ref/v1","id":"a1","uri":"file:///tmp/out.json","media_type":"application/json"}]}`,
		`{"schema":"odek.tool-result/v1","text":"no artifacts"}`,
		`{"schema":"odek.tool-result/v1","text":"x","artifacts":"not-an-array"}`,
		`{"schema":"odek.tool-result/v1","artifacts":[{"id":123}]}`,
		`{"schema":"odek.tool-result/v1"}`,
		`{"schema":"odek.tool-result/v1","text":""}`,
		`{"schema":"other/v9","text":"plain"}`,
		`{"schema":"ODEK.TOOL-RESULT/V1","text":"case"}`,
		`plain text result`,
		``,
		`   `,
		`{not json`,
		`[]`,
		`null`,
		`{"schema":["array"]}`,
		`{"schema":"odek.tool-result/v1","text":"x","artifacts":[null,{},[1]]}`,
		`{"schema":"odek.tool-result/v1","text":"x","artifacts":[{"schema":"odek.artifact-ref/v1","id":"a","uri":"file:///etc/passwd","media_type":"text/plain","sha256":"` + strings.Repeat("0", 64) + `","size_bytes":-5}]}`,
		`{"schema":"odek.tool-result/v1","text":"x","unknown_field":{"nested":[1,2,3]}}`,
		`{"schema":"odek.tool-result/v1","text":"x"}` + strings.Repeat(" ", 1024) + `garbage`,
		`{"schema":"odek.tool-result/v1","text":"line1\nline2","summary":"truncated"}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, text string) {
		env, err := ParseEnvelope(text)
		if err != nil {
			// Fail-closed on schema-claimed-but-malformed input: no envelope.
			if env != nil {
				t.Fatalf("ParseEnvelope returned both envelope and error")
			}
			if !strings.Contains(strings.TrimSpace(text), SchemaToolResult) {
				t.Fatalf("ParseEnvelope rejected text that never claimed the envelope schema")
			}
			return
		}
		if env == nil {
			return // plain-text passthrough
		}
		if env.Schema != SchemaToolResult {
			t.Fatalf("parsed envelope with schema %q", env.Schema)
		}
		// Render must never panic and must never include artifact URIs/paths.
		// (Only checked for distinctive URIs — a 1-2 char URI trivially
		// appears in any output as a substring of other text.)
		out := Render(env)
		for _, a := range env.Artifacts {
			if len(a.URI) >= 8 && strings.Contains(out, a.URI) {
				t.Fatalf("Render leaked artifact URI %q into model-facing output", a.URI)
			}
		}
		if n := CountRendered(out); n != len(env.Artifacts) {
			t.Fatalf("CountRendered=%d, want %d", n, len(env.Artifacts))
		}
	})
}

// FuzzValidateRef unmarshals arbitrary JSON into a Ref and validates it
// against a real artifact root, asserting the fail-closed invariants: no
// panic, no acceptance of non-file schemes, traversal, control-char or
// non-clean paths, and no acceptance of refs whose file escapes the root.
func FuzzValidateRef(f *testing.F) {
	seeds := []string{
		`{"schema":"odek.artifact-ref/v1","id":"ok","uri":"file://%URI%","media_type":"text/plain"}`,
		`{"schema":"odek.artifact-ref/v1","id":"ok","uri":"file://%URI%","media_type":"text/plain","size_bytes":%SIZE%}`,
		`{"schema":"odek.artifact-ref/v1","id":"ok","uri":"file://%URI%","media_type":"text/plain","sha256":"%SHA%"}`,
		`{"schema":"wrong/v1","id":"x","uri":"file://%URI%","media_type":"text/plain"}`,
		`{"schema":"odek.artifact-ref/v1","uri":"file://%URI%","media_type":"text/plain"}`,
		`{"schema":"odek.artifact-ref/v1","id":"x","media_type":"text/plain"}`,
		`{"schema":"odek.artifact-ref/v1","id":"x","uri":"http://evil.test/%URI%","media_type":"text/plain"}`,
		`{"schema":"odek.artifact-ref/v1","id":"x","uri":"file:///etc/passwd","media_type":"text/plain"}`,
		`{"schema":"odek.artifact-ref/v1","id":"x","uri":"file://%ROOT%/../escape.txt","media_type":"text/plain"}`,
		`{"schema":"odek.artifact-ref/v1","id":"x","uri":"file://%ROOT%/%2e%2e/%2e%2e/etc/passwd","media_type":"text/plain"}`,
		`{"schema":"odek.artifact-ref/v1","id":"x","uri":"file://host/%URI%","media_type":"text/plain"}`,
		`{"schema":"odek.artifact-ref/v1","id":"x","uri":"file://%URI%?q=1","media_type":"text/plain"}`,
		`{"schema":"odek.artifact-ref/v1","id":"x","uri":"file://%URI%#frag","media_type":"text/plain"}`,
		`{"schema":"odek.artifact-ref/v1","id":"x","uri":"relative/path.txt","media_type":"text/plain"}`,
		`{"schema":"odek.artifact-ref/v1","id":"x","uri":"FILE://%URI%","media_type":"text/plain"}`,
		`{"schema":"odek.artifact-ref/v1","id":"x","uri":"file://%URI%","media_type":"text/plain","sha256":"ABCDEF"}`,
		`{"schema":"odek.artifact-ref/v1","id":"x","uri":"file://%URI%","media_type":"text/plain","sha256":"` + strings.Repeat("g", 64) + `"}`,
		`{"schema":"odek.artifact-ref/v1","id":"x","uri":"file://%URI%","media_type":"text/plain","size_bytes":-1}`,
		`{"schema":"odek.artifact-ref/v1","id":"x","uri":"file://%URI%","media_type":"text/plain","size_bytes":999999}`,
		`{"schema":"odek.artifact-ref/v1","id":"\u0000nul","uri":"file://%URI%","media_type":"text/plain"}`,
		`not json`,
		`{}`,
		`[]`,
		`null`,
		`{"schema":"odek.artifact-ref/v1","id":"x","uri":"file://%ROOT%/missing.bin","media_type":"application/octet-stream"}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data string) {
		root := t.TempDir()
		content := []byte("artifact payload\n")
		target := filepath.Join(root, "out.txt")
		if err := os.WriteFile(target, content, 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content)

		data = strings.ReplaceAll(data, "%URI%", target)
		data = strings.ReplaceAll(data, "%ROOT%", root)
		data = strings.ReplaceAll(data, "%SHA%", hex.EncodeToString(sum[:]))
		data = strings.ReplaceAll(data, "%SIZE%", "17")

		var ref Ref
		if err := json.Unmarshal([]byte(data), &ref); err != nil {
			// Unparseable JSON never reaches Validate in production
			// (ParseEnvelope fails closed first), but Validate must still
			// reject the zero ref rather than panic.
			if _, verr := Validate(ref, []string{root}); verr == nil {
				t.Fatalf("Validate accepted zero-value ref from unparseable input")
			}
			return
		}

		resolved, err := Validate(ref, []string{root})
		if err != nil {
			return
		}
		// Accepted — every postcondition must hold.
		if ref.Schema != SchemaArtifactRef || ref.ID == "" || ref.MediaType == "" {
			t.Fatalf("Validate accepted ref missing required fields: %+v", ref)
		}
		if !strings.HasPrefix(resolved, root+string(filepath.Separator)) && resolved != root {
			// Note: root may itself resolve through symlinks (e.g. macOS
			// /var → /private/var), so compare against the resolved root.
			resolvedRoot, rerr := filepath.EvalSymlinks(root)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if !strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) && resolved != resolvedRoot {
				t.Fatalf("Validate accepted path %q outside root %q (resolved %q)", resolved, root, resolvedRoot)
			}
		}
		fi, err := os.Stat(resolved)
		if err != nil || !fi.Mode().IsRegular() {
			t.Fatalf("Validate accepted non-regular file %q: %v", resolved, err)
		}
		if ref.SizeBytes != nil && fi.Size() != *ref.SizeBytes {
			t.Fatalf("Validate accepted size mismatch: declared %d, actual %d", *ref.SizeBytes, fi.Size())
		}
		if ref.SHA256 != "" {
			got, herr := fileSHA256(resolved)
			if herr != nil || got != ref.SHA256 {
				t.Fatalf("Validate accepted sha256 mismatch: declared %q", ref.SHA256)
			}
		}

		// Empty roots must reject everything.
		if _, err := Validate(ref, nil); err == nil {
			t.Fatalf("Validate accepted ref %+v with no roots configured", ref)
		}
	})
}
