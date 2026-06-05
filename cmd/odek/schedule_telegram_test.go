package main

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/schedule"
)

func newTGStore(t *testing.T) *schedule.Store {
	t.Helper()
	st, err := schedule.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewStoreAt: %v", err)
	}
	return st
}

// ── splitCronTask / parseScheduleOpts ──────────────────────────────────────

func TestSplitCronTask(t *testing.T) {
	tests := []struct {
		in       string
		wantCron string
		wantTask string
		wantOK   bool
	}{
		{"0 9 * * 1-5 stand up now", "0 9 * * 1-5", "stand up now", true},
		{"@daily summarize my email", "@daily", "summarize my email", true},
		{"  @hourly   check builds ", "@hourly", "check builds", true},
		{"0 9 * * 1-5", "", "", false}, // 5 fields, no task
		{"0 9 task", "", "", false},    // too few cron fields
		{"@daily", "", "", false},      // macro, no task
		{"", "", "", false},
	}
	for _, tc := range tests {
		cron, task, ok := splitCronTask(tc.in)
		if ok != tc.wantOK || (ok && (cron != tc.wantCron || task != tc.wantTask)) {
			t.Errorf("splitCronTask(%q) = (%q,%q,%v), want (%q,%q,%v)",
				tc.in, cron, task, ok, tc.wantCron, tc.wantTask, tc.wantOK)
		}
	}
}

func TestParseScheduleOpts(t *testing.T) {
	got := parseScheduleOpts(" tz=Europe/Berlin deliver=log catchup name=standup ")
	if got["tz"] != "Europe/Berlin" || got["deliver"] != "log" || got["name"] != "standup" {
		t.Errorf("key=value parse wrong: %+v", got)
	}
	if got["catchup"] != "true" {
		t.Errorf("bare flag should map to true: %+v", got)
	}
	if len(parseScheduleOpts("")) != 0 {
		t.Error("empty opts should be empty map")
	}
}

// ── add ─────────────────────────────────────────────────────────────────────

func TestTelegramScheduleAdd_DefaultsToThisChat(t *testing.T) {
	st := newTGStore(t)
	reloaded := false
	reply, run := telegramScheduleReply(555, "add 0 9 * * 1-5 Summarize my unread email",
		st, func() { reloaded = true }, true)
	if run != "" {
		t.Error("add should not produce a runTask")
	}
	if !strings.Contains(reply, "Added") {
		t.Fatalf("unexpected reply: %q", reply)
	}
	if !reloaded {
		t.Error("add should trigger a reload")
	}
	jobs, _ := st.List()
	if len(jobs) != 1 {
		t.Fatalf("want 1 job, got %d", len(jobs))
	}
	j := jobs[0]
	if j.Deliver.Kind != schedule.DeliverTelegram || j.Deliver.ChatID != 555 {
		t.Errorf("delivery should default to telegram:555, got %+v", j.Deliver)
	}
	if j.Name == "" || j.Cron != "0 9 * * 1-5" || !j.Enabled {
		t.Errorf("job fields wrong: %+v", j)
	}
}

func TestTelegramScheduleAdd_Options(t *testing.T) {
	st := newTGStore(t)
	reply, _ := telegramScheduleReply(7, "add @daily Daily digest | tz=Europe/Berlin name=digest deliver=log catchup disabled",
		st, nil, true)
	if !strings.Contains(reply, "Added") {
		t.Fatalf("unexpected reply: %q", reply)
	}
	jobs, _ := st.List()
	j := jobs[0]
	if j.Timezone != "Europe/Berlin" || j.Name != "digest" || !j.Catchup || j.Enabled {
		t.Errorf("options not applied: %+v", j)
	}
	if j.Deliver.Kind != schedule.DeliverLog {
		t.Errorf("deliver=log not applied: %+v", j.Deliver)
	}
}

func TestTelegramScheduleAdd_Errors(t *testing.T) {
	st := newTGStore(t)
	cases := map[string]string{
		"missing args":   "add",
		"too few fields": "add 0 9 too-short",
		"invalid cron":   "add nope nope nope nope nope do the thing",
		"bad deliver":    "add 0 9 * * * a task | deliver=pigeon",
	}
	for name, args := range cases {
		reply, _ := telegramScheduleReply(1, args, st, nil, true)
		if !strings.HasPrefix(reply, "❗") && !strings.HasPrefix(reply, "❌") {
			t.Errorf("%s: expected an error reply, got %q", name, reply)
		}
	}
	if jobs, _ := st.List(); len(jobs) != 0 {
		t.Errorf("no job should persist from failed adds, got %d", len(jobs))
	}
}

// ── list / view / next ──────────────────────────────────────────────────────

func TestTelegramScheduleListViewNext(t *testing.T) {
	st := newTGStore(t)
	if reply, _ := telegramScheduleReply(1, "list", st, nil, true); !strings.Contains(reply, "No scheduled jobs") {
		t.Errorf("empty list reply: %q", reply)
	}
	a, _ := st.Add(schedule.Job{Name: "morning", Cron: "0 9 * * *", Task: "x",
		Deliver: schedule.Delivery{Kind: schedule.DeliverStdout}, Enabled: true})

	if reply, _ := telegramScheduleReply(1, "list", st, nil, true); !strings.Contains(reply, a.ID) {
		t.Errorf("list should include the job id: %q", reply)
	}
	if reply, _ := telegramScheduleReply(1, "view "+a.ID, st, nil, true); !strings.Contains(reply, "morning") {
		t.Errorf("view reply: %q", reply)
	}
	if reply, _ := telegramScheduleReply(1, "view jb-missing", st, nil, true); !strings.Contains(reply, "No job") {
		t.Errorf("view-missing reply: %q", reply)
	}
	if reply, _ := telegramScheduleReply(1, "next "+a.ID, st, nil, true); !strings.Contains(reply, a.ID) {
		t.Errorf("next-by-id reply: %q", reply)
	}
	if reply, _ := telegramScheduleReply(1, "next */15 * * * *", st, nil, true); !strings.Contains(reply, "UTC") {
		t.Errorf("next-by-cron reply: %q", reply)
	}
	if reply, _ := telegramScheduleReply(1, "next not-a-cron", st, nil, true); !strings.HasPrefix(reply, "❌") {
		t.Errorf("next-bad reply: %q", reply)
	}
}

// ── rm / enable / disable / run ─────────────────────────────────────────────

func TestTelegramScheduleMutations(t *testing.T) {
	st := newTGStore(t)
	a, _ := st.Add(schedule.Job{Name: "j", Cron: "0 9 * * *", Task: "do it",
		Deliver: schedule.Delivery{Kind: schedule.DeliverStdout}, Enabled: true})

	// disable / enable
	if reply, _ := telegramScheduleReply(1, "disable "+a.ID, st, nil, true); !strings.Contains(reply, "Disabled") {
		t.Errorf("disable reply: %q", reply)
	}
	if j, _, _ := st.Get(a.ID); j.Enabled {
		t.Error("job should be disabled")
	}
	if reply, _ := telegramScheduleReply(1, "enable "+a.ID, st, nil, true); !strings.Contains(reply, "Enabled") {
		t.Errorf("enable reply: %q", reply)
	}

	// run → returns the job task for the caller to dispatch
	reply, run := telegramScheduleReply(1, "run "+a.ID, st, nil, true)
	if run != "do it" || !strings.Contains(reply, "Running") {
		t.Errorf("run should return the task: reply=%q run=%q", reply, run)
	}
	if _, miss := telegramScheduleReply(1, "run jb-missing", st, nil, true); miss != "" {
		t.Error("run of a missing job should not produce a runTask")
	}

	// rm
	if reply, _ := telegramScheduleReply(1, "rm "+a.ID, st, nil, true); !strings.Contains(reply, "Removed") {
		t.Errorf("rm reply: %q", reply)
	}
	if jobs, _ := st.List(); len(jobs) != 0 {
		t.Errorf("job not removed, %d remain", len(jobs))
	}

	// usage errors for missing ids
	for _, args := range []string{"rm", "enable", "disable", "run", "view"} {
		if reply, _ := telegramScheduleReply(1, args, st, nil, true); !strings.HasPrefix(reply, "❗") {
			t.Errorf("%q with no id should return usage, got %q", args, reply)
		}
	}
}

// ── gating / help / nil store ───────────────────────────────────────────────

func TestTelegramSchedule_ManagementGate(t *testing.T) {
	st := newTGStore(t)
	a, _ := st.Add(schedule.Job{Name: "j", Cron: "0 9 * * *", Task: "x",
		Deliver: schedule.Delivery{Kind: schedule.DeliverStdout}, Enabled: true})

	// Mutating verbs are refused when management is disabled.
	for _, args := range []string{"add 0 9 * * * t", "rm " + a.ID, "enable " + a.ID, "disable " + a.ID, "run " + a.ID} {
		reply, run := telegramScheduleReply(1, args, st, nil, false)
		if !strings.Contains(reply, "disabled") || run != "" {
			t.Errorf("gated %q should be refused, got %q", args, reply)
		}
	}
	// Read-only verbs still work.
	if reply, _ := telegramScheduleReply(1, "list", st, nil, false); !strings.Contains(reply, a.ID) {
		t.Errorf("list should work even when management is disabled: %q", reply)
	}
	if reply, _ := telegramScheduleReply(1, "view "+a.ID, st, nil, false); strings.Contains(reply, "disabled (`schedules") {
		t.Error("view should not be gated")
	}
	// The job must be untouched.
	if jobs, _ := st.List(); len(jobs) != 1 {
		t.Errorf("gated mutations must not change the store, %d jobs", len(jobs))
	}
}

func TestTelegramSchedule_HelpAndUnknownAndNilStore(t *testing.T) {
	st := newTGStore(t)
	for _, args := range []string{"", "help"} {
		if reply, _ := telegramScheduleReply(1, args, st, nil, true); !strings.Contains(reply, "Schedule commands") {
			t.Errorf("%q should return usage, got %q", args, reply)
		}
	}
	if reply, _ := telegramScheduleReply(1, "bogus", st, nil, true); !strings.Contains(reply, "Unknown subcommand") {
		t.Errorf("unknown subcommand reply: %q", reply)
	}
	if reply, _ := telegramScheduleReply(1, "list", nil, nil, true); !strings.Contains(reply, "unavailable") {
		t.Errorf("nil store should report unavailable, got %q", reply)
	}
}
