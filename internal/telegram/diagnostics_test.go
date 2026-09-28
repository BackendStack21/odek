package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/diagnostics"
	"github.com/BackendStack21/odek/internal/events"
)

func TestAPIFailureDiagnosticsExcludeTokensAndBodies(t *testing.T) {
	var got []events.Event
	restore := diagnostics.Install(func(ev events.Event) { got = append(got, ev) })
	defer restore()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":403,"description":"PRIVATE response"}`))
	}))
	defer server.Close()
	bot := NewBot("PRIVATE-token")
	bot.BaseURL = server.URL
	if _, err := bot.SendMessageContext(t.Context(), 123, "PRIVATE message", nil); err == nil {
		t.Fatal("API failure swallowed")
	}
	if _, err := bot.SendMessage(123, "PRIVATE message", nil); err == nil {
		t.Fatal("legacy API failure swallowed")
	}
	if len(got) != 2 {
		t.Fatalf("unexpected reports: %+v", got)
	}
	for _, ev := range got {
		if ev.Data["component"] != "telegram" || ev.Data["operation"] != "api_request" || ev.Data["http_status"] != 403 {
			t.Fatal(ev)
		}
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "PRIVATE") {
		t.Fatalf("private payload leaked: %s", b)
	}
}
