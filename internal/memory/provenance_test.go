package memory

import (
	"os"
	"path/filepath"
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

// toolMsgArgs builds an assistant message with one tool call carrying the
// given raw JSON arguments string (as recorded on real sessions).
func toolMsgArgs(name, argsJSON string) llm.Message {
	tc := llm.ToolCall{}
	tc.Function.Name = name
	tc.Function.Arguments = argsJSON
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

// ── Path-aware taint (the fix) ────────────────────────────────────────

// A read confined to the workspace (relative path) must NOT taint — this is
// the headline behavior change that makes episodes from normal coding
// sessions recallable again.
func TestDeriveProvenance_ReadFileWorkspaceTrusted(t *testing.T) {
	for _, p := range []string{"internal/x.go", "./README.md", "cmd/odek/main.go"} {
		prov := DeriveProvenance([]llm.Message{
			toolMsg("shell"),
			toolMsgArgs("read_file", `{"path":"`+p+`"}`),
		})
		if prov.Untrusted {
			t.Errorf("read_file %q is in-workspace and must be trusted, got %+v", p, prov)
		}
	}
}

// search_files / multi_grep with no path default to the workspace → trusted.
func TestDeriveProvenance_SearchDefaultPathTrusted(t *testing.T) {
	msgs := []llm.Message{
		toolMsgArgs("search_files", `{"pattern":"TODO","file_glob":"*.go"}`),
		toolMsgArgs("multi_grep", `{"patterns":["a","b"]}`),
	}
	prov := DeriveProvenance(msgs)
	if prov.Untrusted {
		t.Errorf("workspace-default search should be trusted, got %+v", prov)
	}
}

// A read of a sensitive system path still taints — the original concern the
// provenance control exists for.
func TestDeriveProvenance_ReadFileSensitivePathTaints(t *testing.T) {
	prov := DeriveProvenance([]llm.Message{
		toolMsgArgs("read_file", `{"path":"/etc/passwd"}`),
	})
	if !prov.Untrusted {
		t.Fatalf("/etc/passwd read must taint, got %+v", prov)
	}
	if len(prov.Sources) != 1 || prov.Sources[0] != "read_file" {
		t.Errorf("Sources = %v, want [read_file]", prov.Sources)
	}
}

// Home credential dirs (resolved absolutely) still taint.
func TestDeriveProvenance_ReadFileHomeSecretTaints(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	secret := filepath.Join(home, ".ssh", "id_rsa")
	prov := DeriveProvenance([]llm.Message{
		toolMsgArgs("read_file", `{"path":"`+secret+`"}`),
	})
	if !prov.Untrusted {
		t.Errorf("%s read must taint, got %+v", secret, prov)
	}
}

// Malformed / empty argument strings are treated conservatively (taint),
// since we cannot tell what path was touched.
func TestDeriveProvenance_ReadFileMalformedArgsTaints(t *testing.T) {
	for _, args := range []string{"", "not json", "{"} {
		prov := DeriveProvenance([]llm.Message{toolMsgArgs("read_file", args)})
		if !prov.Untrusted {
			t.Errorf("malformed read_file args %q should conservatively taint, got %+v", args, prov)
		}
	}
}

// Network / audio tools always taint regardless of arguments.
func TestDeriveProvenance_AlwaysExternalToolsTaint(t *testing.T) {
	for _, name := range []string{"http_batch", "transcribe", "web_search", "vision", "delegate_tasks"} {
		prov := DeriveProvenance([]llm.Message{toolMsgArgs(name, `{"path":"internal/x.go"}`)})
		if !prov.Untrusted {
			t.Errorf("%s must always taint, got %+v", name, prov)
		}
	}
}

// ToolCallTaints is the shared predicate used by both memory and skills.
func TestToolCallTaints(t *testing.T) {
	cases := []struct {
		name, args string
		want       bool
	}{
		{"shell", `{"command":"ls"}`, false},
		{"write_file", `{"path":"/etc/x"}`, false}, // not a read tool
		{"read_file", `{"path":"internal/x.go"}`, false},
		{"read_file", `{"path":"/etc/shadow"}`, true},
		{"read_file", `{"path":"/private/etc/master.passwd"}`, true}, // macOS /private symlink
		{"read_file", ``, true},
		{"search_files", `{"pattern":"x"}`, false},
		{"read_file", `{"path":"/workspace/foo.go"}`, false}, // sandbox mount is trusted
		// Broadened file-reading tool coverage (was the D-01 bypass):
		{"batch_read", `{"files":[{"path":"/etc/shadow"}]}`, true},
		{"batch_read", `{"files":[{"path":"internal/x.go"}]}`, false},
		{"json_query", `{"path":"/etc/passwd"}`, true},
		{"diff", `{"path_a":"/etc/hosts","path_b":"a.txt"}`, true}, // any path escaping taints
		{"count_lines", `{"files":[{"path":"go.mod"}]}`, false},
		{"session_search", `{"query":"password"}`, true}, // recall of prior transcripts
		{"browser", `{"url":"https://x"}`, true},
		{"web_search", `{"query":"x"}`, true},    // search-engine results
		{"vision", `{"path":"img.png"}`, true},   // model-described images
		{"delegate_tasks", `{"tasks":[]}`, true}, // sub-agent output
		{"github__list_issues", `{}`, true},
	}
	for _, c := range cases {
		if got := ToolCallTaints(c.name, c.args); got != c.want {
			t.Errorf("ToolCallTaints(%q,%q) = %v, want %v", c.name, c.args, got, c.want)
		}
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
