package danger

import (
	"net"
	"testing"
)

// IPv6 forms that carry an IPv4 address (NAT64, 6to4) and the 0.0.0.0/8
// "this network" block alias internal targets on networks that translate or
// route them, so the embedded address decides.
func TestIsBlockedIP_EmbeddedAndThisNetwork(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		// 0.0.0.0/8
		{"0.1.2.3", true},
		{"0.255.255.255", true},
		{"::ffff:0.1.2.3", true},
		{"1.0.0.1", false},
		// NAT64 64:ff9b::/96
		{"64:ff9b::7f00:1", true},     // 127.0.0.1
		{"64:ff9b::a9fe:a9fe", true},  // 169.254.169.254
		{"64:ff9b::a00:1", true},      // 10.0.0.1
		{"64:ff9b::c0a8:101", true},   // 192.168.1.1
		{"64:ff9b::6440:1", true},     // 100.64.0.1 (CGNAT)
		{"64:ff9b::808:808", false},   // 8.8.8.8
		{"64:ff9b::5db8:d822", false}, // 93.184.216.34
		// Local-use NAT64 64:ff9b:1::/48
		{"64:ff9b:1::1", true},
		{"64:ff9b:1:2:3:4:5:6", true},
		// 6to4 2002::/16
		{"2002:7f00:1::1", true},     // 127.0.0.1
		{"2002:a9fe:a9fe::1", true},  // 169.254.169.254
		{"2002:c0a8:101::1", true},   // 192.168.1.1
		{"2002:808:808::1", false},   // 8.8.8.8
		{"2002:5db8:d822::1", false}, // 93.184.216.34
		// Unrelated global IPv6 stays reachable
		{"2606:4700:4700::1111", false},
		{"2001:4860:4860::8888", false},
		{"64:ff9a::7f00:1", false}, // not the NAT64 prefix
		{"64:ff9b::1:7f00:1", false},
	}
	for _, tc := range tests {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("bad test ip %q", tc.ip)
		}
		if got := IsBlockedIP(ip); got != tc.want {
			t.Errorf("IsBlockedIP(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func TestIsBlockedIP_FourByteSlice(t *testing.T) {
	if !IsBlockedIP(net.IP{0, 9, 9, 9}) {
		t.Error("4-byte 0.9.9.9 must be blocked")
	}
	if IsBlockedIP(net.IP{8, 8, 8, 8}) {
		t.Error("4-byte 8.8.8.8 must not be blocked")
	}
}
