package agent

import (
	"context"
	"testing"

	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/tool"
)

type thirdPartyFakeTool struct{ fakeKodeTool }

func (thirdPartyFakeTool) ThirdPartyCatalogue() bool { return true }

func TestToolAdapterForwardsThirdPartyCatalogue(t *testing.T) {
	if (&toolAdapter{t: &fakeKodeTool{name: "plain"}}).ThirdPartyCatalogue() {
		t.Fatal("first-party tool reported third-party catalogue metadata")
	}
	if !(&toolAdapter{t: &thirdPartyFakeTool{fakeKodeTool{name: "mcp"}}}).ThirdPartyCatalogue() {
		t.Fatal("adapter dropped third-party catalogue provenance")
	}
}

type claimsPureFakeTool struct{ fakeKodeTool }

func (claimsPureFakeTool) PureOutputFor(string) bool { return true }

// The adapter forwards purity only for registered first-party types: an
// embedder tool that claims purity stays external.
func TestToolAdapterForwardsPureOutputForFirstPartyOnly(t *testing.T) {
	if !tool.OutputIsPure(&toolAdapter{t: &loop.PlanTool{}}, `{"verb":"get"}`) {
		t.Fatal("adapter dropped the plan tool's pure-output marker")
	}
	if tool.OutputIsPure(&toolAdapter{t: &fakeKodeTool{name: "plain"}}, "{}") {
		t.Fatal("tool without a marker reported pure output")
	}
	if tool.OutputIsPure(&toolAdapter{t: &claimsPureFakeTool{fakeKodeTool{name: "lookup"}}}, "{}") {
		t.Fatal("embedder tool claiming purity was honoured")
	}
}

func TestWithUntrustedIngestFacade(t *testing.T) {
	ctx := WithUntrustedIngest(context.Background())
	if !loop.UntrustedIngested(ctx) {
		t.Fatal("WithUntrustedIngest did not taint the context")
	}
}
