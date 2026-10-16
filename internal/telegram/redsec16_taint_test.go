package telegram

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/session"
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

// A chat's recorded untrusted spans are persisted by the next save and come
// back with the chat, so a rebuilt per-turn engine is seeded with them.
func TestRED_MarkTaintSpansPersists(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sm := NewSessionManager(store, time.Hour)
	const chat = int64(4343)
	cs, err := sm.GetOrCreate(chat)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.TaintSpans()) != 0 {
		t.Fatal("fresh chat has spans")
	}
	sm.MarkTaintSpans(chat, []session.TaintSpan{{Kind: "host", Value: "evil.example", Source: "web"}})
	sm.MarkTaintSpans(chat, nil)                                                     // no-op
	sm.MarkTaintSpans(9998, []session.TaintSpan{{Kind: "host", Value: "x.example"}}) // uncached: no-op
	if err := sm.Save(chat, []session.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(cs.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.TaintSpans) != 1 || loaded.TaintSpans[0].Value != "evil.example" {
		t.Fatalf("persisted spans = %+v", loaded.TaintSpans)
	}
	sm2 := NewSessionManager(store, time.Hour)
	again, err := sm2.GetOrCreate(chat)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.TaintSpans(); len(got) != 1 {
		t.Fatalf("reloaded chat spans = %+v", got)
	}
	var nilCS *ChatSession
	if nilCS.TaintSpans() != nil {
		t.Fatal("nil chat has spans")
	}
}
