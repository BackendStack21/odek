package main

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/schedule"
)

func TestRED_ScheduleAddFlagAfterTaskFoldedIntoTask(t *testing.T) {
	st, err := schedule.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = scheduleAdd(st, []string{"--cron", "0 9 * * *", "backup the notes", "--disabled", "--deliver", "log"})
	jobs, lerr := st.List()
	if lerr != nil {
		t.Fatal(lerr)
	}
	if err == nil && len(jobs) == 1 && strings.Contains(jobs[0].Task, "--") {
		t.Fatalf("trailing flags were folded into the job task text and never applied: task=%q enabled=%v deliver=%q",
			jobs[0].Task, jobs[0].Enabled, jobs[0].Deliver.Kind)
	}
}
