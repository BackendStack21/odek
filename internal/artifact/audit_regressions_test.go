package artifact

// Regression tests for the 2026-08 audit: (1) a ref with sha256 set but
// size_bytes omitted forced an unbounded streaming hash of whatever lived
// under the server's artifact roots — multi-gigabyte local I/O per tool
// call that no per-server timeout bounds; (2) the per-artifact metadata
// lines Render appends were uncapped in count, bypassing max_result_chars.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestArtifact(t *testing.T, dir, name string, size int, withSHA bool) (string, Ref) {
	t.Helper()
	data := make([]byte, size)
	for i := range data {
		data[i] = byte('a' + i%26)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	ref := Ref{
		Schema:    SchemaArtifactRef,
		ID:        name,
		URI:       "file://" + path,
		MediaType: "application/octet-stream",
	}
	if withSHA {
		sum := sha256.Sum256(data)
		s := hex.EncodeToString(sum[:])
		ref.SHA256 = s
	}
	return path, ref
}

func TestAudit_ValidateRejectsHugeArtifactBeforeHashing(t *testing.T) {
	root := t.TempDir()
	// sha256 present, size_bytes omitted — the audit's unbounded-hash path.
	// The file exceeds the absolute cap, so validation must fail at Stat
	// time without reading the file.
	_, ref := writeTestArtifact(t, root, "huge.bin", int(MaxArtifactBytes)+1, true)

	if _, err := Validate(ref, []string{root}); err == nil {
		t.Fatal("Validate accepted an artifact above the absolute cap")
	} else if !strings.Contains(err.Error(), "absolute artifact cap") {
		t.Fatalf("Validate error should name the cap, got: %v", err)
	}
}

func TestAudit_ValidateStillHashesSmallArtifacts(t *testing.T) {
	root := t.TempDir()
	_, ref := writeTestArtifact(t, root, "ok.bin", 4096, true)
	if _, err := Validate(ref, []string{root}); err != nil {
		t.Fatalf("Validate(small artifact with sha256) = %v, want nil", err)
	}
	// A wrong digest must still fail.
	ref.SHA256 = strings.Repeat("0", 64)
	if _, err := Validate(ref, []string{root}); err == nil {
		t.Fatal("Validate accepted a sha256 mismatch")
	}
}

func TestAudit_ParseEnvelopeCapsArtifactCount(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"schema":"` + SchemaToolResult + `","text":"x","artifacts":[`)
	for i := 0; i <= MaxArtifactsPerEnvelope; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"schema":"%s","id":"a%d","uri":"file:///tmp/a%d","media_type":"text/plain"}`, SchemaArtifactRef, i, i)
	}
	sb.WriteString(`]}`)

	if _, err := ParseEnvelope(sb.String()); err == nil {
		t.Fatal("ParseEnvelope accepted more artifacts than the cap")
	} else if !strings.Contains(err.Error(), "the cap is") {
		t.Fatalf("ParseEnvelope error should name the cap, got: %v", err)
	}

	// Exactly at the cap is fine.
	env := &Envelope{Schema: SchemaToolResult, Text: "x"}
	for i := 0; i < MaxArtifactsPerEnvelope; i++ {
		env.Artifacts = append(env.Artifacts, Ref{Schema: SchemaArtifactRef, ID: fmt.Sprintf("a%d", i), URI: "file:///tmp/x", MediaType: "text/plain"})
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseEnvelope(string(raw)); err != nil {
		t.Fatalf("ParseEnvelope(at cap) = %v, want nil", err)
	}
}
