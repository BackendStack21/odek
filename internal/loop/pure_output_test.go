package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// pureBoundaryTool is a first-party-style tool with pure output, registered
// below; its output still needs the tool-output boundary.
type pureBoundaryTool struct{ externalBoundaryTool }

func (*pureBoundaryTool) Name() string                { return "pure" }
func (*pureBoundaryTool) Call(string) (string, error) { return "42", nil }
func (*pureBoundaryTool) PureOutputFor(string) bool   { return true }

func init() { tool.RegisterPureOutputType((*pureBoundaryTool)(nil)) }

// runBoundaryTool runs one turn that calls name once and returns the tool
// message the provider saw, whether an ingest was recorded and the engine.
func runBoundaryTool(t *testing.T, tl tool.Tool, name string) (string, bool, *Engine) {
	t.Helper()
	var calls int
	var second []session.Message
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			fmt.Fprintf(w, `{"choices":[{"message":{"tool_calls":[{"id":"c1","type":"function","function":{"name":%q,"arguments":"{}"}}]}}]}`, name)
			return
		}
		var body struct {
			Messages []session.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		second = body.Messages
		fmt.Fprint(w, budgetFinalResponse("done", 1, 1))
	}))
	defer server.Close()
	engine := New(testChatClient(t, server.URL), tool.NewRegistry([]tool.Tool{tl}), 4, "runtime", nil, 0)
	var recorded bool
	ctx := WithIngestRecorder(context.Background(), func(string, string) { recorded = true })
	if _, _, err := engine.RunWithMessages(ctx, []session.Message{{Role: "user", Content: "use tool"}}); err != nil {
		t.Fatal(err)
	}
	for _, m := range second {
		if m.Role == "tool" {
			return m.Content, recorded, engine
		}
	}
	t.Fatal("no tool message reached the provider")
	return "", false, nil
}

func TestPureToolOutput_KeepsBoundaryWithoutTaint(t *testing.T) {
	content, recorded, engine := runBoundaryTool(t, &pureBoundaryTool{}, "pure")
	if srcs := session.WrapperSources(content); len(srcs) != 1 || srcs[0] != session.PureToolSourcePrefix+"pure" {
		t.Fatalf("pure output wrapper sources = %q in %q", srcs, content)
	}
	if recorded {
		t.Fatal("pure output recorded an ingest")
	}
	if engine.UntrustedIngested() {
		t.Fatal("pure output tainted the run")
	}
}

// An unregistered tool claiming purity is external.
type claimsPureBoundaryTool struct{ externalBoundaryTool }

func (*claimsPureBoundaryTool) PureOutputFor(string) bool { return true }

func TestUnregisteredPureClaim_StillTaints(t *testing.T) {
	content, recorded, engine := runBoundaryTool(t, &claimsPureBoundaryTool{}, "external")
	if !session.ContentCarriesUntrusted(content) || strings.Contains(content, session.PureToolSourcePrefix) {
		t.Fatalf("unregistered pure claim not wrapped as external: %q", content)
	}
	if !recorded || !engine.UntrustedIngested() {
		t.Fatal("unregistered pure claim did not taint the run")
	}
}

// panickingPureTool is registered (test-only) but its PureOutputFor panics:
// the run must survive and the output must taint as external.
type panickingPureTool struct{ externalBoundaryTool }

func (*panickingPureTool) PureOutputFor(string) bool { panic("boom") }

func init() { tool.RegisterPureOutputType((*panickingPureTool)(nil)) }

func TestPanickingPureClaim_TaintsWithoutCrashing(t *testing.T) {
	content, recorded, engine := runBoundaryTool(t, &panickingPureTool{}, "external")
	if !session.ContentCarriesUntrusted(content) {
		t.Fatalf("panicking pure claim not wrapped as external: %q", content)
	}
	if !recorded || !engine.UntrustedIngested() {
		t.Fatal("panicking pure claim did not taint the run")
	}
}

func TestPlanToolOutputIsPure(t *testing.T) {
	if !tool.OutputIsPure(&PlanTool{}, `{"verb":"get"}`) {
		t.Fatal("plan tool output is not pure")
	}
}
