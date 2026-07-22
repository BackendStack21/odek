package skills

import (
	"testing"
)

func msgWithTool(name string) LlmMessage {
	tc := LlmToolCall{}
	tc.Function.Name = name
	return LlmMessage{
		Role:      "assistant",
		ToolCalls: []LlmToolCall{tc},
	}
}

func msgWithToolArgs(name, argsJSON string) LlmMessage {
	tc := LlmToolCall{}
	tc.Function.Name = name
	tc.Function.Arguments = argsJSON
	return LlmMessage{
		Role:      "assistant",
		ToolCalls: []LlmToolCall{tc},
	}
}

// Mirrors the memory fix: a skill learned from a session that only read
// workspace files must NOT be flagged for review. This also guards the
// llm.Message → LlmMessage conversion: if Arguments were dropped upstream,
// the path tool would read as empty-args and conservatively taint, failing
// this test.
func TestDeriveProvenance_WorkspaceReadTrusted(t *testing.T) {
	msgs := []LlmMessage{
		msgWithTool("shell"),
		msgWithToolArgs("read_file", `{"path":"internal/x.go"}`),
		msgWithToolArgs("search_files", `{"pattern":"TODO"}`),
	}
	prov := DeriveProvenance(msgs)
	if prov.Untrusted || prov.NeedsReview {
		t.Errorf("workspace-only session should yield a trusted skill, got %+v", prov)
	}
}

func TestDeriveProvenance_SensitiveReadTaints(t *testing.T) {
	msgs := []LlmMessage{msgWithToolArgs("read_file", `{"path":"/etc/passwd"}`)}
	prov := DeriveProvenance(msgs)
	if !prov.Untrusted || !prov.NeedsReview {
		t.Fatalf("reading /etc/passwd should taint the skill, got %+v", prov)
	}
	if len(prov.Sources) != 1 || prov.Sources[0] != "read_file" {
		t.Errorf("Sources = %v, want [read_file]", prov.Sources)
	}
}

func TestDeriveProvenance_EmptyIsTrusted(t *testing.T) {
	prov := DeriveProvenance(nil)
	if prov.Untrusted {
		t.Errorf("empty session should be trusted, got %+v", prov)
	}
}

func TestDeriveProvenance_PureShellIsTrusted(t *testing.T) {
	msgs := []LlmMessage{msgWithTool("shell"), msgWithTool("patch")}
	prov := DeriveProvenance(msgs)
	if prov.Untrusted {
		t.Errorf("shell+patch session should be trusted, got %+v", prov)
	}
}

func TestDeriveProvenance_BrowserTaints(t *testing.T) {
	msgs := []LlmMessage{msgWithTool("shell"), msgWithTool("browser"), msgWithTool("patch")}
	prov := DeriveProvenance(msgs)
	if !prov.Untrusted {
		t.Fatalf("browser call should taint provenance, got %+v", prov)
	}
	if !prov.NeedsReview {
		t.Errorf("NeedsReview should be true when untrusted, got %+v", prov)
	}
	if len(prov.Sources) != 1 || prov.Sources[0] != "browser" {
		t.Errorf("Sources should list 'browser', got %v", prov.Sources)
	}
}

func TestDeriveProvenance_MCPAdapterTaints(t *testing.T) {
	// MCP tools follow the "<server>__<tool>" naming convention.
	msgs := []LlmMessage{msgWithTool("github__list_issues")}
	prov := DeriveProvenance(msgs)
	if !prov.Untrusted {
		t.Fatalf("MCP tool should taint provenance, got %+v", prov)
	}
	if len(prov.Sources) != 1 || prov.Sources[0] != "github__list_issues" {
		t.Errorf("Sources should list the MCP tool name, got %v", prov.Sources)
	}
}

// Web search, vision, and sub-agent results all cross the trust boundary,
// so a session using them must produce a tainted skill.
func TestDeriveProvenance_ExternalResultToolsTaint(t *testing.T) {
	for _, name := range []string{"web_search", "vision", "delegate_tasks"} {
		prov := DeriveProvenance([]LlmMessage{msgWithTool(name)})
		if !prov.Untrusted || !prov.NeedsReview {
			t.Errorf("%s should taint provenance (Untrusted+NeedsReview), got %+v", name, prov)
		}
		if len(prov.Sources) != 1 || prov.Sources[0] != name {
			t.Errorf("Sources should list %q, got %v", name, prov.Sources)
		}
	}
}

func TestDeriveProvenance_MultipleSourcesDeduped(t *testing.T) {
	msgs := []LlmMessage{
		msgWithTool("browser"),
		msgWithTool("browser"),
		msgWithTool("read_file"),
		msgWithTool("read_file"),
	}
	prov := DeriveProvenance(msgs)
	if !prov.Untrusted {
		t.Fatalf("expected Untrusted=true, got %+v", prov)
	}
	if len(prov.Sources) != 2 {
		t.Errorf("expected 2 deduped sources, got %v", prov.Sources)
	}
}

func TestSaveSuggestion_PropagatesProvenance(t *testing.T) {
	dir := t.TempDir()
	s := SkillSuggestion{
		Name:        "test-skill",
		Description: "verifying provenance flows through SaveSuggestion",
		Body:        "## Overview\nbody\n\n## Common Pitfalls\nnone",
		Provenance:  SkillProvenance{Untrusted: true, Sources: []string{"browser"}},
	}
	if err := SaveSuggestion(dir, s); err != nil {
		t.Fatalf("SaveSuggestion: %v", err)
	}

	loaded := scanDir(dir)
	if len(loaded) != 1 {
		t.Fatalf("scanDir found %d skills, want 1", len(loaded))
	}
	got := loaded[0].Provenance
	if !got.Untrusted {
		t.Errorf("saved skill missing Untrusted=true, got %+v", got)
	}
	if !got.NeedsReview {
		t.Errorf("Untrusted skill should be saved with NeedsReview=true, got %+v", got)
	}
}
