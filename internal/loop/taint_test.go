package loop

import (
	"context"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

type catalogueTool struct {
	name       string
	thirdParty bool
}

func (c catalogueTool) Name() string                { return c.name }
func (c catalogueTool) Description() string         { return "d" }
func (c catalogueTool) Schema() any                 { return map[string]any{"type": "object"} }
func (c catalogueTool) Call(string) (string, error) { return "", nil }
func (c catalogueTool) ThirdPartyCatalogue() bool   { return c.thirdParty }

func TestMarkCatalogueTaint(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tools []tool.Tool
		want  bool
	}{
		{"no tools", nil, false},
		{"first-party only", []tool.Tool{catalogueTool{name: "a"}}, false},
		{"third-party registered", []tool.Tool{catalogueTool{name: "a"}, catalogueTool{name: "mcp", thirdParty: true}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New(nil, tool.NewRegistry(tc.tools), 1, "", nil, 0)
			ctx := withRunIngestTaint(context.Background(), []session.Message{{Role: "user", Content: "hi"}})
			e.markCatalogueTaint(ctx)
			if got := UntrustedIngested(ctx); got != tc.want {
				t.Fatalf("UntrustedIngested = %v, want %v", got, tc.want)
			}
		})
	}
	// An untracked context has no state to mark; it stays untracked so
	// delegate_tasks fails closed on the missing tracker instead.
	e := New(nil, tool.NewRegistry([]tool.Tool{catalogueTool{name: "mcp", thirdParty: true}}), 1, "", nil, 0)
	ctx := context.Background()
	e.markCatalogueTaint(ctx)
	if IngestProvenanceTracked(ctx) {
		t.Fatal("markCatalogueTaint must not install a tracker")
	}
}
