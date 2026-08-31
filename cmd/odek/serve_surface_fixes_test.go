package main

// Tests for the serve-surface fix sweep (F8, F2, F7, F3/F4):
//
//   F8 — headless REST-run goroutines are tracked in serveRunsWG and
//        drainServeWork waits (bounded) for them at shutdown; previously a
//        blocking run outlived listener shutdown and died at process exit
//        with its cleanup defers (agent.Close → docker rm -f) never running.
//   F2 — the ping/pong heartbeat ran on the socket-reader goroutine while
//        the processor loop wrote resolved.Model (per-prompt model switch);
//        the reader now uses an immutable per-connection snapshot.
//   F7 — the wsConnSem slot acquired in the Handshake callback leaked when
//        x/net/websocket failed the upgrade after that callback returned
//        (newServerConn → AcceptHandshake error): the Handler — and with it
//        handleWS's release defer — never ran. serveWSUpgrades closes that
//        window.
//   F3/F4 — rate-limit keying: clientIP takes the LAST X-Forwarded-For
//        entry (left-most is client-supplied and spoofable behind a trusted
//        proxy), and rateLimiter.allow skips empty keys instead of
//        inserting an never-evicted "" bucket.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	golangws "golang.org/x/net/websocket"
)

// ── F4: empty rate-limit keys are skipped, not tracked ──────────────────

func TestRateLimiter_SkipsEmptyKey(t *testing.T) {
	rl := newRateLimiter(1, time.Minute)
	for i := 0; i < 5; i++ {
		if !rl.allow("") {
			t.Fatalf(`allow("") = false on call %d — unidentifiable clients must not be limited`, i+1)
		}
	}
	rl.mu.Lock()
	_, present := rl.windows[""]
	rl.mu.Unlock()
	if present {
		t.Fatal(`empty key "" was inserted into the rate-limiter map (it would never be evicted)`)
	}
}

// ── F3: clientIP keys on the LAST XFF entry behind a trusted proxy ──────

func TestClientIP_UsesLastForwardedEntryFromTrustedProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	// Left-most entry is client-supplied (spoofable behind a proxy); the
	// right-most is what the trusted proxy appended.
	req.Header.Set("X-Forwarded-For", "9.9.9.9, 7.7.7.7")
	if got := clientIP(req, []string{"10.0.0.1"}); got != "7.7.7.7" {
		t.Fatalf("clientIP = %q, want the last (right-most) forwarded entry %q", got, "7.7.7.7")
	}
}

// ── F7: the handshake-acquired slot survives post-handshake failures ────

// wsSemInFlight reports how many wsConnSem slots are currently held. Tests
// using it must stay sequential with anything that touches wsConnSem (the
// package's tests are sequential by default).
func wsSemInFlight(t *testing.T) int {
	t.Helper()
	held := 0
	for {
		select {
		case <-wsConnSem:
			held++
			wsConnSem <- struct{}{}
		default:
			return held
		}
	}
}

func TestServeWSUpgrades_ReleasesSlotWhenHandlerNeverRuns(t *testing.T) {
	baseline := wsSemInFlight(t)

	// Mimics wsHandshakeWithLimits: the handshake callback acquires a slot.
	handshake := func(cfg *golangws.Config, req *http.Request) error {
		wsConnSem <- struct{}{}
		return nil
	}
	handlerCalled := false
	handler := func(conn *golangws.Conn) { handlerCalled = true }

	// Mimics x/net/websocket failing the upgrade AFTER the handshake
	// callback returned nil (e.g. the 101 write to a vanished peer fails):
	// serveWebSocket returns without ever invoking the Handler.
	failedUpgrade := func(hs func(*golangws.Config, *http.Request) error, h func(*golangws.Conn), w http.ResponseWriter, r *http.Request) {
		if err := hs(nil, r); err != nil {
			return
		}
		// no h(...) — post-handshake failure
	}

	serveWSUpgrades(failedUpgrade, handshake, handler)(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ws", nil))

	if handlerCalled {
		t.Fatal("handler ran for a failed upgrade")
	}
	if got := wsSemInFlight(t); got != baseline {
		t.Fatalf("wsConnSem slots held = %d, want %d — a post-handshake failure leaked a slot", got, baseline)
	}
}

func TestServeWSUpgrades_HandlerOwnsReleaseOnSuccess(t *testing.T) {
	baseline := wsSemInFlight(t)

	handshake := func(cfg *golangws.Config, req *http.Request) error {
		wsConnSem <- struct{}{}
		return nil
	}
	handler := func(conn *golangws.Conn) {
		// Mirrors handleWS's first defer: the handler releases the slot
		// acquired by the handshake.
		select {
		case <-wsConnSem:
		default:
		}
	}
	successfulUpgrade := func(hs func(*golangws.Config, *http.Request) error, h func(*golangws.Conn), w http.ResponseWriter, r *http.Request) {
		if err := hs(nil, r); err == nil {
			h(nil)
		}
	}

	serveWSUpgrades(successfulUpgrade, handshake, handler)(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ws", nil))

	// Exactly one release must have happened: the handler's. A wrapper
	// double-release would leave one slot MORE available than baseline.
	if got := wsSemInFlight(t); got != baseline {
		t.Fatalf("wsConnSem slots held = %d, want %d — the wrapper must not release when the handler ran", got, baseline)
	}
}

func TestServeWSUpgrades_NoReleaseWhenHandshakeRejects(t *testing.T) {
	baseline := wsSemInFlight(t)

	handshake := func(cfg *golangws.Config, req *http.Request) error {
		return fmt.Errorf("rejected before acquire")
	}
	handler := func(conn *golangws.Conn) { t.Error("handler ran for a rejected handshake") }
	failedUpgrade := func(hs func(*golangws.Config, *http.Request) error, h func(*golangws.Conn), w http.ResponseWriter, r *http.Request) {
		_ = hs(nil, r)
	}

	serveWSUpgrades(failedUpgrade, handshake, handler)(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ws", nil))

	if got := wsSemInFlight(t); got != baseline {
		t.Fatalf("wsConnSem slots held = %d, want %d — released a slot that was never acquired", got, baseline)
	}
}

// ── F8: headless runs are tracked and drained at shutdown ───────────────

func TestDrainServeWork_WaitsForTrackedGoroutines(t *testing.T) {
	var finished atomic.Bool
	serveRunsWG.Add(1)
	go func() {
		defer serveRunsWG.Done()
		time.Sleep(150 * time.Millisecond)
		finished.Store(true)
	}()
	if !drainServeWork(5 * time.Second) {
		t.Fatal("drainServeWork timed out although the tracked goroutine finished in 150ms")
	}
	if !finished.Load() {
		t.Fatal("drainServeWork returned before the tracked goroutine finished")
	}
}

func TestDrainServeWork_BoundedByTimeout(t *testing.T) {
	serveRunsWG.Add(1)
	go func() {
		defer serveRunsWG.Done()
		time.Sleep(500 * time.Millisecond)
	}()
	start := time.Now()
	if drainServeWork(100 * time.Millisecond) {
		t.Fatal("drainServeWork reported success although the tracked goroutine was still running")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("drainServeWork overshot its bound: %v", elapsed)
	}
}

func TestStartServeRun_TrackedByDrainServeWork(t *testing.T) {
	release := make(chan struct{})
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		if callCount == 1 {
			<-release // hold the first chat call: the run blocks in handlePrompt
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"done"}}]}`))
	})
	defer llmSrv.Close()
	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()

	store := newTestSessionStore(t)
	resolved := config.LoadConfig(config.CLIFlags{})
	run, err := startServeRun(resolved, defaultSystem, store, nil, promptRequest{Content: "hello"})
	if err != nil {
		t.Fatalf("startServeRun: %v", err)
	}

	// While the run blocks on the LLM, a bounded drain must NOT complete —
	// the run goroutine is tracked (previously it was invisible to
	// shutdown, so a blocking run outlived the listener).
	if drainServeWork(250 * time.Millisecond) {
		t.Fatal("drain completed while a headless run was still executing — run goroutine is untracked")
	}

	close(release)
	if !drainServeWork(30 * time.Second) {
		t.Fatal("drain timed out after the run's LLM call was released")
	}
	if snap := run.snapshot(false); snap["status"] != "completed" {
		t.Fatalf("run status = %v, want completed", snap["status"])
	}
}

// ── F2: pong reads the immutable snapshot, not the live config ──────────

// The socket-reader goroutine answers pings while the processor loop may be
// writing resolved.Model (per-prompt model switch). The pong must carry the
// per-connection snapshot; reading the live struct is a data race (visible
// under -race once a ping and a model-switching prompt overlap).
func TestServe_E2E_PingPongUsesConfigSnapshotNotLiveModel(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	})
	defer llmSrv.Close()
	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()

	store := newTestSessionStore(t)
	ln, mux := buildServeMuxV2(t, store, func(rc *config.ResolvedConfig) { rc.Model = "initial-model" })
	defer ln.Close()
	go func() { _ = serveOnListener(ln, mux) }()
	waitForHTTP(t, ln.Addr().String())

	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()

	// server_info hello carries the snapshot too.
	hello := readWSUntil(t, conn, 10*time.Second, func(e map[string]any) bool { return e["type"] == "server_info" })
	if got, _ := hello["model"].(string); got != "initial-model" {
		t.Fatalf("server_info model = %q, want initial-model", got)
	}

	// Interleave model-switching prompts with pings: every round writes
	// resolved.Model on the processor goroutine (alternating names defeat
	// the != currentModel short-circuit) while the reader answers the ping.
	for i := 0; i < 20; i++ {
		writeJSON(conn, map[string]any{"type": "prompt", "content": "hi", "model": fmt.Sprintf("switched-model-%d", i)})
		writeJSON(conn, map[string]any{"type": "ping"})
		pong := readWSUntil(t, conn, 10*time.Second, func(e map[string]any) bool { return e["type"] == "pong" })
		if pong["t"] == nil {
			t.Errorf("round %d: pong missing t field: %v", i, pong)
		}
		if got, _ := pong["model"].(string); got != "initial-model" {
			t.Fatalf("round %d: pong model = %q, want the immutable snapshot %q (live reads race with the processor loop)", i, got, "initial-model")
		}
	}
}
