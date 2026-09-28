package llmclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/diagnostics"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/session"
)

func TestAuxiliaryFailureDiagnostics(t *testing.T) {
	var got []events.Event
	restore := diagnostics.Install(func(ev events.Event) { got = append(got, ev) })
	defer restore()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"PRIVATE provider body"}}`))
	}))
	defer server.Close()
	client, err := Dial("legacy", "test", "PRIVATE-key", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SimpleCall(t.Context(), "PRIVATE system", "PRIVATE prompt"); err == nil {
		t.Fatal("simple call failure swallowed")
	}
	if _, err := client.SideCall(t.Context(), []session.Message{{Role: "user", Content: "PRIVATE prompt"}}); err == nil {
		t.Fatal("side call failure swallowed")
	}
	if len(got) != 2 {
		t.Fatal(got)
	}
	for i, operation := range []string{"simple_call", "side_call"} {
		if got[i].Data["component"] != "llm" || got[i].Data["operation"] != operation || got[i].Data["error_class"] != "provider_auth" || got[i].Data["http_status"] != 401 {
			t.Fatal(got[i])
		}
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "PRIVATE") {
		t.Fatalf("private data leaked: %s", b)
	}
}
