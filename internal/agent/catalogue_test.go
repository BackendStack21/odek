package agent

import "testing"

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
