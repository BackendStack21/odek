package mcpclient

import "testing"

// Every MCP tool's name, description and schema come from the server, so the
// adapter always reports third-party catalogue metadata.
func TestToolAdapterReportsThirdPartyCatalogue(t *testing.T) {
	if !(&ToolAdapter{}).ThirdPartyCatalogue() {
		t.Fatal("ToolAdapter must report third-party catalogue metadata")
	}
}
