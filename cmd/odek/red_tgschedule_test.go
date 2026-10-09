package main

import "testing"

func TestRED_TelegramScheduleAddBoolOptFalse(t *testing.T) {
	job, errMsg := parseTelegramScheduleAdd(42, "0 9 * * * Stand-up | disabled=false catchup=false")
	if errMsg != "" {
		t.Fatalf("unexpected error: %s", errMsg)
	}
	if !job.Enabled {
		t.Error("disabled=false produced a DISABLED job")
	}
	if job.Catchup {
		t.Error("catchup=false produced a catchup-enabled job")
	}
}
