package main

import (
	"os"
	"testing"
	"time"
)

// TestWebUIBrowserHarness exposes the production mux with a local mock provider
// and isolated session storage for manual browser validation. It is opt-in;
// writing the configured stop file shuts down the fixture and its connections.
func TestWebUIBrowserHarness(t *testing.T) {
	stopFile := os.Getenv("WEBUI_BROWSER_STOP_FILE")
	if stopFile == "" {
		t.Skip("set WEBUI_BROWSER_STOP_FILE for interactive browser validation")
	}
	env := newJourneyEnv(t, false, true)
	t.Logf("ODEK_BROWSER_URL=%s/?token=%s", env.srv.URL, env.token)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(12 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(stopFile); err == nil {
				return
			}
		case <-deadline.C:
			t.Fatal("browser fixture deadline reached")
		}
	}
}
