package loop

import (
	"context"
	"fmt"
	"strings"
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

// Graduated truncation replaces an old tool body with a short marker. When
// that body was wrapped untrusted content, the marker must keep the history
// recognisably tainted: a later run (or a continued session) seeds its taint
// from the history it is handed.
func TestRED_TrimmedUntrustedToolOutputKeepsTaint(t *testing.T) {
	e := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 6000)
	e.SetCompaction(false)
	wrapped := "<untrusted_content_0123abcd source=\"browser\">\n" + strings.Repeat("fetched page ", 2000) + "\n</untrusted_content_0123abcd>"
	msgs := []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "summarise the page"},
		{Role: "assistant", ToolCalls: []session.ToolCall{reviewProbeCall("c0")}},
		{Role: "tool", ToolCallID: "c0", Content: wrapped},
	}
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs, session.Message{Role: "assistant", ToolCalls: []session.ToolCall{reviewProbeCall(id)}}, session.Message{Role: "tool", ToolCallID: id, Content: "ok"})
	}
	got := e.trimContext(context.Background(), msgs, nil)
	trimmed := false
	for _, m := range got {
		if m.ToolCallID == "c0" {
			trimmed = !strings.Contains(m.Content, "fetched page")
		}
	}
	if !trimmed {
		t.Fatalf("setup: wrapped tool output was not truncated: %+v", got)
	}
	ctx := withRunIngestTaint(context.Background(), got)
	if !UntrustedIngested(ctx) {
		t.Fatal("trimming an untrusted tool body erased the run's ingest taint")
	}
	// A trimmed first-party body stays clean.
	clean := []session.Message{{Role: "tool", Content: "[tool output trimmed: 4096 bytes dropped to fit context budget]"}}
	if UntrustedIngested(withRunIngestTaint(context.Background(), clean)) {
		t.Fatal("a trimmed first-party tool body must not taint the run")
	}
}

func TestEngineUntrustedIngestedTracksRun(t *testing.T) {
	e := New(nil, tool.NewRegistry([]tool.Tool{catalogueTool{name: "mcp", thirdParty: true}}), 1, "", nil, 0)
	if e.UntrustedIngested() {
		t.Fatal("engine tainted before any run")
	}
	ctx := withRunIngestTaint(context.Background(), nil)
	e.bindRunTaint(ctx)
	if !e.UntrustedIngested() {
		t.Fatal("catalogue taint not visible on the engine")
	}
	clean := New(nil, tool.NewRegistry(nil), 1, "", nil, 0)
	cctx := withRunIngestTaint(context.Background(), nil)
	clean.bindRunTaint(cctx)
	if clean.UntrustedIngested() {
		t.Fatal("clean run reported tainted")
	}
	// A recorded ingest later in the run shows up too.
	IngestRecorderFrom(cctx)("browser", "x")
	if !clean.UntrustedIngested() {
		t.Fatal("in-run ingest not visible on the engine")
	}
}
