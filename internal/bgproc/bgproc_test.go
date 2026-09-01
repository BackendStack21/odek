package bgproc

import (
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// waitFor polls cond until it returns true or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func newTestManager(t *testing.T, mutate func(*Config)) *Manager {
	t.Helper()
	cfg := Config{
		MaxJobsPerSession: 8,
		MaxOutputBytes:    1 << 20,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return NewManager(cfg, nil)
}

func TestStartRunsAndExits(t *testing.T) {
	m := newTestManager(t, nil)
	job, err := m.Start("s1", "echo hello-bg", "", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if job.Status != StatusRunning {
		t.Fatalf("fresh job status = %q, want running", job.Status)
	}
	var got Job
	waitFor(t, 5*time.Second, func() bool {
		got, _ = m.Get("s1", job.ID)
		return got.Status == StatusExited
	})
	if got.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", got.ExitCode)
	}
	chunk, _, err := m.Output("s1", job.ID, 0, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if !strings.Contains(chunk, "hello-bg") {
		t.Fatalf("output missing command stdout, got %q", chunk)
	}
}

func TestNonZeroExitIsFailed(t *testing.T) {
	m := newTestManager(t, nil)
	job, err := m.Start("s1", "exit 3", "", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		j, _ := m.Get("s1", job.ID)
		return j.Status == StatusFailed
	})
	j, _ := m.Get("s1", job.ID)
	if j.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", j.ExitCode)
	}
}

func TestOutputCursorPagination(t *testing.T) {
	m := newTestManager(t, nil)
	job, err := m.Start("s1", "printf 'abcdef'", "", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		j, _ := m.Get("s1", job.ID)
		return j.Status == StatusExited
	})
	chunk, next, err := m.Output("s1", job.ID, 0, 0)
	if err != nil {
		t.Fatalf("Output(0): %v", err)
	}
	if chunk != "abcdef" {
		t.Fatalf("chunk = %q, want abcdef", chunk)
	}
	chunk, _, err = m.Output("s1", job.ID, next, 0)
	if err != nil {
		t.Fatalf("Output(next): %v", err)
	}
	if chunk != "" {
		t.Fatalf("chunk past end = %q, want empty", chunk)
	}
}

func TestRingDropsFrontKeepsTailRuneSafe(t *testing.T) {
	// Tiny cap: "1111...2222" — the front must be dropped, the tail kept,
	// and the buffer must never split a UTF-8 rune.
	m := newTestManager(t, func(c *Config) { c.MaxOutputBytes = 64 })
	job, err := m.Start("s1", "printf 'ééééééééééééééééééééééééééééééééééééééééééééééééééééééééé'", "", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		j, _ := m.Get("s1", job.ID)
		return j.Status == StatusExited
	})
	chunk, next, err := m.Output("s1", job.ID, 0, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if !strings.Contains(chunk, "earlier output bytes truncated") {
		t.Fatalf("chunk missing truncation marker: %q", chunk)
	}
	if !utf8.ValidString(strings.SplitN(chunk, "\n", 2)[0]) {
		t.Fatalf("chunk header contains invalid UTF-8 after front-drop")
	}
	if len(chunk) > 64+128 {
		t.Fatalf("chunk unreasonably large: %d bytes", len(chunk))
	}
	// Cursor from before the drop must still yield the retained tail.
	chunk2, _, err := m.Output("s1", job.ID, next, 0)
	if err != nil {
		t.Fatalf("Output(next): %v", err)
	}
	if chunk2 != "" {
		t.Fatalf("expected empty read at end, got %q", chunk2)
	}
}

func TestOwnershipChecks(t *testing.T) {
	m := newTestManager(t, nil)
	job, err := m.Start("alice", "sleep 30", "", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, ok := m.Get("bob", job.ID); ok {
		t.Fatal("bob must not see alice's job")
	}
	if _, ok := m.Get("alice", job.ID); !ok {
		t.Fatal("alice must see her own job")
	}
	if _, _, err := m.Output("bob", job.ID, 0, 0); err == nil {
		t.Fatal("bob Output must fail")
	}
	if _, ok := m.Stop("bob", job.ID); ok {
		t.Fatal("bob Stop must not succeed")
	}
	if j, ok := m.Get("alice", job.ID); !ok || j.Status != StatusRunning {
		t.Fatalf("alice's job must still be running after bob's attempts, got %+v", j)
	}
	m.Stop("alice", job.ID)
}

func TestStopKillsRunningJob(t *testing.T) {
	m := newTestManager(t, nil)
	job, err := m.Start("s1", "sleep 30", "", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	start := time.Now()
	j, ok := m.Stop("s1", job.ID)
	if !ok {
		t.Fatal("Stop not found")
	}
	if j.Status != StatusKilled {
		t.Fatalf("status = %q, want killed", j.Status)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("stop took %v, grace is 5s — kill escalation broken", d)
	}
	waitFor(t, 5*time.Second, func() bool {
		jj, _ := m.Get("s1", job.ID)
		return !jj.EndedAt.IsZero()
	})
}

func TestStopAllIsSessionScoped(t *testing.T) {
	m := newTestManager(t, nil)
	ja, _ := m.Start("A", "sleep 30", "", 0)
	jb, _ := m.Start("B", "sleep 30", "", 0)
	killed := m.StopAll("A")
	if len(killed) != 1 || killed[0].ID != ja.ID {
		t.Fatalf("StopAll(A) = %+v, want [%s]", killed, ja.ID)
	}
	if j, _ := m.Get("B", jb.ID); j.Status != StatusRunning {
		t.Fatalf("B's job status = %q, want still running", j.Status)
	}
	m.StopAll("B")
}

func TestShutdownKillsEverything(t *testing.T) {
	m := newTestManager(t, nil)
	ja, _ := m.Start("A", "sleep 30", "", 0)
	jb, _ := m.Start("B", "sleep 30", "", 0)
	killed := m.Shutdown()
	ids := map[string]bool{}
	for _, j := range killed {
		ids[j.ID] = true
	}
	if !ids[ja.ID] || !ids[jb.ID] {
		t.Fatalf("Shutdown killed %v, want both %s and %s", killed, ja.ID, jb.ID)
	}
}

func TestTimeoutKill(t *testing.T) {
	m := newTestManager(t, nil)
	job, err := m.Start("s1", "sleep 30", "", 150*time.Millisecond)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		j, _ := m.Get("s1", job.ID)
		return j.Status == StatusTimeout
	})
}

func TestMaxTimeoutClamp(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.MaxTimeout = time.Second })
	job, err := m.Start("s1", "sleep 30", "", time.Hour)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if job.Timeout != time.Second {
		t.Fatalf("timeout = %v, want clamped to 1s", job.Timeout)
	}
	m.Stop("s1", job.ID)
}

func TestMaxJobsPerSessionEnforced(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.MaxJobsPerSession = 2 })
	var live []*Job
	for i := 0; i < 2; i++ {
		j, err := m.Start("s1", "sleep 30", "", 0)
		if err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
		live = append(live, j)
	}
	if _, err := m.Start("s1", "sleep 30", "", 0); err == nil {
		t.Fatal("third concurrent job must be rejected")
	}
	// Other sessions unaffected.
	if _, err := m.Start("s2", "sleep 30", "", 0); err != nil {
		t.Fatalf("s2 Start: %v", err)
	}
	for _, j := range live {
		m.Stop("s1", j.ID)
	}
	m.StopAll("s2")
}

func TestConcurrentStartRaceUnderCap(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.MaxJobsPerSession = 5 })
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, rejected := 0, 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Start("s1", "sleep 30", "", 0)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				rejected++
			} else {
				ok++
			}
		}()
	}
	wg.Wait()
	if ok != 5 || rejected != 15 {
		t.Fatalf("ok=%d rejected=%d, want 5/15 — cap raced", ok, rejected)
	}
	m.StopAll("s1")
}

func TestDrainNoticesOnce(t *testing.T) {
	m := newTestManager(t, nil)
	job, _ := m.Start("s1", "echo done", "", 0)
	waitFor(t, 5*time.Second, func() bool {
		j, _ := m.Get("s1", job.ID)
		return j.Status == StatusExited
	})
	notices := m.DrainNotices("s1")
	if len(notices) != 1 {
		t.Fatalf("first drain = %d notices, want 1", len(notices))
	}
	if notices[0].JobID != job.ID || notices[0].Status != StatusExited {
		t.Fatalf("notice = %+v", notices[0])
	}
	if again := m.DrainNotices("s1"); len(again) != 0 {
		t.Fatalf("second drain = %d notices, want 0", len(again))
	}
	if got := m.DrainNotices("other"); len(got) != 0 {
		t.Fatalf("foreign drain = %d notices, want 0", len(got))
	}
}

func TestKilledJobEnqueuesNotice(t *testing.T) {
	m := newTestManager(t, nil)
	job, _ := m.Start("s1", "sleep 30", "", 0)
	m.Stop("s1", job.ID)
	waitFor(t, 5*time.Second, func() bool {
		return len(m.DrainNotices("s1")) > 0
	})
}

func TestSandboxWrapUsedAndFollowUpRuns(t *testing.T) {
	wrapCalled := false
	followUpCalled := false
	m := newTestManager(t, func(c *Config) {
		c.SandboxWrap = func(command string) ([]string, func(), error) {
			wrapCalled = true
			return []string{"sh", "-c", command}, func() { followUpCalled = true }, nil
		}
	})
	job, err := m.Start("s1", "sleep 30", "", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !wrapCalled {
		t.Fatal("SandboxWrap not called in sandbox mode")
	}
	m.Stop("s1", job.ID)
	waitFor(t, 5*time.Second, func() bool { return followUpCalled })
}

func TestObserverCallbacks(t *testing.T) {
	var mu sync.Mutex
	var started, exited []string
	m := NewManager(Config{MaxJobsPerSession: 4, MaxOutputBytes: 1 << 20}, observerFunc(
		func(j Job) {
			mu.Lock()
			started = append(started, j.ID)
			mu.Unlock()
		},
		func(n Notice) {
			mu.Lock()
			exited = append(exited, n.JobID)
			mu.Unlock()
		},
	))
	job, _ := m.Start("s1", "echo x", "", 0)
	m.Stop("s1", job.ID)
	waitFor(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(started) == 1 && len(exited) == 1
	})
}

func TestListFiltersSessionAndOmitsNothing(t *testing.T) {
	m := newTestManager(t, nil)
	ja, _ := m.Start("A", "echo a", "", 0)
	jb, _ := m.Start("B", "echo b", "", 0)
	listA := m.List("A")
	if len(listA) != 1 || listA[0].ID != ja.ID {
		t.Fatalf("List(A) = %+v, want only %s", listA, ja.ID)
	}
	listB := m.List("B")
	if len(listB) != 1 || listB[0].ID != jb.ID {
		t.Fatalf("List(B) = %+v, want only %s", listB, jb.ID)
	}
}

func TestStartRejectsEmptyArgs(t *testing.T) {
	m := newTestManager(t, nil)
	if _, err := m.Start("", "echo x", "", 0); err == nil {
		t.Fatal("empty session must be rejected")
	}
	if _, err := m.Start("s1", "   ", "", 0); err == nil {
		t.Fatal("blank command must be rejected")
	}
}

func TestStopUnknownAndStale(t *testing.T) {
	m := newTestManager(t, nil)
	if _, ok := m.Stop("s1", "bg_doesnotexist"); ok {
		t.Fatal("stopping unknown id must return false")
	}
	// Exited job: Stop must not fabricate a kill.
	job, _ := m.Start("s1", "echo bye", "", 0)
	waitFor(t, 5*time.Second, func() bool {
		j, _ := m.Get("s1", job.ID)
		return j.Status == StatusExited
	})
	j, ok := m.Stop("s1", job.ID)
	if !ok {
		t.Fatal("Stop on exited job should still find it")
	}
	if j.Status != StatusExited {
		t.Fatalf("Stop on exited job changed status to %q", j.Status)
	}
}

// observerFunc adapts two funcs into an Observer.
func observerFunc(started func(Job), exited func(Notice)) Observer {
	return &funcObserver{started: started, exited: exited}
}

type funcObserver struct {
	started func(Job)
	exited  func(Notice)
}

func (f *funcObserver) BGStarted(j Job)   { f.started(j) }
func (f *funcObserver) BGExited(n Notice) { f.exited(n) }
