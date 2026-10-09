package danger

import "testing"

// tar's long --file=PATH spelling is not path-classified while -f PATH and
// --file PATH are.
func TestRED_TarLongFileEqualsTarget(t *testing.T) {
	if got := Classify("tar --file /etc/x.tar -c x"); got != SystemWrite {
		t.Fatalf("control failed: %s", got)
	}
	if got := Classify("tar --file=/etc/x.tar -c x"); got != SystemWrite {
		t.Errorf("tar --file=/etc/x.tar -c writes /etc, got %s want system_write", got)
	}
	if got := Classify("tar --file=$HOME/.bashrc -c x"); Rank(got) < Rank(Persistence) {
		t.Errorf("tar --file=~/.bashrc overwrites a shell rc file, got %s", got)
	}
}

// tar --remove-files deletes the archived sources, like rsync
// --remove-source-files.
func TestRED_TarRemoveFilesDestructive(t *testing.T) {
	if got := Classify("tar -cf a.tar --remove-files ."); Rank(got) < Rank(Destructive) {
		t.Errorf("tar --remove-files deletes sources, got %s want destructive", got)
	}
}
