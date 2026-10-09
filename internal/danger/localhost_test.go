package danger

import "testing"

// RFC 6761: every name under .localhost resolves to loopback (systemd-resolved,
// browsers, curl). ClassifyURL only recognises the bare name and the
// localhost6 aliases, so a subdomain reaches the loopback services as plain
// network_egress.
func TestRED_ClassifyURLLocalhostSubdomain(t *testing.T) {
	for _, u := range []string{
		"http://foo.localhost/",
		"http://admin.localhost:8080/api",
		"http://a.b.LOCALHOST./",
	} {
		if got := ClassifyURL(u); got != SystemWrite {
			t.Errorf("ClassifyURL(%q) = %s, want system_write (loopback)", u, got)
		}
	}
	if !HostIsImplicitlyInternal("foo.localhost") {
		t.Error("HostIsImplicitlyInternal(foo.localhost) = false, want true")
	}
}
