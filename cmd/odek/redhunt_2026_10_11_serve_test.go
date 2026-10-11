package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	odek "github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/bgproc"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/mcpclient"
	"github.com/BackendStack21/odek/internal/resource"
)

// serveFixNewRun builds a REST run wired to a wsApprover exactly like
// startServeRun does: typed approvalRequest values go through
// recordApprovalRequest, map frames through record.
func serveFixNewRun() (*serveRun, *wsApprover) {
	run := &serveRun{ID: "run-redhunt", Status: "running", pending: map[string]*approvalRequest{}}
	run.cond = sync.NewCond(&run.mu)
	approver := newWSApprover(func(v any) error {
		if ar, ok := v.(approvalRequest); ok {
			run.recordApprovalRequest(ar)
			return nil
		}
		return run.record(v)
	})
	run.approver = approver
	return run, approver
}

// An approval that times out on a headless REST run must leave the run's
// pending list and stop reporting waiting_approval: the loop has already
// moved on (the tool was denied by timeout) and answering the stale id
// returns 404.
func TestRED_RunApprovalExpiryLeavesStaleWaitingApproval(t *testing.T) {
	run, approver := serveFixNewRun()
	approver.SetApprovalTimeout(50 * time.Millisecond)

	err := approver.PromptCommand(danger.SystemWrite, "touch /etc/x", "")
	if err == nil {
		t.Fatal("PromptCommand returned nil; want an approval timeout error")
	}

	snap := run.snapshot(false)
	pending, _ := snap["pending_approvals"].([]map[string]any)
	if snap["status"] != "running" || len(pending) != 0 {
		t.Fatalf("after approval expiry: status=%v pending=%d; want status=running and no pending approvals (the approval is gone: answering it returns 404)", snap["status"], len(pending))
	}
}

// The per-session wake rate limit must count each wake once. dispatch
// filters the timestamp window in place into the same backing array but
// does not store the filtered slice back, so an expired entry is replaced
// by a duplicate of a live one and the next successful wake keeps it.
func TestRED_WakeRateLimitDoubleCountsAfterWindowSlides(t *testing.T) {
	router := newFakeWakeRouter(wakeIdle)
	d := newTestDispatcher(router, time.Millisecond, 3)
	defer d.Stop()

	now := time.Now()
	d.mu.Lock()
	// One wake outside the trailing hour, one inside it.
	d.wakes["sess-1"] = []time.Time{now.Add(-61 * time.Minute), now.Add(-30 * time.Minute)}
	d.pending["sess-1"] = 1
	d.mu.Unlock()

	d.dispatch("sess-1")
	if n := router.postCount(); n != 1 {
		t.Fatalf("first dispatch posted %d wakes, want 1", n)
	}

	d.mu.Lock()
	got := len(d.wakes["sess-1"])
	d.mu.Unlock()
	// Real wakes inside the trailing hour: the -30m one and the one just sent.
	if got != 2 {
		t.Fatalf("recorded wakes in the trailing hour = %d, want 2 (a live timestamp was duplicated)", got)
	}

	// With max 3/h and only 2 real wakes in the window, the next wake must go out.
	d.mu.Lock()
	d.pending["sess-1"] = 1
	d.mu.Unlock()
	d.dispatch("sess-1")
	if n := router.postCount(); n != 2 {
		t.Fatalf("second dispatch: posts=%d, want 2 (only 2 of 3 hourly wakes were used)", n)
	}
}

// newServeAgent must stop the MCP subprocesses it already started when a
// later construction step fails. With sandbox enabled and Docker
// unavailable, setupSandbox fails AFTER loadMCPTools spawned the servers,
// and the error return drops mcpCleanup — every WebUI (re)connect leaks one
// set of MCP server processes.
func TestRED_ServeAgentSandboxFailureLeaksMCPServers(t *testing.T) {
	fake := fakeMCPPath(t) // build before PATH is emptied
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "mcp.pid")
	script := filepath.Join(dir, "mcp.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho $$ > "+pidFile+"\nexec "+fake+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", t.TempDir())
	resolved := loadJSONMockResolved()
	resolved.Sandbox = true
	resolved.SandboxImage = "alpine:latest" // skip the Dockerfile approval path
	resolved.MCPServers = map[string]mcpclient.ServerConfig{
		"leaky": {Command: script, AutoApprove: true},
	}
	// No docker binary reachable: the sandbox step fails deterministically.
	t.Setenv("PATH", t.TempDir())

	agent, _, _, _, _, _, _, err := newServeAgent(resolved, "sys", "run-leak", func(any) error { return nil }, &wsDeltaCounters{}, false, nil)
	if err == nil {
		agent.Close()
		t.Skip("sandbox setup unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("newServeAgent error = %v, want a sandbox failure", err)
	}

	raw, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("MCP server never started (no pidfile): %v", rerr)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if pid <= 0 {
		t.Fatalf("bad pid %q", raw)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return // process gone: cleaned up
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("MCP server pid %d still running 3s after newServeAgent returned %q: subprocess leaked", pid, err)
}

// GET /api/subagents?session_id= resolves live run keys by reading every
// registered run's SessionID. serveRun.SessionID is written under run.mu by
// record() ("session" frame) and by the run goroutine at turn end, but
// subagentRunKeysForSession reads it holding only serveRuns.mu: a data race
// between a live headless run and any subagent-registry poll. Run with -race.
func TestRED_SubagentRunKeysRaceOnRunSessionID(t *testing.T) {
	resetServeRuns()
	t.Cleanup(resetServeRuns)
	run := &serveRun{ID: "run-race-redhunt", Status: "running", pending: map[string]*approvalRequest{}}
	run.cond = sync.NewCond(&run.mu)
	registerRun(run)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = run.record(map[string]any{"type": "session", "session_id": "sess-" + strconv.Itoa(i)})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = subagentRunKeysForSession("sess-" + strconv.Itoa(i))
		}
	}()
	wg.Wait()
}

// Graceful shutdown must make in-flight headless runs finish so their
// cleanup (agent.Close, sandbox docker rm -f, MCP teardown) runs before the
// process exits. serveOnListener closes WebSockets (which cancels WS
// prompts) but never cancels REST runs: a run blocked on an approval (up to
// 10 minutes) or a long LLM call outlives the 10s drain and its deferred
// cleanup never runs.
func TestRED_ShutdownDoesNotCancelHeadlessRuns(t *testing.T) {
	resetServeRuns()
	t.Cleanup(resetServeRuns)
	// requestServeShutdown is a process-wide once; re-arm it for later tests.
	t.Cleanup(func() {
		serveShutdownOnce = sync.Once{}
		serveShutdownCh = make(chan struct{})
	})

	run, approver := serveFixNewRun()
	approver.SetApprovalTimeout(10 * time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	run.cancel = cancel
	run.resourcesActive = true
	registerRun(run)

	cleanedUp := make(chan struct{})
	serveRunsWG.Add(1)
	go func() { // mirrors startServeRun's run goroutine blocked in an approval
		defer serveRunsWG.Done()
		defer close(cleanedUp) // stands in for run.releaseResources()
		_ = approver.PromptCommand(danger.SystemWrite, "touch /etc/x", "")
		_ = ctx
	}()
	// Wait until the approval is actually pending.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && run.snapshot(false)["status"] != "waiting_approval" {
		time.Sleep(5 * time.Millisecond)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = serveOnListener(ln, http.NewServeMux())
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	requestServeShutdown()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("serveOnListener did not return")
	}
	select {
	case <-cleanedUp:
	default:
		approver.Cancel() // release the goroutine so the test process is clean
		t.Fatal("serveOnListener returned while a headless run was still blocked: shutdown never cancels REST runs, so their cleanup (sandbox/MCP teardown) never runs")
	}
}

// Once shutdown has swept the headless runs, a POST /api/prompt still in
// flight must not register a new run behind the sweep: it would never be
// cancelled and its sandbox/MCP resources would be cut off at process exit.
// startServeRun refuses, and nothing stays registered or tracked.
func TestRED_ShutdownRefusesRunsStartedAfterSweep(t *testing.T) {
	resetServeRuns()
	t.Cleanup(resetServeRuns)
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer llm.Close()
	defer setTestEnv(t, llm.URL)()

	cancelActiveServeRuns() // shutdown sweep

	run, err := startServeRun(loadJSONMockResolved(), "Help", newTestSessionStore(t), nil, promptRequest{Content: "hello"})
	if err == nil {
		t.Fatalf("startServeRun admitted run %s after the shutdown sweep; it is never cancelled", run.ID)
	}
	if n := len(listRunsWire(0)); n != 0 {
		t.Fatalf("%d run(s) registered after the shutdown sweep", n)
	}
	if !drainServeWork(2 * time.Second) {
		t.Fatal("a refused run is still tracked by the shutdown drain")
	}
}

// The admission check runs under the registry lock at registration time,
// so a run whose agent construction outlasted the sweep is refused too.
func TestRED_ShutdownAdmissionRefusesLateRegistration(t *testing.T) {
	resetServeRuns()
	t.Cleanup(resetServeRuns)
	run := &serveRun{ID: "run-late", Status: "running", pending: map[string]*approvalRequest{}}
	run.cond = sync.NewCond(&run.mu)
	if err := admitServeRun(run); err != nil {
		t.Fatalf("admission before shutdown refused: %v", err)
	}
	serveRunsWG.Done() // balance the admission's tracking
	cancelActiveServeRuns()
	late := &serveRun{ID: "run-late-2", Status: "running", pending: map[string]*approvalRequest{}}
	late.cond = sync.NewCond(&late.mu)
	if err := admitServeRun(late); err == nil {
		t.Fatal("run admitted after the shutdown sweep")
	}
	if n := len(listRunsWire(0)); n != 1 {
		t.Fatalf("registry holds %d runs, want only the pre-shutdown one", n)
	}
}

// A new serve session's title is shorten(prompt, 60) — truncated BEFORE the
// store's save-time redaction. A key that straddles the 60-byte cut keeps
// fewer characters than the redaction patterns require, so a fragment of
// the secret is persisted as the session title and served by
// GET /api/sessions. (The REST run Task does it in the safe order:
// shorten(redact.RedactSecrets(content), 160).)
func TestRED_NewSessionTitleLeaksTruncatedSecret(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer llm.Close()
	store := newTestSessionStore(t)
	a, err := odek.New(odek.Config{Provider: "deepseek", BaseURL: llm.URL, APIKey: "test-key", Model: "test-model", SystemMessage: "Help", NoProjectFile: true, MemoryDir: t.TempDir(), MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	const keyBody = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789wxyz" // 40 chars
	prompt := "Please deploy the staging cluster using sk-proj-" + keyBody + " today"
	msg := wsClientMsg{Type: "prompt", Content: prompt}
	sess := handlePrompt(context.Background(), func(map[string]any) {}, store, resource.NewRegistry(), loadJSONMockResolved(), a, nil, nil, msg, new(int), new(int), nil, nil, nil, nil)
	if sess == nil {
		t.Fatal("handlePrompt created no session")
	}
	saved, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(saved.Task, keyBody[:8]) {
		t.Fatalf("persisted session title %q contains a fragment of the API key", saved.Task)
	}
}

// `"sandbox": false` in trusted config (and ODEK_NO_SANDBOX=1) is a
// documented serve opt-out (docs/CONFIG.md "Opt out explicitly with
// --no-sandbox, ODEK_NO_SANDBOX=1, or "sandbox": false in trusted config").
// serveCmd only looks at the --no-sandbox flag pointer and !resolved.Sandbox,
// so the explicit config opt-out is overridden and serve runs sandboxed
// (hard-failing every connection when Docker is absent).
func TestRED_ServeIgnoresConfigSandboxOptOut(t *testing.T) {
	t.Cleanup(func() {
		serveShutdownOnce = sync.Once{}
		serveShutdownCh = make(chan struct{})
		resetServeRuns() // shutdown closed run admission for this process
	})
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".odek"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".odek", "config.json"), []byte(`{"sandbox": false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if pre := config.LoadConfig(config.CLIFlags{}); pre.Sandbox || !pre.SandboxExplicit {
		t.Fatalf("precondition: config loader should resolve an explicit sandbox=false, got Sandbox=%v Explicit=%v", pre.Sandbox, pre.SandboxExplicit)
	}

	origStderr := os.Stderr
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = pw
	urlCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 1024)
		sent := false
		for {
			n, rerr := pr.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if !sent {
				if i := strings.Index(string(buf), "http://127.0.0.1:"); i >= 0 {
					rest := string(buf[i:])
					if j := strings.IndexByte(rest, '\n'); j >= 0 {
						urlCh <- strings.TrimSpace(rest[:j])
						sent = true
					}
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- serveCmd([]string{"--addr", "127.0.0.1:0"}) }()

	var tokenURL string
	select {
	case tokenURL = <-urlCh:
	case err := <-done:
		os.Stderr = origStderr
		t.Fatalf("serveCmd returned early: %v", err)
	case <-time.After(15 * time.Second):
		os.Stderr = origStderr
		t.Fatal("serve did not print its URL")
	}
	defer func() {
		requestServeShutdown()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
		}
		os.Stderr = origStderr
		pw.Close()
	}()

	i := strings.Index(tokenURL, "/?token=")
	base, token := tokenURL[:i], tokenURL[i+len("/?token="):]
	req, _ := http.NewRequest(http.MethodGet, base+"/api/health", nil)
	req.Header.Set("X-Odek-Ws-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var health map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health["sandbox"] != false {
		t.Fatalf(`/api/health sandbox = %v with "sandbox": false in ~/.odek/config.json; the documented explicit opt-out was overridden`, health["sandbox"])
	}
}

// serveSandboxPolicy applies the same opt-out rules as `run`: an explicit
// trusted setting wins, ODEK_NO_SANDBOX=1 opts out when nothing is set, the
// default is on, and ODEK_REQUIRE_SANDBOX=1 turns any opt-out into an error.
func TestRED_ServeSandboxPolicyMatchesRun(t *testing.T) {
	cases := []struct {
		name         string
		resolved     config.ResolvedConfig
		noSandbox    string
		require      string
		want, forced bool
		wantErr      bool
	}{
		{name: "default on", resolved: config.ResolvedConfig{}, want: true, forced: true},
		{name: "explicit on", resolved: config.ResolvedConfig{Sandbox: true, SandboxExplicit: true}, want: true},
		{name: "explicit off", resolved: config.ResolvedConfig{SandboxExplicit: true}, want: false},
		{name: "env opt-out", resolved: config.ResolvedConfig{}, noSandbox: "1", want: false},
		{name: "explicit on beats env opt-out", resolved: config.ResolvedConfig{Sandbox: true, SandboxExplicit: true}, noSandbox: "1", want: true},
		{name: "require beats explicit off", resolved: config.ResolvedConfig{SandboxExplicit: true}, require: "1", wantErr: true},
		{name: "require beats env opt-out", resolved: config.ResolvedConfig{}, noSandbox: "1", require: "1", wantErr: true},
		{name: "require with default", resolved: config.ResolvedConfig{}, require: "1", want: true, forced: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ODEK_NO_SANDBOX", tc.noSandbox)
			t.Setenv("ODEK_REQUIRE_SANDBOX", tc.require)
			got, forced, err := serveSandboxPolicy(tc.resolved)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("serveSandboxPolicy = (%v, %v, nil), want an error", got, forced)
				}
				return
			}
			if err != nil || got != tc.want || forced != tc.forced {
				t.Fatalf("serveSandboxPolicy = (%v, %v, %v), want (%v, %v, nil)", got, forced, err, tc.want, tc.forced)
			}
		})
	}
}

// After a REST run is cancelled, a late approval request from the draining
// loop (the case recordApprovalRequest explicitly handles by refusing to
// list it) must fail fast. Instead wsApprover.Cancel re-armed a fresh
// cancel generation, the request is hidden from GET .../approvals, and
// PromptCommand blocks for the full approval timeout (up to 10 minutes),
// keeping the run goroutine, its sandbox/MCP resources and an active-run
// slot alive with nothing able to answer or cancel it.
func TestRED_CancelledRunLateApprovalBlocksUntilTimeout(t *testing.T) {
	run, approver := serveFixNewRun()
	approver.SetApprovalTimeout(3 * time.Second)
	_, cancel := context.WithCancel(context.Background())
	run.cancel = cancel

	cancelRun(run)
	if st := run.snapshot(false)["status"]; st != "cancelled" {
		t.Fatalf("precondition: status = %v, want cancelled", st)
	}

	start := time.Now()
	err := approver.PromptCommand(danger.SystemWrite, "touch /etc/x", "")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("late approval on a cancelled run was approved")
	}
	pending, _ := run.snapshot(false)["pending_approvals"].([]map[string]any)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("late approval on a cancelled run blocked %v (pending visible to operator: %d) — it waited out the approval timeout instead of failing fast", elapsed.Round(time.Millisecond), len(pending))
	}
}

// A closed approver denies every later prompt at once — trusted classes
// included — and stays safe to Cancel/Close again. A pending prompt is
// released by Close.
func TestRED_ClosedApproverDeniesLatePrompts(t *testing.T) {
	approver := newWSApprover(func(any) error { return nil })
	approver.SetApprovalTimeout(5 * time.Second)

	released := make(chan error, 1)
	go func() { released <- approver.PromptCommand(danger.SystemWrite, "touch /etc/x", "") }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		approver.mu.Lock()
		n := len(approver.pending)
		approver.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	approver.Close()
	select {
	case err := <-released:
		if err == nil {
			t.Fatal("pending prompt was approved by Close")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not release the pending prompt")
	}

	approver.Cancel() // must not panic on the already-closed generation
	approver.Close()
	approver.SetTrustAll(true)
	approver.mu.Lock()
	approver.approveAll[danger.LocalWrite] = true
	approver.mu.Unlock()
	if err := approver.PromptCommand(danger.LocalWrite, "touch x", ""); err == nil {
		t.Fatal("trusted class approved on a closed approver")
	}
	if _, err := approver.PromptClarify("which?"); err == nil {
		t.Fatal("clarify answered on a closed approver")
	}
}

// GET /api/jobs must redact secrets in the command head exactly like the
// bg_job WebSocket frame does (bgJobFrame runs redact.RedactSecrets on the
// same headString clamp). The REST listing returns the raw head.
func TestRED_JobsListLeaksSecretInCommandHead(t *testing.T) {
	env := newJobsEnv(t)
	const secret = "sk-proj-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"
	id := env.startJob(t, env.sessA.ID, "echo "+secret)
	waitForExit(t, env.mgr, env.sessA.ID, id)

	// Parity reference: the WS frame for the same job is redacted.
	frame := bgJobFrame(bgproc.Notice{JobID: id, SessionID: env.sessA.ID, Command: "echo " + secret}, true)
	if strings.Contains(frame["command_head"].(string), secret) {
		t.Fatalf("precondition: bg_job frame is expected to redact the secret")
	}

	resp := env.do(t, http.MethodGet, "/api/jobs?session_id="+env.sessA.ID, env.sessA.AuthToken, nil, "")
	body := decodeBody(t, resp)
	jobs, _ := body["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %v, want 1 entry", body)
	}
	cmd, _ := jobs[0].(map[string]any)["command"].(string)
	if strings.Contains(cmd, secret) {
		t.Fatalf("GET /api/jobs command = %q: secret not redacted (bg_job frame shows %q)", cmd, frame["command_head"])
	}
}

// A key that straddles the bgCommandHead clamp must not survive as a
// fragment: the client command head redacts before it truncates.
func TestRED_JobCommandHeadRedactsBeforeClamp(t *testing.T) {
	const keyBody = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789wxyz"
	prefix := strings.Repeat("x", bgCommandHead-20)
	cmd := "echo " + prefix + " sk-proj-" + keyBody
	for name, head := range map[string]string{
		"helper": bgClientCommandHead(cmd),
		"frame":  bgJobFrame(bgproc.Notice{JobID: "j1", SessionID: "s1", Command: cmd}, true)["command_head"].(string),
	} {
		if strings.Contains(head, keyBody[:6]) {
			t.Fatalf("%s command head %q keeps a fragment of the key", name, head)
		}
	}
}
