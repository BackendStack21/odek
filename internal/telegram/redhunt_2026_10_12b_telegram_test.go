package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The plain-text fallback (SendWithPlainFallback → StripMarkdown) drops every
// unescaped '_', '*', '~' and '`'. Inside code spans and pre blocks
// FormatResponse leaves those characters unescaped (MarkdownV2 only escapes
// '`' and '\' there), so the fallback deletes them from code: identifiers,
// globs and arithmetic shown to the user are silently corrupted.
func TestRED_StripMarkdownKeepsCodeContent(t *testing.T) {
	answer := "Set `max_retry_count` to 3, then run:\n```\nls *_test.go && echo ~/x\n```"
	chunks, err := FormatResponse(answer)
	if err != nil || len(chunks) != 1 {
		t.Fatalf("FormatResponse: %v (%d chunks)", err, len(chunks))
	}
	plain := StripMarkdown(chunks[0])
	for _, want := range []string{"max_retry_count", "ls *_test.go && echo ~/x"} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain fallback lost code content %q:\n%s", want, plain)
		}
	}
}

// Long-line entity splitting (close the open entities at a cut, reopen them in
// the next chunk) is applied only on the plain-paragraph path. A paragraph
// that also holds a fenced block goes through splitMarkdownParagraph, whose
// prose pieces are cut by splitPlainSegment without closing the bold/italic
// entity, so Telegram rejects the chunk ("can't parse entities").
func TestRED_LongProseNextToFenceKeepsEntitiesBalanced(t *testing.T) {
	header := "## " + strings.TrimSpace(strings.Repeat("alpha ", 900)) // ~5.4 KB bold header
	answer := header + "\n```\ncode\n```"
	chunks, err := FormatResponse(answer)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected the oversized paragraph to be split, got %d chunk(s)", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 4096 {
			t.Errorf("chunk %d exceeds 4096 bytes (%d)", i, len(c))
		}
		if open := openEntities(c); len(open) != 0 {
			t.Errorf("chunk %d leaves entities %q open (Telegram rejects it): head=%q tail=%q",
				i, open, c[:min(40, len(c))], c[max(0, len(c)-40):])
		}
	}
}

// Outbound media rejects credential files only by a short $HOME-centric list,
// while the rest of odek (danger.IsSensitiveReadPath / credentialFileBase)
// treats private keys, PEM/key files, Terraform state, Vault tokens and
// service-account JSON as credentials wherever they live. Inside the bot's
// working directory (or the temp dir) these are accepted for upload.
func TestRED_MediaPathRejectsCredentialShapedFiles(t *testing.T) {
	dir := t.TempDir() // inside os.TempDir(), an allowed media base
	names := []string{
		"id_ed25519",
		"deploy.pem",
		"server.key",
		"terraform.tfstate",
		".vault-token",
		"service-account.json",
		"credentials.json",
		".envrc",
	}
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("secret"), 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := ResolveMediaPathForChat(p, 42); err == nil {
			t.Errorf("credential file %q accepted for outbound upload (resolved %s)", n, got)
		}
	}
}

// MarkdownV2 bold is a SINGLE asterisk pair ("*bold*"); unlike CommonMark there
// is no "**" form ("__" is underline). FormatResponse leaves model "**bold**"
// unchanged, which Telegram parses as an empty bold entity, the plain word,
// and another empty bold entity: every bold span in an answer renders as plain
// text. The conversion must emit "*bold*".
func TestRED_DoubleStarBoldConvertedToMarkdownV2Bold(t *testing.T) {
	chunks, err := FormatResponse("This is **important** today")
	if err != nil || len(chunks) != 1 {
		t.Fatalf("FormatResponse: %v (%d chunks)", err, len(chunks))
	}
	if got := chunks[0]; !strings.Contains(got, "*important*") || strings.Contains(got, "**") {
		t.Errorf("bold not converted to MarkdownV2 single-asterisk form: %q", got)
	}
}

// SetFallbackURLs swaps the bot's 60 s long-poll client for the fallback
// transport's client, whose timeout is 30 s — the same as the default
// poll_timeout (30 s), and below any allowed poll_timeout up to 60 s. An idle
// getUpdates long-poll (Telegram holds it for poll_timeout seconds) is then
// cut by the client timeout on every cycle: the request is retried as a
// "timeout" network error, then counted as a poll error, and the poller's
// exponential backoff grows to 60 x interval, delaying every incoming message.
func TestRED_FallbackClientTimeoutCoversLongPoll(t *testing.T) {
	bot := NewBot("testtoken")
	if err := bot.SetFallbackURLs([]string{"http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	poll := time.Duration(DefaultConfig().PollTimeout) * time.Second
	const margin = 10 * time.Second
	if bot.Client.Timeout != 0 && bot.Client.Timeout < poll+margin {
		t.Errorf("fallback client timeout %s does not cover the default %s long-poll (+%s margin): idle polls time out",
			bot.Client.Timeout, poll, margin)
	}
}

// doUpload reads the whole file into memory before sending, with no size
// bound, although the Bot API refuses uploads over 50 MB. An approved upload
// of a large file (a log, an image dump) is buffered in full — twice the file
// size with the multipart copy — and posted, instead of being refused up front.
func TestRED_UploadRefusesFilesOverBotAPILimit(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
	}))
	t.Cleanup(srv.Close)
	bot := NewBot("testtoken")
	bot.BaseURL = srv.URL

	p := filepath.Join(t.TempDir(), "big.bin")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(51 << 20); err != nil { // sparse: no disk cost
		t.Fatal(err)
	}
	f.Close()

	_, err = bot.SendDocument(1, p, "", nil)
	if err == nil {
		t.Errorf("a 51 MiB upload was buffered and posted (server hits=%d); want a size error before reading", hits.Load())
	}
	if hits.Load() != 0 {
		t.Errorf("oversized file was sent to the API (%d request(s))", hits.Load())
	}
}
