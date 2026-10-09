package danger

import "testing"

func TestRsyncLocalFileOptionsAreWriteTargets(t *testing.T) {
	for _, cmd := range []string{
		"rsync -a --log-file /etc/rsync.log a b",
		"rsync -a --write-batch /etc/batch a b",
		"rsync -a --write-b=/etc/batch a b",
		"rsync -a --only-write-batch=/usr/local/x a b",
	} {
		if got := Classify(cmd); Rank(got) < Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want at least system_write", cmd, got)
		}
	}
	for _, cmd := range []string{
		"rsync -a --log-file=run.log a b",
		"rsync -a --write-batch=batch.out a b",
		"rsync -a a b",
	} {
		if got := Classify(cmd); Rank(got) > Rank(LocalWrite) {
			t.Errorf("Classify(%q) = %s, want at most local_write", cmd, got)
		}
	}
	if got := Classify("rsync -a --remove-sent-files --dry-run a b"); Rank(got) < Rank(Destructive) {
		t.Errorf("remove-sent-files stays destructive even with a dry run flag spelled elsewhere, got %s", got)
	}
}
