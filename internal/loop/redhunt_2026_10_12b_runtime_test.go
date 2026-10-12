package loop

import (
	"context"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/mcpclient"
	"github.com/BackendStack21/odek/internal/session"
)

// A shortened MCP tool name must still be recognised as an MCP tool: it is
// classified Unknown by the batch gate and taints episodes.
func TestRED_ShortenedMCPToolNameStillClassifiedAndTainting(t *testing.T) {
	for _, server := range []string{"srv", strings.Repeat("s", 40), strings.Repeat("s", 64)} {
		name := mcpclient.RegisteredToolName(server, strings.Repeat("t", 64))
		if len(name) > 64 {
			t.Fatalf("%q longer than 64", name)
		}
		if class, _ := classifyToolCallCtx(context.Background(), name, "{}"); class != danger.Unknown {
			t.Errorf("%q classified %q, want unknown", name, class)
		}
		if !session.ToolCallTaintsEpisode(name, "{}") {
			t.Errorf("%q does not taint episodes", name)
		}
	}
}
