package telegram

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The saved document path is odek's own: its basename carries no text the
// sender chose — only the chat tag, a file-ID digest and a sanitized
// extension — so it can sit outside the untrusted wrapper.
func TestRED_DownloadDocument_PathCarriesNoSenderText(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "getFile") {
			fmt.Fprint(w, `{"ok":true,"result":{"file_id":"d1","file_path":"documents/file_7.pdf"}}`)
			return
		}
		_, _ = w.Write([]byte("pdf"))
	}))
	defer ts.Close()
	t.Setenv("HOME", t.TempDir())
	shape := regexp.MustCompile(`^doc_chat42_[0-9a-f]{16}(\.[a-z0-9]{1,16})?$`)
	for _, name := range []string{"curl_evil.example_x.pdf", "Quarterly Report.PDF", "noext", "x." + strings.Repeat("A", 40)} {
		p, err := DownloadDocument(testBot(t, ts), 42, "d1-"+name, name)
		if err != nil {
			t.Fatal(err)
		}
		base := filepath.Base(p)
		if !shape.MatchString(base) {
			t.Errorf("sender name %q produced basename %q", name, base)
		}
	}
}
