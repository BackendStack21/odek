package memory

import (
	"testing"

	"github.com/BackendStack21/odek/internal/llm"
)

func toolMsg(name string) llm.Message {
	tc := llm.ToolCall{}
	tc.Function.Name = name
	return llm.Message{
		Role:      "assistant",
		ToolCalls: []llm.ToolCall{tc},
	}
}

func TestDeriveProvenance_Empty(t *testing.T) {
	prov := DeriveProvenance(nil)
	if prov.Untrusted {
		t.Errorf("empty input should be trusted, got %+v", prov)
	}
}

func TestDeriveProvenance_PureShellIsTrusted(t *testing.T) {
	prov := DeriveProvenance([]llm.Message{toolMsg("shell"), toolMsg("patch")})
	if prov.Untrusted {
		t.Errorf("shell+patch is internal, should be trusted, got %+v", prov)
	}
}

func TestDeriveProvenance_BrowserTaints(t *testing.T) {
	prov := DeriveProvenance([]llm.Message{toolMsg("shell"), toolMsg("browser")})
	if !prov.Untrusted {
		t.Fatalf("browser should taint, got %+v", prov)
	}
	if len(prov.Sources) != 1 || prov.Sources[0] != "browser" {
		t.Errorf("Sources = %v, want [browser]", prov.Sources)
	}
}

func TestDeriveProvenance_MCPAdapterTaints(t *testing.T) {
	prov := DeriveProvenance([]llm.Message{toolMsg("github__list_issues")})
	if !prov.Untrusted {
		t.Fatalf("MCP tool should taint, got %+v", prov)
	}
}

func TestDeriveProvenance_ReadFileTaints(t *testing.T) {
	prov := DeriveProvenance([]llm.Message{toolMsg("read_file")})
	if !prov.Untrusted {
		t.Errorf("read_file should taint (conservative — file may be outside CWD), got %+v", prov)
	}
}

// TestEpisode_TaintedNotReplayed is the headline test: an episode
// written with Untrusted=true is kept on disk but NEVER returned by
// Search. This is what stops a one-shot injection from becoming a
// persistent backdoor.
func TestEpisode_TaintedNotReplayed(t *testing.T) {
	es := NewEpisodeStore(t.TempDir(), nil)

	if err := es.WriteWithProvenance("20260101-clean", "clean session summary", 5,
		EpisodeProvenance{}); err != nil {
		t.Fatalf("write clean: %v", err)
	}
	if err := es.WriteWithProvenance("20260102-tainted", "tainted session summary", 5,
		EpisodeProvenance{Untrusted: true, Sources: []string{"browser"}}); err != nil {
		t.Fatalf("write tainted: %v", err)
	}

	results, err := es.Search("any query", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, ep := range results {
		if ep.SessionID == "20260102-tainted" {
			t.Errorf("tainted episode was returned by Search — backdoor vector still open")
		}
	}
	// Sanity: the clean episode is present.
	var sawClean bool
	for _, ep := range results {
		if ep.SessionID == "20260101-clean" {
			sawClean = true
			break
		}
	}
	if !sawClean {
		t.Errorf("clean episode should be returned by Search, got %v", results)
	}
}

// TestEpisode_UserApprovedTainted_IsReplayed verifies the promotion
// escape hatch: a tainted episode the user has explicitly promoted IS
// auto-replayed.
func TestEpisode_UserApprovedTainted_IsReplayed(t *testing.T) {
	es := NewEpisodeStore(t.TempDir(), nil)
	if err := es.WriteWithProvenance("20260103-approved", "promoted summary", 5,
		EpisodeProvenance{Untrusted: true, UserApproved: true, Sources: []string{"browser"}}); err != nil {
		t.Fatalf("write: %v", err)
	}

	results, err := es.Search("any", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 || results[0].SessionID != "20260103-approved" {
		t.Errorf("UserApproved tainted episode should be returned, got %v", results)
	}
}

// TestEpisode_ProvenanceRoundtripsThroughIndex confirms the index
// persists the provenance fields (so a fresh process load preserves
// the trust signal).
func TestEpisode_ProvenanceRoundtripsThroughIndex(t *testing.T) {
	dir := t.TempDir()
	es1 := NewEpisodeStore(dir, nil)
	if err := es1.WriteWithProvenance("20260104-x", "summary", 5,
		EpisodeProvenance{Untrusted: true, Sources: []string{"browser", "read_file"}}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Fresh store, fresh cache — must reload from disk.
	es2 := NewEpisodeStore(dir, nil)
	idx, err := es2.ReadIndex()
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(idx) != 1 {
		t.Fatalf("index entries = %d, want 1", len(idx))
	}
	if !idx[0].Provenance.Untrusted {
		t.Errorf("Provenance.Untrusted lost on roundtrip: %+v", idx[0].Provenance)
	}
	if len(idx[0].Provenance.Sources) != 2 {
		t.Errorf("Provenance.Sources lost on roundtrip: %v", idx[0].Provenance.Sources)
	}
}
