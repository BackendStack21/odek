package llmclient

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/BackendStack21/go-llm-sdk"

	"github.com/BackendStack21/odek/internal/diagnostics"
	"github.com/BackendStack21/odek/internal/events"
)

// captureNotices redirects the standard logger and the diagnostics observer for
// one test.
func captureNotices(t *testing.T) (*bytes.Buffer, func() []events.Event) {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	var mu sync.Mutex
	var got []events.Event
	remove := diagnostics.Install(func(ev events.Event) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	})
	t.Cleanup(func() {
		remove()
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return &buf, func() []events.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]events.Event(nil), got...)
	}
}

func TestReportAttempt_RetryIsAnnounced(t *testing.T) {
	buf, evs := captureNotices(t)
	reportAttempt(sdk.AttemptEvent{
		Provider: "mlx-runner", Operation: "stream", Attempt: 2, Status: 500,
		Err:        &sdk.APIError{Provider: "mlx-runner", Status: 500, Message: "Generation failed: GPU error: Impacting Interactivity", Retryable: true},
		RetryDelay: 4 * time.Second,
	})
	notice := buf.String()
	for _, want := range []string{"attempt 2", "HTTP 500", "Generation failed: GPU error: Impacting Interactivity", "retrying in 4s"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice %q lacks %q", notice, want)
		}
	}
	e := evs()
	if len(e) != 1 || e[0].Type != "operation_warning" || e[0].Data["http_status"] != 500 || e[0].Data["operation"] != "request_retry" {
		t.Fatalf("diagnostics = %+v, want one request_retry warning with http_status 500", e)
	}
	for k, v := range e[0].Data {
		if s, ok := v.(string); ok && strings.Contains(s, "GPU error") {
			t.Fatalf("runtime log field %q carries the error text", k)
		}
	}
}

func TestReportAttempt_OnlyRetriesAreAnnounced(t *testing.T) {
	failure := &sdk.APIError{Provider: "p", Status: 503, Retryable: true}
	for name, ev := range map[string]sdk.AttemptEvent{
		"success":           {Operation: "chat", Attempt: 1, Status: 200},
		"final attempt":     {Operation: "chat", Attempt: 8, Status: 503, Err: failure},
		"learn-once retry":  {Operation: "stream", Attempt: 1, Status: 400, Err: &sdk.APIError{Status: 400}},
		"model listing":     {Operation: "models", Attempt: 1, Err: errors.New("connection refused"), RetryDelay: time.Second},
		"cancelled attempt": {Operation: "chat", Attempt: 1, Err: context.Canceled},
	} {
		buf, evs := captureNotices(t)
		reportAttempt(ev)
		if buf.Len() != 0 || len(evs()) != 0 {
			t.Fatalf("%s: announced %q", name, buf.String())
		}
	}
}

func TestAttemptNotice_RedactsSanitizesAndClips(t *testing.T) {
	key := "sk-proj-" + strings.Repeat("A1b2C3d4", 6)
	ev := sdk.AttemptEvent{Attempt: 1, RetryDelay: time.Second,
		Err: errors.New("upstream rejected api_key=" + key + " \x1b[2Jcleared " + strings.Repeat("x", 2*maxNoticeRunes))}
	notice := attemptNotice(ev)
	if strings.Contains(notice, key) {
		t.Fatalf("secret leaked: %q", notice)
	}
	if strings.ContainsRune(notice, '\x1b') {
		t.Fatalf("escape sequence passed through: %q", notice)
	}
	if !strings.Contains(notice, "…") || len([]rune(notice)) > maxNoticeRunes+100 {
		t.Fatalf("not clipped: %d runes", len([]rune(notice)))
	}
}

// Wired through NewSDK: a 503 followed by a success prints one notice.
func TestNewSDK_AnnouncesRetries(t *testing.T) {
	buf, _ := captureNotices(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"model is loading"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{}}`))
	}))
	defer srv.Close()
	s, err := NewSDK(Options{Provider: "gw", Model: "m", Providers: map[string]ProviderOverride{
		"gw": {APIKey: "k", BaseURL: srv.URL, Format: "openai"},
	}})
	if err != nil {
		t.Fatalf("NewSDK: %v", err)
	}
	c, err := New(s, "gw", "m")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := c.SimpleCall(ctx, "system", "hi"); err != nil {
		t.Fatalf("SimpleCall: %v", err)
	}
	if n := strings.Count(buf.String(), "retrying in"); n != 1 || !strings.Contains(buf.String(), "model is loading") {
		t.Fatalf("notices = %q, want one about the 503", buf.String())
	}
}
