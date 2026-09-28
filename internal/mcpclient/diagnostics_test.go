package mcpclient

import (
	"path/filepath"
	"testing"

	"github.com/BackendStack21/odek/internal/diagnostics"
	"github.com/BackendStack21/odek/internal/events"
)

func TestStartupFailureDiagnostic(t *testing.T) {
	var got []events.Event
	restore := diagnostics.Install(func(ev events.Event) { got = append(got, ev) })
	defer restore()
	if _, err := New("test", ServerConfig{Command: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("missing executable started")
	}
	if len(got) != 1 || got[0].Data["component"] != "mcp" || got[0].Data["operation"] != "connect" || got[0].Data["error_class"] != "not_found" {
		t.Fatalf("missing MCP startup report: %+v", got)
	}
}
