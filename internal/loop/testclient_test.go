package loop

import (
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llmclient"
)

// testChatClient binds an OpenAI-format ChatClient to an httptest URL.
func testChatClient(t *testing.T, baseURL string) *llmclient.Client {
	t.Helper()
	s, err := llmclient.NewSDK(llmclient.Options{
		Provider: "test",
		Model:    "test-model",
		APIKey:   "sk-test",
		BaseURL:  baseURL,
		Providers: map[string]llmclient.ProviderOverride{
			"test": {APIKey: "sk-test", BaseURL: baseURL, Format: "openai"},
		},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewSDK: %v", err)
	}
	c, err := llmclient.New(s, "test", "test-model")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	return c
}
