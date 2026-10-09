package main

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/schedule"
)

func TestScheduleAdd_RejectsFlagAfterTask(t *testing.T) {
	st, err := schedule.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = scheduleAdd(st, []string{"--cron", "0 9 * * *", "backup the notes", "--disabled"})
	if err == nil || !strings.Contains(err.Error(), "after the task text") {
		t.Fatalf("err = %v, want a flag-after-task error", err)
	}
	if jobs, _ := st.List(); len(jobs) != 0 {
		t.Fatalf("a rejected add must not save a job, got %d", len(jobs))
	}
}

func TestScheduleAdd_DoubleDashAllowsDashTask(t *testing.T) {
	st, err := schedule.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduleAdd(st, []string{"--cron", "0 9 * * *", "--disabled", "--", "--verbose cleanup"}); err != nil {
		t.Fatal(err)
	}
	jobs, _ := st.List()
	if len(jobs) != 1 || jobs[0].Task != "--verbose cleanup" || jobs[0].Enabled {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestScheduleAdd_FlagsBeforeTaskStillApply(t *testing.T) {
	st, err := schedule.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduleAdd(st, []string{"--cron", "0 9 * * *", "--disabled", "--deliver", "log", "backup the notes"}); err != nil {
		t.Fatal(err)
	}
	jobs, _ := st.List()
	if len(jobs) != 1 || jobs[0].Task != "backup the notes" || jobs[0].Enabled {
		t.Fatalf("jobs = %+v", jobs)
	}
}
