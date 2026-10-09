package danger

import "testing"

// The superuser's home is /root on Linux and /var/root on macOS (where ~root
// expands to it): its shell rc files are persistence targets on both.
func TestSuperuserHomeRCFilesArePersistence(t *testing.T) {
	for _, p := range []string{"/root/.bashrc", "/var/root/.bashrc", "/private/var/root/.zshrc"} {
		if !IsPersistencePath(p) {
			t.Errorf("IsPersistencePath(%q) = false, want true", p)
		}
	}
	if IsPersistencePath("/var/rootkit/.bashrc") {
		t.Error("/var/rootkit is not the superuser home")
	}
	homes := accountHomes("/var/root/.ssh/id_rsa")
	found := false
	for _, h := range homes {
		if h == "/var/root" {
			found = true
		}
	}
	if !found {
		t.Errorf("accountHomes = %q, want /var/root among them", homes)
	}
	if got := Classify("cp x ~root/.bashrc"); Rank(got) < Rank(SystemWrite) {
		t.Errorf("cp into root's rc file = %s, want at least system_write", got)
	}
}
