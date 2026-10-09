package danger

import "testing"

// --remove-sent-files is rsync's older spelling of --remove-source-files,
// which the classifier already treats as destructive.
func TestRED_RsyncRemoveSentFiles(t *testing.T) {
	if got := Classify("rsync -a --remove-source-files a b"); got != Destructive {
		t.Fatalf("control failed: %s", got)
	}
	if got := Classify("rsync -a --remove-sent-files a b"); got != Destructive {
		t.Errorf("rsync --remove-sent-files deletes the sources, got %s want destructive", got)
	}
}

// rsync --write-batch / --log-file write an arbitrary local file.
func TestRED_RsyncWriteBatchTarget(t *testing.T) {
	for _, c := range []string{
		"rsync -a --write-batch=/etc/cron.d/x a b",
		"rsync -a --only-write-batch=/etc/x a b",
		"rsync -a --log-file=/etc/x a b",
	} {
		if got := Classify(c); Rank(got) < Rank(SystemWrite) {
			t.Errorf("%s writes under /etc, got %s", c, got)
		}
	}
}
