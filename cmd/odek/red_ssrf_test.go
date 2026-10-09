package main

import (
	"context"
	"net"
	"testing"
)

// A hostname whose AAAA record is a NAT64 / 6to4 / 0.0.0.0/8 address that
// embeds or aliases an internal IPv4 target must be refused by the dial guard.
func TestRED_SSRFGuard_BlocksEmbeddedInternalForms(t *testing.T) {
	cases := map[string]string{
		"nat64-loopback": "64:ff9b::7f00:1",
		"nat64-metadata": "64:ff9b::a9fe:a9fe",
		"6to4-loopback":  "2002:7f00:1::1",
		"this-network":   "0.1.2.3",
	}
	for name, ipStr := range cases {
		t.Run(name, func(t *testing.T) {
			ip := net.ParseIP(ipStr)
			dialed := false
			base := func(ctx context.Context, network, addr string) (net.Conn, error) {
				dialed = true
				c, _ := net.Pipe()
				return c, nil
			}
			lookup := func(ctx context.Context, host string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: ip}}, nil
			}
			d := ssrfGuardedDial(base, lookup)
			conn, err := d(context.Background(), "tcp", "evil.example.com:80")
			if conn != nil {
				conn.Close()
			}
			if dialed || err == nil {
				t.Fatalf("guard dialed %s (internal alias) for external-looking host", ipStr)
			}
		})
	}
}
