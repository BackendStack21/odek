package llmclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeepSeekShortcutRequests(t *testing.T) {
	for _, provider := range []string{"deepseek", "legacy"} {
		t.Run(provider, func(t *testing.T) {
			requests := make(chan string, 3)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				requests <- body.Model
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()
			client, err := Dial(provider, "deepseek-flash", "test-key", server.URL)
			if err != nil {
				t.Fatal(err)
			}
			for _, model := range []string{"deepseek-flash", "deepseek-pro", "deepseek-v4-pro"} {
				client, err = client.RebindModel(model)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := client.SimpleCall(context.Background(), "test", "hello"); err != nil {
					t.Fatal(err)
				}
				want := model
				if provider == "deepseek" && model == "deepseek-pro" {
					want = "deepseek-v4-pro"
				}
				if got := <-requests; got != want {
					t.Fatalf("%s request model = %q, want %q", model, got, want)
				}
			}
		})
	}
}
