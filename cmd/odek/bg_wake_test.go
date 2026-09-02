package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/bgproc"
)

// ── fakes ────────────────────────────────────────────────────────────────

type fakeWakeRouter struct {
	mu     sync.Mutex
	state  wakeConnState
	posts  []wsClientMsg
	postOK bool
}

func newFakeWakeRouter(state wakeConnState) *fakeWakeRouter {
	return &fakeWakeRouter{state: state, postOK: true}
}

func (f *fakeWakeRouter) State(sessionID string) wakeConnState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *fakeWakeRouter) Post(sessionID string, item wsClientMsg) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.postOK {
		return false
	}
	f.posts = append(f.posts, item)
	return true
}

func (f *fakeWakeRouter) postCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.posts)
}

// ── dispatcher ───────────────────────────────────────────────────────────

func newTestDispatcher(router wakeRouter, coalesce time.Duration, maxPerHour int) *wakeDispatcher {
	return newWakeDispatcher(router, wakeSettings{
		CoalesceMS:   int(coalesce.Milliseconds()),
		MaxWakesHour: maxPerHour,
	})
}

func TestWakeDispatcher_CoalescesExitsInWindow(t *testing.T) {
	router := newFakeWakeRouter(wakeIdle)
	d := newTestDispatcher(router, 30*time.Millisecond, 60)
	defer d.Stop()

	for i := 0; i < 3; i++ {
		d.BGExited(bgproc.Notice{JobID: "bg_1", SessionID: "sess-1"})
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n := router.postCount(); n == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if n := router.postCount(); n != 1 {
		t.Fatalf("wake posts = %d, want exactly 1 (three exits inside the coalesce window share one wake)", n)
	}
	router.mu.Lock()
	item := router.posts[0]
	router.mu.Unlock()
	if item.Type != "bg_wake" {
		t.Errorf("wake item type = %q, want %q", item.Type, "bg_wake")
	}
	if item.SessionID != "sess-1" {
		t.Errorf("wake item session = %q, want %q", item.SessionID, "sess-1")
	}
}

func TestWakeDispatcher_BusySessionDrops(t *testing.T) {
	router := newFakeWakeRouter(wakeBusy)
	d := newTestDispatcher(router, 10*time.Millisecond, 60)
	defer d.Stop()

	d.BGExited(bgproc.Notice{JobID: "bg_1", SessionID: "sess-1"})
	time.Sleep(60 * time.Millisecond)

	if n := router.postCount(); n != 0 {
		t.Fatalf("wake posts = %d, want 0 (a busy session relies on the per-iteration drain)", n)
	}
}

func TestWakeDispatcher_NoConnDrops(t *testing.T) {
	router := newFakeWakeRouter(wakeNoConn)
	d := newTestDispatcher(router, 10*time.Millisecond, 60)
	defer d.Stop()

	d.BGExited(bgproc.Notice{JobID: "bg_1", SessionID: "sess-1"})
	time.Sleep(60 * time.Millisecond)

	if n := router.postCount(); n != 0 {
		t.Fatalf("wake posts = %d, want 0 (no bound connection: payload path covers it)", n)
	}
}

func TestWakeDispatcher_RateLimitPerHour(t *testing.T) {
	router := newFakeWakeRouter(wakeIdle)
	d := newTestDispatcher(router, 5*time.Millisecond, 2) // 2 wakes/hour
	defer d.Stop()

	for i := 0; i < 3; i++ {
		d.BGExited(bgproc.Notice{JobID: "bg_x", SessionID: "sess-1"})
		time.Sleep(25 * time.Millisecond) // let each coalesce window fire separately
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n := router.postCount(); n >= 2 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := router.postCount(); n != 2 {
		t.Fatalf("wake posts = %d, want exactly 2 (third wake exceeds the per-hour ceiling)", n)
	}
}

func TestWakeDispatcher_FullQueueDoesNotConsumeBudget(t *testing.T) {
	router := newFakeWakeRouter(wakeIdle)
	router.postOK = false // queue full / closed: Post fails
	d := newTestDispatcher(router, 5*time.Millisecond, 1)
	defer d.Stop()

	d.BGExited(bgproc.Notice{JobID: "bg_1", SessionID: "sess-1"})
	time.Sleep(40 * time.Millisecond)

	// The failed post must not consume the wake budget: after the router
	// recovers, the next exit still wakes.
	router.mu.Lock()
	router.postOK = true
	router.mu.Unlock()
	d.BGExited(bgproc.Notice{JobID: "bg_2", SessionID: "sess-1"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n := router.postCount(); n == 1 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("wake posts = 0, want 1 (a failed post must not consume the wake budget)")
}

func TestWakeDispatcher_StopPreventsFurtherWakes(t *testing.T) {
	router := newFakeWakeRouter(wakeIdle)
	d := newTestDispatcher(router, 5*time.Millisecond, 60)
	d.Stop()

	d.BGExited(bgproc.Notice{JobID: "bg_1", SessionID: "sess-1"})
	time.Sleep(40 * time.Millisecond)

	if n := router.postCount(); n != 0 {
		t.Fatalf("wake posts = %d, want 0 after Stop", n)
	}
}

// ── guarded slot (W2: enqueue-after-close must never panic) ─────────────

func TestConnWakeSlot_PostAfterCloseIsSafe(t *testing.T) {
	ch := make(chan []byte, 1)
	slot := newConnWakeSlot(ch)
	slot.close()

	if slot.post([]byte(`{}`)) {
		t.Fatal("post on closed slot reported success")
	}
}

func TestConnWakeSlot_PostFullDrops(t *testing.T) {
	ch := make(chan []byte, 1)
	slot := newConnWakeSlot(ch)
	if !slot.post([]byte(`{}`)) {
		t.Fatal("first post into an empty buffered slot should succeed")
	}
	if slot.post([]byte(`{}`)) {
		t.Fatal("post into a full slot should drop (turns queued ⇒ drain covers)")
	}
}

func TestConnWakeSlot_ConcurrentPostDuringCloseIsSafe(t *testing.T) {
	// close() closes the channel under the same lock that guards post():
	// a concurrent post either lands before the close (buffered,
	// non-blocking) or observes closed and drops. Any send on a closed
	// channel would panic — this hammers the boundary.
	ch := make(chan []byte, 8)
	slot := newConnWakeSlot(ch)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			slot.post([]byte(`{"type":"bg_wake"}`))
		}
	}()
	slot.close()
	wg.Wait()
}

// ── wsWakeRouter (registry-backed) ───────────────────────────────────────

func TestWSWakeRouter_StatePerSession(t *testing.T) {
	router := wsWakeRouter{}

	// No connection bound.
	if got := router.State("sess-none"); got != wakeNoConn {
		t.Fatalf("State = %v, want wakeNoConn with no bound connection", got)
	}

	// One idle connection bound.
	info := &wsConnInfo{ID: newWSConnID()}
	info.setLive("sess-1", false)
	info.wakeSlot = newConnWakeSlot(make(chan []byte, 8))
	wsConnRegister(info)
	defer wsConnUnregister(info.ID)
	if got := router.State("sess-1"); got != wakeIdle {
		t.Fatalf("State = %v, want wakeIdle with one idle bound connection", got)
	}

	// A SECOND connection bound to the same session going busy makes the
	// whole session busy (W3: per-session exclusion — a wake turn must
	// never run concurrently with the other connection's turn).
	info2 := &wsConnInfo{ID: newWSConnID()}
	info2.setLive("sess-1", true)
	wsConnRegister(info2)
	defer wsConnUnregister(info2.ID)
	if got := router.State("sess-1"); got != wakeBusy {
		t.Fatalf("State = %v, want wakeBusy when any bound connection is busy", got)
	}

	info2.setLive("sess-1", false)
	if got := router.State("sess-1"); got != wakeIdle {
		t.Fatalf("State = %v, want wakeIdle after the second connection went idle", got)
	}
}

func TestWSWakeRouter_PostDeliversToBoundSlot(t *testing.T) {
	info := &wsConnInfo{ID: newWSConnID()}
	info.setLive("sess-1", false)
	info.wakeSlot = newConnWakeSlot(make(chan []byte, 8))
	wsConnRegister(info)
	defer wsConnUnregister(info.ID)

	if !(wsWakeRouter{}).Post("sess-1", wsClientMsg{Type: "bg_wake", SessionID: "sess-1"}) {
		t.Fatal("Post to an idle bound connection reported failure")
	}
	raw := <-info.wakeSlot.ch
	var got wsClientMsg
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("wake item is not valid JSON: %v", err)
	}
	if got.Type != "bg_wake" || got.SessionID != "sess-1" {
		t.Fatalf("wake item = %+v, want type bg_wake for sess-1", got)
	}
}

func TestWSWakeRouter_PostWithoutSlotDrops(t *testing.T) {
	// A bound connection whose slot is nil (registration/teardown window)
	// must drop the wake, not panic.
	info := &wsConnInfo{ID: newWSConnID()}
	info.setLive("sess-1", false)
	wsConnRegister(info)
	defer wsConnUnregister(info.ID)

	if (wsWakeRouter{}).Post("sess-1", wsClientMsg{Type: "bg_wake", SessionID: "sess-1"}) {
		t.Fatal("Post without a wake slot must drop")
	}
}

// ── bg_job frame (M2) ────────────────────────────────────────────────────

func TestBGJobFrame_TerminalFieldsAndRedaction(t *testing.T) {
	secret := "AKIA" + "IOSFODNN7EXAMPL" + "E"
	n := bgproc.Notice{
		JobID:       "bg_9",
		SessionID:   "sess-1",
		Status:      bgproc.StatusExited,
		ExitCode:    0,
		Duration:    1500 * time.Millisecond,
		OutputBytes: 2048,
		Command:     "deploy.sh --token=" + secret,
	}
	f := bgJobFrame(n, false)

	for _, k := range []string{"type", "job_id", "session_id", "status", "exit_code", "duration_ms", "output_bytes", "t", "command_head"} {
		if _, ok := f[k]; !ok {
			t.Errorf("bg_job terminal frame missing %q", k)
		}
	}
	if f["type"] != "bg_job" || f["job_id"] != "bg_9" || f["session_id"] != "sess-1" {
		t.Fatalf("frame identity wrong: %+v", f)
	}
	head, _ := f["command_head"].(string)
	if strings.Contains(head, secret) {
		t.Fatalf("secret leaked into command_head: %q", head)
	}
	if !strings.Contains(head, "deploy.sh") {
		t.Fatalf("command_head lost the benign prefix: %q", head)
	}
}

func TestBGJobFrame_StartedOmitsTerminalKeys(t *testing.T) {
	// Absent keys, not zero values: old clients and status-based upserts
	// must not see exit_code=0/output_bytes=0 before the job ends.
	s := bgJobFrame(bgproc.Notice{JobID: "bg_9", SessionID: "sess-1", Status: bgproc.StatusRunning, Command: "x"}, true)
	for _, k := range []string{"exit_code", "duration_ms", "output_bytes"} {
		if _, ok := s[k]; ok {
			t.Errorf("started frame carries %q (must be absent for version-matrix degradation)", k)
		}
	}
	if s["status"] != string(bgproc.StatusRunning) {
		t.Fatalf("started status = %v, want %q", s["status"], string(bgproc.StatusRunning))
	}
	if _, ok := s["command_head"]; !ok {
		t.Error("started frame should carry the clamped command_head")
	}
}
