package danger

import "testing"

func TestKillBroadcast_MatchEverythingAndUserSelectors(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	rhAtLeast(t, SystemWrite,
		`pkill -f "\w"`,
		`pkill -f "\S"`,
		`pkill -f /`,
		`pkill -f " "`,
		`killall -u alice`,
		`pkill -u alice`,
		`killall --user alice`,
		`pkill -9 -f "\w"`,
	)
	for _, cmd := range []string{
		`pkill -f "\."`,
		`pkill -f myserver`,
		`pkill -f "node server.js"`,
		`pkill -f ^/usr/bin/foo`,
		`pkill -u alice sleep`,
		`killall -u alice firefox`,
		`pkill -9 node`,
		`killall myserver`,
	} {
		if got := Classify(cmd); Rank(got) >= Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want below system_write", cmd, got)
		}
	}
}
