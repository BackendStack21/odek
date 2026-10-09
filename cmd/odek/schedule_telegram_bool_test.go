package main

import "testing"

func TestParseTelegramScheduleAdd_BoolOptions(t *testing.T) {
	cases := []struct {
		opts        string
		wantEnabled bool
		wantCatchup bool
		wantErr     bool
	}{
		{"", true, false, false},
		{"catchup", true, true, false},
		{"disabled", false, false, false},
		{"catchup=true disabled=true", false, true, false},
		{"catchup=TRUE disabled=1", false, true, false},
		{"catchup=off disabled=no", true, false, false},
		{"disabled=maybe", false, false, true},
		{"catchup=2", false, false, true},
	}
	for _, tc := range cases {
		job, msg := parseTelegramScheduleAdd(42, "0 9 * * * Stand-up | "+tc.opts)
		if tc.wantErr {
			if msg == "" {
				t.Errorf("%q: expected an error message", tc.opts)
			}
			continue
		}
		if msg != "" {
			t.Errorf("%q: unexpected error %s", tc.opts, msg)
			continue
		}
		if job.Enabled != tc.wantEnabled || job.Catchup != tc.wantCatchup {
			t.Errorf("%q: enabled=%v catchup=%v, want %v/%v", tc.opts, job.Enabled, job.Catchup, tc.wantEnabled, tc.wantCatchup)
		}
	}
}
