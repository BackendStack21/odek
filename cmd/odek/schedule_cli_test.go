package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/schedule"
	"github.com/BackendStack21/odek/internal/telegram"
)

// newCLITestStore returns a schedule store rooted at a temp dir.
func newCLITestStore(t *testing.T) *schedule.Store {
	t.Helper()
	st, err := schedule.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewStoreAt: %v", err)
	}
	return st
}

func validJob() schedule.Job {
	return schedule.Job{Name: "j", Cron: "0 9 * * *", Task: "do it",
		Deliver: schedule.Delivery{Kind: schedule.DeliverStdout}, Enabled: true}
}

// ── list ──────────────────────────────────────────────────────────────────

func TestScheduleList(t *testing.T) {
	st := newCLITestStore(t)
	// Empty store path.
	if err := scheduleList(st); err != nil {
		t.Fatalf("scheduleList(empty): %v", err)
	}
	// Enabled + disabled jobs exercise both the "yes"/"no" and last-status paths.
	if _, err := st.Add(validJob()); err != nil {
		t.Fatal(err)
	}
	dis := validJob()
	dis.Name, dis.Enabled = "off", false
	if _, err := st.Add(dis); err != nil {
		t.Fatal(err)
	}
	if err := scheduleList(st); err != nil {
		t.Fatalf("scheduleList(populated): %v", err)
	}
}

func TestScheduleList_StoreError(t *testing.T) {
	dir := t.TempDir()
	st, _ := schedule.NewStoreAt(dir)
	if err := os.WriteFile(filepath.Join(dir, "schedules.json"), []byte("{bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := scheduleList(st); err == nil {
		t.Error("scheduleList should surface a corrupt store")
	}
}

// ── add ───────────────────────────────────────────────────────────────────

func TestScheduleAdd(t *testing.T) {
	st := newCLITestStore(t)
	if err := scheduleAdd(st, []string{"--cron", "0 9 * * *", "--name", "morning", "summarize", "calendar"}); err != nil {
		t.Fatalf("scheduleAdd: %v", err)
	}
	jobs, _ := st.List()
	if len(jobs) != 1 || jobs[0].Name != "morning" {
		t.Fatalf("job not added correctly: %+v", jobs)
	}

	// Default name derived from the task, disabled flag honoured.
	if err := scheduleAdd(st, []string{"--cron", "* * * * *", "--disabled", "auto", "named", "job", "here"}); err != nil {
		t.Fatalf("scheduleAdd(disabled): %v", err)
	}
}

func TestScheduleAdd_Errors(t *testing.T) {
	st := newCLITestStore(t)
	cases := map[string][]string{
		"missing cron and task": {},
		"bad flag":              {"--nope"},
		"unknown deliver":       {"--cron", "* * * * *", "--deliver", "pigeon", "x"},
		"invalid cron rejected": {"--cron", "garbage", "x"},
	}
	for name, args := range cases {
		if err := scheduleAdd(st, args); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// ── rm / enable / disable ──────────────────────────────────────────────────

func TestScheduleRemove(t *testing.T) {
	st := newCLITestStore(t)
	a, _ := st.Add(validJob())
	if err := scheduleRemove(st, []string{a.ID}); err != nil {
		t.Fatalf("scheduleRemove: %v", err)
	}
	if err := scheduleRemove(st, nil); err == nil {
		t.Error("scheduleRemove with no args should error")
	}
	if err := scheduleRemove(st, []string{"jb-missing"}); err == nil {
		t.Error("scheduleRemove of a missing id should error")
	}
}

func TestScheduleSetEnabled(t *testing.T) {
	st := newCLITestStore(t)
	a, _ := st.Add(validJob())
	if err := scheduleSetEnabled(st, []string{a.ID}, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := scheduleSetEnabled(st, []string{a.ID}, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := scheduleSetEnabled(st, nil, true); err == nil {
		t.Error("enable with no args should error")
	}
	if err := scheduleSetEnabled(st, []string{"jb-missing"}, false); err == nil {
		t.Error("disable of a missing id should error")
	}
}

// ── next ───────────────────────────────────────────────────────────────────

func TestScheduleNext(t *testing.T) {
	st := newCLITestStore(t)
	a, _ := st.Add(validJob())

	if err := scheduleNext(st, []string{a.ID}); err != nil {
		t.Fatalf("next by id: %v", err)
	}
	if err := scheduleNext(st, []string{"*/15", "*", "*", "*", "*"}); err != nil {
		t.Fatalf("next by expression: %v", err)
	}
	// An impossible cron prints the "no further fires" line without erroring.
	if err := scheduleNext(st, []string{"0", "0", "30", "2", "*"}); err != nil {
		t.Fatalf("next impossible cron: %v", err)
	}
	if err := scheduleNext(st, nil); err == nil {
		t.Error("next with no args should error")
	}
	if err := scheduleNext(st, []string{"not-a-cron"}); err == nil {
		t.Error("next with a bad expression should error")
	}
}

func TestScheduleNext_StoreError(t *testing.T) {
	dir := t.TempDir()
	st, _ := schedule.NewStoreAt(dir)
	if err := os.WriteFile(filepath.Join(dir, "schedules.json"), []byte("{bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := scheduleNext(st, []string{"jb-x"}); err == nil {
		t.Error("scheduleNext should surface a corrupt store on Get")
	}
}

// ── dispatch ────────────────────────────────────────────────────────────────

func TestScheduleCmd_Dispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// No args → usage, no error.
	if err := scheduleCmd(nil); err != nil {
		t.Errorf("scheduleCmd(nil): %v", err)
	}
	// Unknown subcommand → error.
	if err := scheduleCmd([]string{"bogus"}); err == nil {
		t.Error("scheduleCmd(bogus) should error")
	}
	// Store-backed subcommands route correctly.
	if err := scheduleCmd([]string{"list"}); err != nil {
		t.Errorf("scheduleCmd(list): %v", err)
	}
	if err := scheduleCmd([]string{"add", "--cron", "0 9 * * *", "morning task"}); err != nil {
		t.Errorf("scheduleCmd(add): %v", err)
	}
	if err := scheduleCmd([]string{"ls"}); err != nil {
		t.Errorf("scheduleCmd(ls): %v", err)
	}
	// rm/enable/disable/next with missing args still route (and return usage errors).
	for _, sub := range [][]string{{"rm"}, {"enable"}, {"disable"}, {"next"}} {
		if err := scheduleCmd(sub); err == nil {
			t.Errorf("scheduleCmd(%v) should error on missing args", sub)
		}
	}
}

func TestPrintScheduleUsage(t *testing.T) {
	printScheduleUsage() // smoke: must not panic
}

// ── cliDeliverer stdout ─────────────────────────────────────────────────────

func TestCliDeliverer_Stdout(t *testing.T) {
	d := cliDeliverer{resolved: config.ResolvedConfig{}}
	job := schedule.Job{Name: "j", Deliver: schedule.Delivery{Kind: schedule.DeliverStdout}}
	if err := d.Deliver(context.Background(), job, "hello stdout"); err != nil {
		t.Errorf("stdout deliver: %v", err)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func TestSchedulerOptions(t *testing.T) {
	log := schedule.NopLogger{}
	// Valid timezone is loaded.
	opts := schedulerOptions(config.ScheduleConfig{Timezone: "Europe/Berlin", MaxConcurrent: 3, Catchup: true}, log)
	if opts.DefaultTZ == nil || opts.DefaultTZ.String() != "Europe/Berlin" {
		t.Errorf("DefaultTZ = %v, want Europe/Berlin", opts.DefaultTZ)
	}
	if opts.MaxConcurrent != 3 || !opts.Catchup {
		t.Errorf("options not carried through: %+v", opts)
	}
	// Invalid timezone falls back to UTC (and logs).
	opts = schedulerOptions(config.ScheduleConfig{Timezone: "Mars/Phobos"}, log)
	if opts.DefaultTZ != time.UTC {
		t.Errorf("invalid tz should fall back to UTC, got %v", opts.DefaultTZ)
	}
	// Empty timezone → UTC.
	opts = schedulerOptions(config.ScheduleConfig{}, log)
	if opts.DefaultTZ != time.UTC {
		t.Errorf("empty tz should be UTC, got %v", opts.DefaultTZ)
	}
}

func TestBuildScheduledMCPTools_NoServers(t *testing.T) {
	tools, cleanup, err := buildScheduledMCPTools(config.ResolvedConfig{})
	if err != nil {
		t.Fatalf("buildScheduledMCPTools: %v", err)
	}
	if tools != nil {
		t.Errorf("expected no tools, got %d", len(tools))
	}
	cleanup() // no-op, must not panic
}

func TestAppendScheduleLog_HomeError(t *testing.T) {
	t.Setenv("HOME", "")
	if err := appendScheduleLog(schedule.Job{ID: "jb-1", Name: "j"}, "x"); err == nil {
		t.Error("appendScheduleLog should error when HOME is unresolvable")
	}
}

func TestAcquireScheduleLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	unlock, err := acquireScheduleLock()
	if err != nil {
		t.Fatalf("acquireScheduleLock: %v", err)
	}
	pidPath := filepath.Join(home, ".odek", "schedule.pid")
	if _, err := os.Stat(pidPath); err != nil {
		t.Errorf("pid file not written: %v", err)
	}

	// A second acquire while this (live, odek-owned) process holds the lock must
	// be refused — but only when /proc reports an odek cmdline for our PID.
	if cmdline, err := os.ReadFile("/proc/self/cmdline"); err == nil && strings.Contains(string(cmdline), "odek") {
		if _, err := acquireScheduleLock(); err == nil {
			t.Error("expected refusal while a live owned daemon holds the lock")
		}
	}

	unlock()
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("unlock did not remove the pid file")
	}
	// After release, re-acquiring succeeds.
	u2, err := acquireScheduleLock()
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	u2()
}

func TestAcquireScheduleLock_HomeError(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := acquireScheduleLock(); err == nil {
		t.Error("acquireScheduleLock should error when HOME is unresolvable")
	}
}

// ── embedded scheduler (bot) lifecycle ──────────────────────────────────────

func TestStartSchedulerForBot_Disabled(t *testing.T) {
	stop := startSchedulerForBot(context.Background(), nil, config.ResolvedConfig{}, "system",
		telegram.NewFileLogger(telegram.LogInfo, ""))
	stop() // disabled → no-op stop, must not panic
}

func TestStartSchedulerForBot_StartAndStop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bot, _ := newRecordingTestBot(t)
	resolved := config.ResolvedConfig{
		Schedules: config.ScheduleConfig{Enabled: true, MaxConcurrent: 2, Timezone: "UTC"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := startSchedulerForBot(ctx, bot, resolved, "system", telegram.NewFileLogger(telegram.LogInfo, ""))
	cancel()
	stop() // drains the scheduler goroutine, cleans up MCP, releases the lock
}

// ── telegramRunner budget gate ──────────────────────────────────────────────

func TestTelegramRunner_BudgetExhausted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".odek"), 0755); err != nil {
		t.Fatal(err)
	}
	// Seed today's usage file above the limit so the pre-flight check trips
	// before any (LLM-dependent) task execution.
	date := time.Now().Format("2006-01-02")
	usageFile := filepath.Join(home, ".odek", "telegram_token_usage_"+date)
	if err := os.WriteFile(usageFile, []byte("1000"), 0644); err != nil {
		t.Fatal(err)
	}
	bot := telegram.NewBot("test:token")
	bot.SetDailyTokenBudget(10)
	r := telegramRunner{
		resolved: config.ResolvedConfig{Telegram: telegram.TelegramConfig{DailyTokenBudget: 10}},
		bot:      bot,
	}
	_, _, err := r.Run(context.Background(), schedule.Job{Task: "x"})
	if err == nil {
		t.Error("expected a budget-exhausted error before task execution")
	}
}
