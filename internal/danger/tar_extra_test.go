package danger

import "testing"

func TestTarArchiveFileIsWriteTargetOnlyWhenWriting(t *testing.T) {
	for _, cmd := range []string{
		"tar --file=/etc/x.tar -c x",
		"tar --fi=/etc/x.tar --create x",
		"tar -rf /etc/x.tar x",
		"tar -uf /etc/x.tar x",
		"tar --file /etc/x.tar --append x",
	} {
		if got := Classify(cmd); Rank(got) < Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want at least system_write", cmd, got)
		}
	}
	for _, cmd := range []string{
		"tar -tf x.tar",
		"tar --file=x.tar -x",
		"tar --file=x.tar --list",
	} {
		if got := Classify(cmd); Rank(got) > Rank(LocalWrite) {
			t.Errorf("Classify(%q) = %s, want at most local_write", cmd, got)
		}
	}
	for _, cmd := range []string{
		"tar -czf out.tgz --remove-files dir",
		"tar -cf out.tar --remove-f dir",
	} {
		if got := Classify(cmd); Rank(got) < Rank(Destructive) {
			t.Errorf("Classify(%q) = %s, want destructive", cmd, got)
		}
	}
	if got := Classify("tar -cf out.tar dir"); got != LocalWrite {
		t.Errorf("a plain local archive stays local_write, got %s", got)
	}
}
