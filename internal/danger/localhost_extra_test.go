package danger

import "testing"

func TestHostnameInternalLocalhostForms(t *testing.T) {
	for _, h := range []string{"localhost.", "LOCALHOST", "x.localhost", "x.y.localhost.", "foo.local.", "metadata.google.internal."} {
		if !hostnameIsInternal(h) {
			t.Errorf("hostnameIsInternal(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"notlocalhost", "localhost.example.com", "localhostx.com", "example.com", "."} {
		if hostnameIsInternal(h) {
			t.Errorf("hostnameIsInternal(%q) = true, want false", h)
		}
	}
	if got := ClassifyURL("http://localhost./admin"); got != SystemWrite {
		t.Errorf("ClassifyURL(localhost.) = %s, want system_write", got)
	}
	if got := ClassifyURL("http://localhost.example.com/"); got == SystemWrite {
		t.Errorf("a public name that starts with localhost is not loopback, got %s", got)
	}
}
