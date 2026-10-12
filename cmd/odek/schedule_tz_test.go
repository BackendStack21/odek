package main

import (
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/schedule"
)

// The default zone is resolved by the caller once and applied to every
// job without its own timezone; jobSchedule itself reads no configuration.
func TestJobSchedule_UsesPassedDefaultZone(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	job := schedule.Job{Cron: "0 9 * * *"}
	utc, err := jobSchedule(job, nil)
	if err != nil {
		t.Fatal(err)
	}
	jp, err := jobSchedule(job, tokyo)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if utc.Next(now).Equal(jp.Next(now)) {
		t.Fatal("default zone was ignored")
	}
	own, _ := jobSchedule(schedule.Job{Cron: "0 9 * * *", Timezone: "UTC"}, tokyo)
	if !own.Next(now).Equal(utc.Next(now)) {
		t.Fatal("a job's own timezone must win over the default")
	}
}
