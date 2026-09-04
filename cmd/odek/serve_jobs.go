package main

// Serve (Web UI) integration for background commands.
//
// One process-scoped bgproc.Manager is created at serve startup (serveCmd):
// background jobs must outlive the agents that started them, and agents in
// serve mode are per WebSocket connection (or per headless REST run). The
// manager is reachable from three places:
//
//   - serveMuxDeps.BGManager — constructor-injected into the /api/jobs
//     handlers, so tests can mount the EXACT production routes with their
//     own manager (same pattern as every other /api dependency);
//   - newServeAgent — builds a per-agent *bgRuntime bound to the shared
//     manager and registers the bg_* tools + the engine notice provider;
//   - serveOnListener — shutdown kills any jobs still running (the bgproc
//     v1 contract has no detach mode).
//
// Security posture (mirrors internal/bgproc + bg_tools.go):
//   - events carry the command SHA-256 and size/duration metadata only —
//     never raw commands, never output;
//   - every /api/jobs route sits behind the same apiAuth chain as the rest
//     of the management surface (per-instance CSRF token, loopback Host,
//     local-origin check on mutations) plus per-session auth tokens, so a
//     session can neither see nor stop another session's jobs;
//   - unknown and foreign job ids get the same {"status":"unknown"} answer
//     (no existence oracle).

import (
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BackendStack21/odek/internal/bgproc"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/session"
)

// serveBG holds the process-scoped manager created by serveCmd. Handler
// dependencies are injected explicitly (serveMuxDeps.BGManager); this global
// exists only so newServeAgent (called from per-connection and per-run
// contexts that have no mux deps) and the serveOnListener shutdown path can
// reach the same instance. Nil means background commands are disabled.
var serveBG atomic.Pointer[bgproc.Manager]

// backgroundSettingsFrom maps the resolved `background` config section onto
// the decoupled BackgroundSettings primitives. Single mapping point for the
// whole serve surface. NOTE: config.Notify is a string ("observe"/"off");
// BackgroundSettings.Notify is the boolean "notices are injected" form.
func backgroundSettingsFrom(resolved config.ResolvedConfig) BackgroundSettings {
	b := resolved.Background
	return BackgroundSettings{
		Enabled:           b.Enabled,
		MaxJobs:           b.MaxJobs,
		MaxOutputBytes:    b.MaxOutputBytes,
		MaxTimeoutSeconds: b.MaxTimeoutSeconds,
		Notify:            b.Notify == "observe",
		WakeOnComplete:    b.WakeOnComplete,
		WakeCoalesceMS:    b.WakeCoalesceMS,
		MaxWakesPerHour:   b.MaxWakesPerHour,
	}
}

// newServeBGManager builds the shared manager from resolved config. It
// returns nil when background commands are disabled — or when sandbox mode
// is on: the manager's SandboxWrap bakes in ONE container name, but serve
// creates a fresh container per connection, so bg spawns cannot be routed
// through the agent's container today. Rather than letting bg_start run on
// the host while the operator believes everything is confined, the feature
// stays off in serve sandbox mode.
func newServeBGManager(resolved config.ResolvedConfig) *bgproc.Manager {
	if !resolved.Background.Enabled || resolved.Sandbox {
		return nil
	}
	s := backgroundSettingsFrom(resolved)
	if s.MaxJobs <= 0 {
		s.MaxJobs = 8
	}
	if s.MaxOutputBytes <= 0 {
		s.MaxOutputBytes = 1 << 20
	}
	cfg := bgproc.Config{
		MaxJobsPerSession: s.MaxJobs,
		MaxOutputBytes:    s.MaxOutputBytes,
	}
	if s.MaxTimeoutSeconds > 0 {
		cfg.MaxTimeout = time.Duration(s.MaxTimeoutSeconds) * time.Second
	}
	// Same sink the agents' EventHandler feeds: the /api/events ring. The
	// bgEventObserver emits hashes and counters only — never raw command
	// text or job output.
	eventObs := bgproc.Observer(&bgEventObserver{emit: func(ev events.Event) {
		serveEvents.add(ev)
	}})
	// Wake-on-complete: fan the dispatcher and the frame emitter in beside
	// the event observer so job exits also schedule system-initiated wake
	// turns and reach bound clients as bg_job frames (serve surface only;
	// REPL/Telegram/run managers keep their single observers).
	obs := []bgproc.Observer{eventObs}
	if s.WakeOnComplete {
		d := newWakeDispatcher(wsWakeRouter{}, wakeSettings{
			CoalesceMS:   s.WakeCoalesceMS,
			MaxWakesHour: s.MaxWakesPerHour,
		})
		serveWakeDispatcher.Store(d)
		obs = append(obs, d)
	}
	obs = append(obs, bgFrameEmitter{})
	eventObs = &bgObserverGroup{list: obs}
	return bgproc.NewManager(cfg, eventObs)
}

// setServeBGManager installs the shared manager at serve startup.
func setServeBGManager(mgr *bgproc.Manager) {
	if mgr == nil {
		serveBG.Store(nil)
		return
	}
	serveBG.Store(mgr)
}

// shutdownServeBG kills every job still running across all sessions
// (process-exit contract: no detach mode). Idempotent; returns the killed
// jobs for logging. The wake dispatcher is stopped first so jobs killed
// here cannot schedule wakes during teardown.
func shutdownServeBG() []bgproc.Job {
	stopServeWake := serveWakeDispatcher.Swap(nil)
	if stopServeWake != nil {
		stopServeWake.Stop()
	}
	mgr := serveBG.Swap(nil)
	if mgr == nil {
		return nil
	}
	return mgr.Shutdown()
}

// newServeBGRuntime binds ONE agent to the shared manager. The runtime's
// session is empty at construction — the stable store session id is only
// known once a prompt resolves or creates the session, and it is bound by
// handlePrompt (bindBGRuntime) on the same goroutine that later runs the
// tools, so jobs land in the session the WS is actually attached to and
// survive across runs in one session. notify=false (background.notify
// == "off") leaves the provider unset: the agent must poll with
// bg_status/bg_output instead of receiving injected summaries.
func newServeBGRuntime(mgr *bgproc.Manager, notify bool) *bgRuntime {
	if mgr == nil {
		return nil
	}
	rt := &bgRuntime{mgr: mgr}
	if notify {
		rt.provider = func() string {
			// Runs on the engine's iteration path; rt.session was bound
			// before the agent started (same goroutine) — no lock needed.
			return rt.formatNotices(mgr.DrainNotices(rt.session))
		}
	}
	return rt
}

// bindBGRuntime points the runtime at the session the prompt resolved.
// Single-goroutine discipline: callers run on the connection's processor
// loop (WS) or the run goroutine (headless), which are also the only
// goroutines that invoke the tools and the notice provider.
func bindBGRuntime(rt *bgRuntime, sessionID string) {
	if rt != nil && sessionID != "" {
		rt.session = sessionID
	}
}

// ── REST surface ─────────────────────────────────────────────────────────

// authenticateJobsRequest validates the instance-authenticated request's
// session scope: session_id query parameter plus session auth token
// (X-Session-Token header / session_token cookie) — the exact contract of
// /api/cancel. Returns the session, or a non-zero HTTP status with message.
func authenticateJobsRequest(store *session.Store, r *http.Request) (*session.Session, int, string) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		return nil, http.StatusBadRequest, "missing session_id"
	}
	sess, err := store.Load(sessionID)
	if err != nil || sess == nil {
		return nil, http.StatusNotFound, "session not found"
	}
	if !validateSessionTokenStrict(store, sess, sessionTokenFromRequest(r)) {
		return nil, http.StatusUnauthorized, "invalid session token"
	}
	return sess, 0, ""
}

// jobIDFromPath extracts the job id from /api/jobs/{id}/{action}.
func jobIDFromPath(path, action string) string {
	return strings.TrimSuffix(strings.TrimPrefix(path, "/api/jobs/"), "/"+action)
}

// handleJobsList serves GET /api/jobs: the authenticated session's jobs,
// oldest first. Commands are rendered as a bounded head only.
func handleJobsList(store *session.Store, mgr *bgproc.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sess, code, msg := authenticateJobsRequest(store, r)
		if code != 0 {
			http.Error(w, msg, code)
			return
		}
		out := make([]map[string]any, 0)
		if mgr != nil {
			for _, j := range mgr.List(sess.ID) {
				item := map[string]any{
					"id":        j.ID,
					"command":   headString(j.Command, bgCommandHead),
					"status":    string(j.Status),
					"runtime_s": jobRuntimeSeconds(j),
				}
				if j.Status != bgproc.StatusRunning {
					item["exit_code"] = j.ExitCode
				}
				out = append(out, item)
			}
		}
		writeAPIJSON(w, http.StatusOK, map[string]any{"jobs": out})
	}
}

// handleJobOutput serves GET /api/jobs/{id}/output?since=N&limit=N:
// paginated output with an absolute byte cursor. limit defaults to the same
// 32 KiB chunk the bg_output tool returns; unknown or foreign ids get the
// tools' {"status":"unknown"} shape with a 404.
func handleJobOutput(store *session.Store, mgr *bgproc.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sess, code, msg := authenticateJobsRequest(store, r)
		if code != 0 {
			http.Error(w, msg, code)
			return
		}
		jobID := jobIDFromPath(r.URL.Path, "output")
		if jobID == "" {
			http.Error(w, "missing job id", http.StatusBadRequest)
			return
		}
		var since int64
		if v := r.URL.Query().Get("since"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				http.Error(w, "invalid since", http.StatusBadRequest)
				return
			}
			since = n
		}
		limit := bgOutputChunkBytes
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			limit = n
		}
		if mgr == nil {
			writeAPIJSON(w, http.StatusNotFound, map[string]any{"job_id": jobID, "status": "unknown"})
			return
		}
		chunk, next, err := mgr.Output(sess.ID, jobID, since, limit)
		if err != nil {
			writeAPIJSON(w, http.StatusNotFound, map[string]any{"job_id": jobID, "status": "unknown"})
			return
		}
		// End-of-stream normalization: the manager reports ("", end) once
		// since reaches the logical end; the API contract is next_cursor 0
		// when there is nothing more to read (0 = restart from the top).
		if chunk == "" {
			next = 0
		}
		writeAPIJSON(w, http.StatusOK, map[string]any{
			"job_id":      jobID,
			"output":      chunk,
			"next_cursor": next,
		})
	}
}

// handleJobStop serves POST /api/jobs/{id}/stop: kill a job owned by the
// authenticated session. Stopping an already-finished job reports its final
// state; unknown or foreign ids get {"status":"unknown"} with a 404.
func handleJobStop(store *session.Store, mgr *bgproc.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sess, code, msg := authenticateJobsRequest(store, r)
		if code != 0 {
			http.Error(w, msg, code)
			return
		}
		jobID := jobIDFromPath(r.URL.Path, "stop")
		if jobID == "" {
			http.Error(w, "missing job id", http.StatusBadRequest)
			return
		}
		if mgr == nil {
			writeAPIJSON(w, http.StatusNotFound, map[string]any{"job_id": jobID, "status": "unknown"})
			return
		}
		job, ok := mgr.Stop(sess.ID, jobID)
		if !ok {
			writeAPIJSON(w, http.StatusNotFound, map[string]any{"job_id": jobID, "status": "unknown"})
			return
		}
		writeAPIJSON(w, http.StatusOK, map[string]any{
			"job_id": job.ID,
			"status": string(job.Status),
		})
	}
}

// handleJobByID routes /api/jobs/{id}/{action} — the same suffix-dispatch
// pattern as /api/runs/.
func handleJobByID(store *session.Store, mgr *bgproc.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
		switch {
		case strings.HasSuffix(rest, "/output"):
			handleJobOutput(store, mgr)(w, r)
		case strings.HasSuffix(rest, "/stop"):
			handleJobStop(store, mgr)(w, r)
		default:
			http.NotFound(w, r)
		}
	}
}
