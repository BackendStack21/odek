package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// The batch approval card classifies http_request and browser calls by
// method/action only, while the tools themselves classify the target URL:
// an internal destination (cloud metadata, loopback, private ranges) is
// system_write in the tool but plain network_egress on the card. With the
// default policy (network_egress: allow) the call is left off the card, and
// once the user approves a risky sibling the batch trustAll grant
// auto-approves the tool's own system_write prompt — an operation the user
// never saw.
func TestRED_LoopBatchCardClassifiesInternalURLLikeTool(t *testing.T) {
	cases := []struct{ name, args string }{
		{"http_request", `{"url":"http://169.254.169.254/latest/meta-data/iam/security-credentials/"}`},
		{"http_request", `{"url":"http://127.0.0.1:8080/admin","method":"GET"}`},
		{"browser", `{"action":"navigate","url":"http://169.254.169.254/latest/meta-data/"}`},
	}
	for _, c := range cases {
		var u struct {
			URL string `json:"url"`
		}
		_ = jsonUnmarshalRedhunt1012(c.args, &u)
		toolClass := danger.ClassifyURL(u.URL)
		got, _ := classifyToolCall(c.name, c.args)
		if danger.Rank(got) < danger.Rank(toolClass) {
			t.Errorf("classifyToolCall(%s, %s) = %q, but the tool itself gates this call as %q; the batch card under-classifies it", c.name, c.args, got, toolClass)
		}
	}
}

// args_summary.argv0 is advertised as "no argument content": assignment
// values never surface. A command-substitution or backtick assignment value
// containing whitespace is split by shellWords, and its second word is
// reported as the program name in every tool_call_started event.
func TestRED_LoopArgv0LeaksSubstitutionAssignmentValue(t *testing.T) {
	cases := []struct{ cmd, secret string }{
		{`PGPASSWORD=$(printf hunter2-secret) psql -U app`, "hunter2-secret"},
		{"TOKEN=`echo s3cr3t-value` ./deploy", "s3cr3t-value"},
	}
	for _, c := range cases {
		got := argv0(c.cmd)
		if strings.Contains(got, c.secret) {
			t.Errorf("argv0(%q) = %q: a fragment of the assignment value leaks into args_summary", c.cmd, got)
		}
	}
}

// docs/PLANNING.md: "Newly declared checks in a revision cannot be satisfied
// by a tool call in the same parallel batch; they must be run afterward."
// create and revise bump the check epoch; check_replace with a replacement
// does not, so a sibling call in the same batch — one that ran BEFORE the
// replacement check existed — is recorded as its passing evidence.
func TestRED_LoopCheckReplaceSameBatchSatisfiesNewCheck(t *testing.T) {
	planArgs := `{"verb":"create","steps":[{"id":"s1","title":"Fix","checks":[{"id":"old","description":"old check","tool":"shell","arguments":{"command":"make check"}}]}]}`
	replace := `{"verb":"check_replace","step_id":"s1","check_id":"old","justification":"make target removed","replacement":{"id":"fresh","description":"tests pass","tool":"shell","arguments":{"command":"go test ./..."}}}`
	batches := [][]session.ToolCall{
		{acceptanceCall("plan", "plan", planArgs)},
		{
			// Runs first: the plan call is an unknown-effect barrier and
			// waits for it, so this executes before "fresh" is declared.
			acceptanceCall("early", "shell", `{"command":"go test ./..."}`),
			acceptanceCall("rep", "plan", replace),
		},
	}
	srv := redhuntBatchServer(t, batches)
	store := NewPlanStore(12, 4000)
	shell := &contractTool{name: "shell", run: func(string) (string, error) { return "ok", nil }}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{NewPlanTool(store), shell}), 12, "sys", nil, 0)
	e.SetPlanStore(store)
	if _, _, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "go"}}); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Snapshot()
	c := st.Steps[0].Checks[0]
	if c.ID != "fresh" {
		t.Fatalf("fixture: replacement not applied: %+v", st.Steps[0].Checks)
	}
	if c.Status == PlanCheckPassed {
		t.Fatalf("replacement check %q is %q from call %q that ran in the same batch, before the check was declared; want pending", c.ID, c.Status, c.CallID)
	}
}

// The reconciliation ledger records completed mutating shell commands so a
// final reply that denies any action gets the runtime consistency notice.
// bg_start runs the same commands (classified at spawn time with shell
// parity) but never enters the ledger.
func TestRED_LoopBGStartMutationMissingFromLedger(t *testing.T) {
	e := New(nil, tool.NewRegistry(nil), 4, "sys", nil, 0)
	e.recordMutation("bg_start", `{"command":"rm -rf build && make install"}`, `{"job_id":"j1","status":"running"}`)
	got := e.reconcileFinalReply("I did not run or modify anything; no changes were made.")
	if !strings.Contains(got, "consistency notice") {
		t.Fatalf("a mutating bg_start command is missing from the run ledger: reply denying all actions passes without the consistency notice: %q", got)
	}
}

func jsonUnmarshalRedhunt1012(s string, v any) error { return json.Unmarshal([]byte(s), v) }

// redhunt1012Approver approves the batch card and records every per-tool
// prompt that auto-passed only because a batch trustAll grant was active —
// i.e. an operation the user approved without ever seeing it.
type redhunt1012Approver struct {
	mu         sync.Mutex
	trustAll   bool
	cards      []string
	autoPassed []string
}

func (a *redhunt1012Approver) PromptCommand(cls danger.RiskClass, cmd, _ string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cls == danger.ToolBatchClass {
		a.cards = append(a.cards, cmd)
		return nil
	}
	if a.trustAll && danger.TrustShortcutAllowed(cls) {
		a.autoPassed = append(a.autoPassed, string(cls)+" "+cmd)
		return nil
	}
	return fmt.Errorf("denied")
}

func (a *redhunt1012Approver) PromptOperation(op danger.ToolOperation) error {
	return a.PromptCommand(op.Risk, op.Resource, op.Name)
}

func (a *redhunt1012Approver) SetTrustAll(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.trustAll = on
}

// The batch card classifies diff by its "path" argument only, but diff reads
// path_a/path_b whenever both are set (they take precedence). A decoy "path"
// makes the call look like a harmless workspace read: it is left off the
// card yet counts as classified, so approving a risky sibling grants batch
// trustAll — and diff's own system_write prompt for the real target
// (/etc/master.passwd, ~/.ssh/id_rsa, …) auto-passes unseen.
func TestRED_LoopBatchCardDiffDecoyPathBypassesApproval(t *testing.T) {
	secret := "/etc/master.passwd"
	if danger.ClassifyPath(secret) != danger.SystemWrite {
		secret = "/etc/shadow"
	}
	diffArgs := `{"path":"go.mod","path_a":"` + secret + `","path_b":"go.mod"}`
	if cls, _ := classifyToolCall("diff", diffArgs); danger.Rank(cls) < danger.Rank(danger.ClassifyPath(secret)) {
		t.Errorf("classifyToolCall(diff, %s) = %q; diff reads %s (%q)", diffArgs, cls, secret, danger.ClassifyPath(secret))
	}

	approver := &redhunt1012Approver{}
	cfg := &danger.DangerousConfig{Approver: approver}
	diff := &contractTool{name: "diff", run: func(args string) (string, error) {
		var p struct {
			PathA string `json:"path_a"`
			PathB string `json:"path_b"`
		}
		_ = json.Unmarshal([]byte(args), &p)
		for _, path := range []string{p.PathA, p.PathB} {
			if err := cfg.CheckOperation(danger.ToolOperation{Name: "diff", Resource: path, Risk: danger.ClassifyPath(path)}, nil); err != nil {
				return "", err
			}
		}
		return "read " + p.PathA, nil
	}}
	shell := &contractTool{name: "shell", run: func(string) (string, error) { return "installed", nil }}
	srv := redhuntBatchServer(t, [][]session.ToolCall{{
		acceptanceCall("a", "shell", `{"command":"pip install requests"}`),
		acceptanceCall("b", "diff", diffArgs),
	}})
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{shell, diff}), 4, "sys", nil, 0)
	e.SetApprover(approver)
	e.SetDangerousConfig(cfg)
	if _, _, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "go"}}); err != nil {
		t.Fatal(err)
	}
	if len(approver.cards) != 1 {
		t.Fatalf("fixture: expected one batch card, got %d", len(approver.cards))
	}
	if strings.Contains(approver.cards[0], secret) {
		return
	}
	if len(approver.autoPassed) > 0 {
		t.Fatalf("batch card did not list %s:\n%s\nyet the batch trustAll grant auto-approved: %v", secret, approver.cards[0], approver.autoPassed)
	}
}

func TestRED_LoopBatchCardPathAndURLSiblings(t *testing.T) {
	if cls, _ := classifyToolCall("diff", `{"path_a":"go.mod","path_b":"/etc/shadow"}`); danger.Rank(cls) < danger.Rank(danger.ClassifyPath("/etc/shadow")) {
		t.Errorf("diff path_b not classified: %q", cls)
	}
	if cls, res := classifyToolCall("diff", `{"path":"go.mod","content":"x"}`); cls == "" || res != "go.mod" {
		t.Errorf("diff path+content = %q %q", cls, res)
	}
	if cls, _ := classifyToolCall("diff", `{"path_a":"go.mod"}`); cls != "" {
		t.Errorf("diff with half a pair must stay unclassifiable, got %q", cls)
	}
	if cls, _ := classifyToolCall("browser", `{"action":"click"}`); cls != "" {
		t.Errorf("browser click must withhold batch trust, got %q", cls)
	}
	if cls, _ := classifyToolCall("http_request", `{"url":"https://example.com"}`); cls != danger.NetworkEgress {
		t.Errorf("public GET = %q", cls)
	}
}
