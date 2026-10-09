package main

import (
	"context"
	"net"
	"testing"
)

// A DNS answer that embeds the cloud-metadata IPv4 address inside a NAT64
// (64:ff9b::/96) or 6to4 (2002::/16) prefix is routed to 169.254.169.254 on
// NAT64 / 6to4 networks, but the dial guard only blocks the literal IPv4
// ranges and so lets the connection through.
func TestRED_SSRFDialGuardAllowsEmbeddedMetadataIPv6(t *testing.T) {
	for _, s := range []string{"64:ff9b::a9fe:a9fe", "2002:a9fe:a9fe::1", "64:ff9b::7f00:1"} {
		dialed := false
		base := func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed = true
			c, _ := net.Pipe()
			return c, nil
		}
		lookup := func(ctx context.Context, host string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP(s)}}, nil
		}
		conn, err := ssrfGuardedDial(base, lookup)(context.Background(), "tcp", "rebind.example.com:80")
		if conn != nil {
			conn.Close()
		}
		if err == nil || dialed {
			t.Errorf("guard dialed %s (embeds an internal IPv4) instead of refusing", s)
		}
	}
}
