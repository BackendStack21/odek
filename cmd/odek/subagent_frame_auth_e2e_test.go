package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/config"
)

// TestE2E_SubagentResultFrameAuthenticated runs the real binary as a child:
// it must read the nonce from the private descriptor and stamp its framed
// result with it, so the parent accepts the reported usage.
func TestE2E_SubagentResultFrameAuthenticated(t *testing.T) {
	skipIfNoE2E(t)
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}],"usage":{"prompt_tokens":50,"completion_tokens":10}}`)
	}))
	defer llmSrv.Close()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("DEEPSEEK_API_KEY", "sk-mock-frame")
	t.Setenv("ODEK_BASE_URL", llmSrv.URL)
	os.Unsetenv("OPENAI_API_KEY")
	writeMockProviderFixture(t, homeDir, llmSrv.URL)
	t.Chdir(t.TempDir())

	o := &recordingBudgetOwner{grant: budget.Grant{ID: 1, Limits: budget.Limits{MaxInputTokens: 100000, MaxOutputTokens: 50000, MaxToolCalls: 8}}}
	tool := &delegateTasksTool{odekPath: e2eBinary, timeout: 60 * time.Second, maxConcurrency: 1, budgetInherit: config.BudgetInheritShare, budgetView: o}
	out := tool.runTask(0, "frame-e2e", "say done", "", "", "", "", "", "")
	if o.usage == nil {
		t.Fatalf("real child's result frame was not authenticated (usage unrecorded); result: %s", out)
	}
	if o.usage.TotalInput() == 0 {
		t.Fatalf("authenticated usage is empty: %+v; result: %s", o.usage, out)
	}
}
