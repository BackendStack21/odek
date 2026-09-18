package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE2E_SubagentTaskModelReachesProvider verifies the task envelope's model
// survives the real subprocess boundary and is used in the provider request.
// This is intentionally E2E-gated because it builds/runs the odek binary.
func TestE2E_SubagentTaskModelReachesProvider(t *testing.T) {
	skipIfNoE2E(t)
	requestModels := make(chan string, 16)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		requestModels <- body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`))
	}))
	defer provider.Close()

	fixture := t.TempDir()
	configDir := filepath.Join(fixture, ".odek")
	if err := os.Mkdir(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	config := `{"model":"operator-model","memory":{"enabled":false},"limits":{"model_prices":{"task-model":{"input_cost_per_million_usd":2,"output_cost_per_million_usd":4},"operator-model":{"input_cost_per_million_usd":20,"output_cost_per_million_usd":40}}}}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(fixture, "task.json")
	spec := taskFileSpec{Goal: "reply briefly", Provider: "deepseek", Model: "task-model", BaseURL: provider.URL}
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e2eBinary, "subagent", "--task", taskPath, "--quiet")
	cmd.Env = append(os.Environ(), "ODEK_API_KEY=test-key", "ODEK_NO_SANDBOX=1", "HOME="+fixture, "USERPROFILE="+fixture)
	cmd.Dir = fixture
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("subagent failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	select {
	case requestModel := <-requestModels:
		if requestModel != "task-model" {
			t.Fatalf("provider received model %q, want task-model", requestModel)
		}
	default:
		t.Fatal("no provider request received")
	}
	var result subagentResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("invalid result: %v: %s", err, stdout.String())
	}
	if result.Status != "success" {
		t.Fatalf("child failed: %+v", result)
	}
	if math.Abs(result.CostUSD-0.00028) > 1e-10 {
		t.Fatalf("child cost = %g, want 0.00028 from selected model prices; result=%+v", result.CostUSD, result)
	}
}
