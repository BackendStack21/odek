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

func TestSanitizeDocName_NeutralizesChatTag(t *testing.T) {
	for _, in := range []string{"report_chat7_x.pdf", "chat7_x.pdf", "a_chat_chat7_7_x.pdf", "_c_chat7_hat7_.pdf"} {
		got := sanitizeDocName(in, "FILEID123", "documents/f.pdf")
		if regexp.MustCompile(`_chat\d+_`).MatchString("doc_chat5_" + got) {
			// Only the real chat prefix may match.
			if regexp.MustCompile(`_chat\d+_`).FindAllString("doc_chat5_"+got, -1)[0] != "_chat5_" ||
				len(regexp.MustCompile(`_chat\d+_`).FindAllString("doc_chat5_"+got, -1)) != 1 {
				t.Errorf("sanitizeDocName(%q) = %q still carries a chat tag", in, got)
			}
		}
	}
}

// A document named by chat 5's user may embed another chat's tag
// ("_chat7_"). The tag is derived from attacker-controlled metadata, so it
// must not make chat 5's file resolvable by chat 7.
func TestRED_DocNameForgesOtherChatTag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "getFile") {
			fmt.Fprint(w, `{"ok":true,"result":{"file_id":"f","file_path":"documents/file.bin"}}`)
			return
		}
		w.Write([]byte("secret of chat 5"))
	}))
	defer ts.Close()
	bot := testBot(t, ts)

	path, err := DownloadDocument(bot, 5, "FILEID123", "report_chat7_x.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveMediaPathForChat(path, 7); err == nil {
		t.Fatalf("chat 7 may resolve chat 5's file %s because its filename embeds _chat7_", filepath.Base(path))
	}
}
