package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/session"
)

// Temperature < 0 means "omit from request". The verify-model client must
// inherit that polarity; otherwise it sends an explicit temperature 0 to a
// provider/model configured to reject the field.
func TestRED_VerifyClientInheritsOmitTemperature(t *testing.T) {
	var mu sync.Mutex
	bodies := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		if mn, _ := m["model"].(string); mn != "" {
			bodies[mn] = m
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"verdict\":\"pass\"}"}}]}`))
	}))
	defer srv.Close()
	a, err := New(Config{Model: "main-model", VerifyModel: "verify-model", BaseURL: srv.URL, APIKey: "k",
		Temperature: -1, MaxIterations: 3, NoProjectFile: true, MemoryDir: t.TempDir(),
		Verify: &loop.VerifyConfig{Enabled: true, Mode: "hint"}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, _, err = a.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if _, ok := bodies["main-model"]; !ok {
		t.Fatal("no main request")
	}
	v, ok := bodies["verify-model"]
	if !ok {
		t.Fatal("no verify request")
	}
	if _, has := bodies["main-model"]["temperature"]; has {
		t.Fatal("main sent temperature (test premise broken)")
	}
	if tv, has := v["temperature"]; has {
		t.Fatalf("verify request carries temperature=%v although Config.Temperature<0 (omit)", tv)
	}
}
