package main

// Tests for the serve (Web UI) background-jobs REST surface:
//
//	GET  /api/jobs                      — list the authenticated session's jobs
//	GET  /api/jobs/{id}/output          — paginated output (since/next_cursor)
//	POST /api/jobs/{id}/stop            — kill a job within the session
//
// Every request goes through newServeMux so the EXACT production middleware
// chain applies: per-instance CSRF token, loopback Host check, local-origin
// check on mutations, and per-session auth tokens. Jobs are seeded directly
// through the shared bgproc manager (the same object the bg_* tools use),
// keyed by store session ids.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/bgproc"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/resource"
	"github.com/BackendStack21/odek/internal/session"
)

// jobsEnv is the full production mux plus one shared bgproc manager and two
// sessions (A and B) with independently minted auth tokens.
type jobsEnv struct {
	srv   *httptest.Server
	token string // per-instance CSRF token
	store *session.Store
	mgr   *bgproc.Manager
	sessA *session.Session
	sessB *session.Session
}

func newJobsEnv(t *testing.T) *jobsEnv {
	t.Helper()

	store := newTestSessionStore(t)
	serveEvents.reset()
	t.Cleanup(serveEvents.reset)
	// Same observer wiring as newServeBGManager: lifecycle events land in
	// the /api/events ring with hashes only.
	mgr := bgproc.NewManager(bgproc.Config{MaxJobsPerSession: 8}, &bgEventObserver{emit: func(ev events.Event) {
		serveEvents.add(ev)
	}})

	resolved := config.LoadConfig(config.CLIFlags{})
	if resolved.System == "" {
		resolved.System = defaultSystem
	}
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	resourceReg := resource.NewRegistry(
		resource.NewFileResolver(cwd),
		resource.NewSessionResolver(filepath.Join(home, ".odek", "sessions")),
	)
	wsToken, err := newServeToken()
	if err != nil {
		t.Fatalf("CSRF token: %v", err)
	}

	mux := newServeMux(serveMuxDeps{
		Store:         store,
		Resources:     resourceReg,
		Resolved:      resolved,
		SystemMessage: resolved.System,
		State:         &serveState{startedAt: time.Now(), resolved: resolved},
		WsToken:       wsToken,
		MemoryDir:     filepath.Join(t.TempDir(), "memory"),
		BGManager:     mgr,
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	sessA, err := store.Create([]llm.Message{{Role: "user", Content: "A"}}, "m", "jobs-a")
	if err != nil {
		t.Fatal(err)
	}
	sessB, err := store.Create([]llm.Message{{Role: "user", Content: "B"}}, "m", "jobs-b")
	if err != nil {
		t.Fatal(err)
	}

	return &jobsEnv{srv: srv, token: wsToken, store: store, mgr: mgr, sessA: sessA, sessB: sessB}
}

// do issues a request against the production mux with the instance CSRF
// token always attached and an optional session token / Origin header.
func (e *jobsEnv) do(t *testing.T, method, path, sessionToken string, body io.Reader, origin string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	req.Header.Set("X-Odek-Ws-Token", e.token)
	if sessionToken != "" {
		req.Header.Set("X-Session-Token", sessionToken)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func decodeBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response (status %d): %v", resp.StatusCode, err)
	}
	return out
}

// waitForExit polls the manager until the job leaves running state.
func waitForExit(t *testing.T, mgr *bgproc.Manager, sessionID, jobID string) bgproc.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := mgr.Get(sessionID, jobID); ok && j.Status != bgproc.StatusRunning {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not exit in time", jobID)
	return bgproc.Job{}
}

// startJob seeds a background job into the shared manager exactly as the
// bg_start tool would (same manager, same session key).
func (e *jobsEnv) startJob(t *testing.T, sessionID, command string) string {
	t.Helper()
	j, err := e.mgr.Start(sessionID, command, "", 0)
	if err != nil {
		t.Fatalf("mgr.Start(%q): %v", command, err)
	}
	return j.ID
}

// ── GET /api/jobs — session-scoped listing ───────────────────────────────

func TestAPIJobs_List_IsSessionScopedAndShaped(t *testing.T) {
	env := newJobsEnv(t)

	exited := env.startJob(t, env.sessA.ID, `printf '0123456789ABCDEF'`)
	waitForExit(t, env.mgr, env.sessA.ID, exited)
	running := env.startJob(t, env.sessA.ID, "sleep 30")
	t.Cleanup(func() { env.mgr.StopAll(env.sessA.ID) })
	env.startJob(t, env.sessB.ID, "sleep 30")
	t.Cleanup(func() { env.mgr.StopAll(env.sessB.ID) })

	resp := env.do(t, http.MethodGet, "/api/jobs?session_id="+env.sessA.ID, env.sessA.AuthToken, nil, "")
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	jobs, ok := body["jobs"].([]any)
	if !ok {
		t.Fatalf("body missing jobs array: %v", body)
	}
	if len(jobs) != 2 {
		t.Fatalf("len(jobs) = %d, want 2 (session A jobs only)", len(jobs))
	}
	byID := map[string]map[string]any{}
	for _, raw := range jobs {
		j, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("job entry not an object: %v", raw)
		}
		id, _ := j["id"].(string)
		byID[id] = j
		// Command head, status, runtime are mandatory fields.
		if cmd, _ := j["command"].(string); cmd == "" {
			t.Errorf("job %s: missing command head", id)
		}
		if _, ok := j["status"].(string); !ok {
			t.Errorf("job %s: missing status", id)
		}
		if _, ok := j["runtime_s"].(float64); !ok {
			t.Errorf("job %s: missing runtime_s", id)
		}
	}
	if _, ok := byID[exited]; !ok {
		t.Fatalf("exited job %s missing from list", exited)
	}
	if _, ok := byID[running]; !ok {
		t.Fatalf("running job %s missing from list", running)
	}
	// exit_code only appears on finished jobs.
	if _, ok := byID[exited]["exit_code"]; !ok {
		t.Errorf("exited job %s: exit_code missing", exited)
	}
	if _, ok := byID[running]["exit_code"]; ok {
		t.Errorf("running job %s: exit_code must be absent", running)
	}
	if got := byID[exited]["status"]; got != string(bgproc.StatusExited) {
		t.Errorf("exited job status = %v, want exited", got)
	}
}

func TestAPIJobs_List_UnknownOrForeignSessionRejected(t *testing.T) {
	env := newJobsEnv(t)

	// Missing session_id.
	resp := env.do(t, http.MethodGet, "/api/jobs", env.sessA.AuthToken, nil, "")
	if resp.StatusCode != http.StatusBadRequest {
		resp.Body.Close()
		t.Fatalf("missing session_id: status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Unknown session.
	resp = env.do(t, http.MethodGet, "/api/jobs?session_id=does-not-exist", "whatever", nil, "")
	if resp.StatusCode != http.StatusNotFound {
		resp.Body.Close()
		t.Fatalf("unknown session: status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// Foreign session token → 401, never the job list.
	env.startJob(t, env.sessA.ID, "sleep 30")
	t.Cleanup(func() { env.mgr.StopAll(env.sessA.ID) })
	resp = env.do(t, http.MethodGet, "/api/jobs?session_id="+env.sessA.ID, env.sessB.AuthToken, nil, "")
	if resp.StatusCode != http.StatusUnauthorized {
		resp.Body.Close()
		t.Fatalf("foreign token: status = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
}

// ── GET /api/jobs/{id}/output — pagination ───────────────────────────────

func TestAPIJobs_Output_PaginatesWithSinceCursor(t *testing.T) {
	env := newJobsEnv(t)

	// Exactly 32 deterministic bytes.
	const want = "0123456789ABCDEF0123456789ABCDEF"
	jobID := env.startJob(t, env.sessA.ID, fmt.Sprintf("printf '%s'", want))
	waitForExit(t, env.mgr, env.sessA.ID, jobID)

	var collected strings.Builder
	since := int64(0)
	for i := 0; ; i++ {
		resp := env.do(t, http.MethodGet,
			fmt.Sprintf("/api/jobs/%s/output?session_id=%s&since=%d&limit=16", jobID, env.sessA.ID, since),
			env.sessA.AuthToken, nil, "")
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("page %d: status = %d, want 200", i, resp.StatusCode)
		}
		body := decodeBody(t, resp)
		resp.Body.Close()
		if got, _ := body["job_id"].(string); got != jobID {
			t.Errorf("page %d: job_id = %v, want %s", i, got, jobID)
		}
		chunk, _ := body["output"].(string)
		collected.WriteString(chunk)
		next, ok := body["next_cursor"].(float64)
		if !ok {
			t.Fatalf("page %d: next_cursor missing: %v", i, body)
		}
		since = int64(next)
		if since == 0 {
			if chunk != "" {
				t.Errorf("page %d: next_cursor 0 but chunk non-empty — final page must be empty", i)
			}
			break
		}
		if i > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if collected.String() != want {
		t.Fatalf("reassembled output = %q, want %q", collected.String(), want)
	}
}

func TestAPIJobs_Output_UnknownJobShape(t *testing.T) {
	env := newJobsEnv(t)

	resp := env.do(t, http.MethodGet,
		"/api/jobs/bg_doesnotexist/output?session_id="+env.sessA.ID,
		env.sessA.AuthToken, nil, "")
	if resp.StatusCode != http.StatusNotFound {
		resp.Body.Close()
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["status"] != "unknown" {
		t.Errorf("body status = %v, want %q", body["status"], "unknown")
	}
}

// ── POST /api/jobs/{id}/stop — within-session stop ───────────────────────

func TestAPIJobs_Stop_KillsJobWithinSession(t *testing.T) {
	env := newJobsEnv(t)

	jobID := env.startJob(t, env.sessA.ID, "sleep 30")

	resp := env.do(t, http.MethodPost,
		"/api/jobs/"+jobID+"/stop?session_id="+env.sessA.ID,
		env.sessA.AuthToken, nil, "")
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["job_id"] != jobID {
		t.Errorf("job_id = %v, want %s", body["job_id"], jobID)
	}
	if body["status"] != string(bgproc.StatusKilled) {
		t.Errorf("status = %v, want %q", body["status"], bgproc.StatusKilled)
	}
	final := waitForExit(t, env.mgr, env.sessA.ID, jobID)
	if final.Status != bgproc.StatusKilled {
		t.Errorf("manager job status = %s, want killed", final.Status)
	}
}

func TestAPIJobs_Stop_MethodGuard(t *testing.T) {
	env := newJobsEnv(t)
	jobID := env.startJob(t, env.sessA.ID, "sleep 30")
	t.Cleanup(func() { env.mgr.StopAll(env.sessA.ID) })

	resp := env.do(t, http.MethodGet,
		"/api/jobs/"+jobID+"/stop?session_id="+env.sessA.ID,
		env.sessA.AuthToken, nil, "")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		resp.Body.Close()
		t.Fatalf("GET on stop: status = %d, want 405", resp.StatusCode)
	}
	resp.Body.Close()
}

// ── middleware: instance CSRF token + local-origin + session isolation ───

func TestAPIJobs_Stop_WithoutInstanceTokenForbidden(t *testing.T) {
	env := newJobsEnv(t)
	jobID := env.startJob(t, env.sessA.ID, "sleep 30")
	t.Cleanup(func() { env.mgr.StopAll(env.sessA.ID) })

	req, err := http.NewRequest(http.MethodPost, env.srv.URL+"/api/jobs/"+jobID+"/stop?session_id="+env.sessA.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Session-Token", env.sessA.AuthToken)
	resp, err := env.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stop without CSRF token: status = %d, want 403", resp.StatusCode)
	}
}

func TestAPIJobs_Stop_NonLocalOriginForbidden(t *testing.T) {
	env := newJobsEnv(t)
	jobID := env.startJob(t, env.sessA.ID, "sleep 30")
	t.Cleanup(func() { env.mgr.StopAll(env.sessA.ID) })

	// Valid CSRF token, but the mutation comes from a non-local Origin —
	// the same class of request requireLocalOrigin rejects on /api/cancel.
	resp := env.do(t, http.MethodPost,
		"/api/jobs/"+jobID+"/stop?session_id="+env.sessA.ID,
		env.sessA.AuthToken, nil, "https://evil.example")
	if resp.StatusCode != http.StatusForbidden {
		resp.Body.Close()
		t.Fatalf("stop with foreign Origin: status = %d, want 403", resp.StatusCode)
	}
}

func TestAPIJobs_CrossSession_InvisibleAndUnstoppable(t *testing.T) {
	env := newJobsEnv(t)

	jobID := env.startJob(t, env.sessA.ID, "sleep 30")

	// B's token cannot list A's jobs…
	resp := env.do(t, http.MethodGet, "/api/jobs?session_id="+env.sessA.ID, env.sessB.AuthToken, nil, "")
	if resp.StatusCode != http.StatusUnauthorized {
		resp.Body.Close()
		t.Fatalf("list with B token on A: status = %d, want 401", resp.StatusCode)
	}

	// …cannot read A's job output…
	resp = env.do(t, http.MethodGet,
		"/api/jobs/"+jobID+"/output?session_id="+env.sessA.ID,
		env.sessB.AuthToken, nil, "")
	if resp.StatusCode != http.StatusUnauthorized {
		resp.Body.Close()
		t.Fatalf("output with B token on A's job: status = %d, want 401", resp.StatusCode)
	}

	// …cannot stop it via A's session id…
	resp = env.do(t, http.MethodPost,
		"/api/jobs/"+jobID+"/stop?session_id="+env.sessA.ID,
		env.sessB.AuthToken, nil, "")
	if resp.StatusCode != http.StatusUnauthorized {
		resp.Body.Close()
		t.Fatalf("stop with B token on A: status = %d, want 401", resp.StatusCode)
	}

	// …and addressing A's job under B's own session yields the same
	// "unknown" answer as a stale id (no existence oracle).
	resp = env.do(t, http.MethodPost,
		"/api/jobs/"+jobID+"/stop?session_id="+env.sessB.ID,
		env.sessB.AuthToken, nil, "")
	if resp.StatusCode != http.StatusNotFound {
		resp.Body.Close()
		t.Fatalf("stop A's job under B's session: status = %d, want 404", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["status"] != "unknown" {
		t.Errorf("body status = %v, want %q", body["status"], "unknown")
	}

	// The job must still be alive — the foreign attempts touched nothing.
	if j, ok := env.mgr.Get(env.sessA.ID, jobID); !ok || j.Status != bgproc.StatusRunning {
		t.Fatalf("job after foreign attempts: ok=%v status=%s, want alive", ok, j.Status)
	}
	env.mgr.StopAll(env.sessA.ID)
}

// ── events: job lifecycle carries hashes only, never raw commands ────────

func TestAPIJobs_Events_CarryHashesNotRawCommands(t *testing.T) {
	env := newJobsEnv(t)

	const secret = "super-secret-command-token-xyz"
	jobID := env.startJob(t, env.sessA.ID, "echo "+secret)
	waitForExit(t, env.mgr, env.sessA.ID, jobID)

	evs := serveEvents.snapshot(serveEventsCap, "", env.sessA.ID)
	var sawStarted, sawExited bool
	for _, ev := range evs {
		switch ev.Type {
		case "bg_started":
			sawStarted = true
			b, _ := json.Marshal(ev.Data)
			s := string(b)
			if strings.Contains(s, secret) {
				t.Errorf("bg_started leaks raw command: %s", s)
			}
			hash, _ := ev.Data["command_sha256"].(string)
			if len(hash) != 64 {
				t.Errorf("bg_started command_sha256 = %v, want 64-hex", ev.Data["command_sha256"])
			}
			if ev.Data["job_id"] != jobID {
				t.Errorf("bg_started job_id = %v, want %s", ev.Data["job_id"], jobID)
			}
			if _, ok := ev.Data["timeout_s"]; !ok {
				t.Error("bg_started missing timeout_s")
			}
		case "bg_exited":
			sawExited = true
			b, _ := json.Marshal(ev.Data)
			if strings.Contains(string(b), secret) {
				t.Errorf("bg_exited leaks raw command: %s", b)
			}
			for _, field := range []string{"job_id", "status", "exit_code", "duration_ms", "output_bytes"} {
				if _, ok := ev.Data[field]; !ok {
					t.Errorf("bg_exited missing %s", field)
				}
			}
		}
	}
	if !sawStarted || !sawExited {
		t.Fatalf("expected bg_started=%v bg_exited=%v in %d events", sawStarted, sawExited, len(evs))
	}
}

// ── disabled manager: endpoints degrade to empty/unknown, never panic ────

func TestAPIJobs_DisabledManager_YieldsEmptyList(t *testing.T) {
	env := newJobsEnv(t)
	// Simulate the disabled state by removing the manager from the mux deps:
	// build a second mux without BGManager.
	resolved := config.LoadConfig(config.CLIFlags{})
	if resolved.System == "" {
		resolved.System = defaultSystem
	}
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	mux := newServeMux(serveMuxDeps{
		Store:         env.store,
		Resources:     resource.NewRegistry(resource.NewFileResolver(cwd), resource.NewSessionResolver(filepath.Join(home, ".odek", "sessions"))),
		Resolved:      resolved,
		SystemMessage: resolved.System,
		State:         &serveState{startedAt: time.Now(), resolved: resolved},
		WsToken:       env.token,
		MemoryDir:     filepath.Join(t.TempDir(), "memory"),
		BGManager:     nil,
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/jobs?session_id="+env.sessA.ID, nil)
	req.Header.Set("X-Odek-Ws-Token", env.token)
	req.Header.Set("X-Session-Token", env.sessA.AuthToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Jobs) != 0 {
		t.Fatalf("jobs = %v, want empty", body.Jobs)
	}
}
