package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/redact"
)

// TestRED_BufferTruncatesBeforeRedactingSoSecretPrefixLeaks: AppendBuffer
// runs summarizeForBuffer (which hard-cuts a space-free token at 200 runes)
// BEFORE redact.RedactSecrets. A secret straddling the cut loses its tail, no
// longer matches its pattern, and the surviving prefix is kept in the buffer,
// injected into the system prompt and persisted in Session.Buffer (the store's
// save-time redaction sees the same cut text). The episode writer documents
// and applies the opposite order for exactly this reason.
func TestRED_BufferTruncatesBeforeRedactingSoSecretPrefixLeaks(t *testing.T) {
	const token = "ghp_abcdefghijklmnopqrstuvwxyz1234567890"
	// Minified JSON (tool/CLI output pasted by the user): no spaces and no
	// sentence terminator in the second half of the window.
	msg := `{"note":"` + strings.Repeat("a", 160) + `","gh":"` + token + `"}`
	if strings.Contains(redact.RedactSecrets(msg), "abcdefghijklmnop") {
		t.Skip("redactor does not cover this token in full text; case not applicable")
	}
	mm := NewMemoryManager(t.TempDir(), nil, DefaultMemoryConfig())
	mm.AppendBuffer("user", msg)
	for _, l := range mm.GetBuffer() {
		if strings.Contains(l, "ghp_abcdefghijklmnop") {
			t.Fatalf("buffer line keeps a prefix of a secret that the full-text redaction removes: %q", l)
		}
	}
}

// A failed summary-file write must not leave the previous (possibly trusted)
// index provenance over new text: the index is written first, so the index
// carries the new provenance when the file write fails.
func TestRED_EpisodeIndexWrittenBeforeSummaryFile(t *testing.T) {
	dir := t.TempDir()
	st := NewEpisodeStore(dir, nil)
	if err := st.WriteWithProvenance("20261001-aaaa", "old trusted text", 5, EpisodeProvenance{UserApproved: true}); err != nil {
		t.Fatal(err)
	}
	md := filepath.Join(dir, "20261001-aaaa.md")
	if err := os.Remove(md); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(md, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteWithProvenance("20261001-aaaa", "new untrusted text", 5, EpisodeProvenance{Untrusted: true}); err == nil {
		t.Fatal("expected summary write to fail")
	}
	idx, err := st.ReadIndex()
	if err != nil || len(idx) != 1 || !idx[0].Provenance.Untrusted || idx[0].Provenance.UserApproved {
		t.Fatalf("index must carry the new provenance when the file write fails, got %+v err=%v", idx, err)
	}
}
