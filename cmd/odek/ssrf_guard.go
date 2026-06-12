package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

// SSRF / DNS-rebinding dial guard.
//
// danger.ClassifyURL inspects only the literal hostname of a URL, so a domain
// whose A/AAAA record points at 169.254.169.254 (cloud metadata), 10.x,
// 192.168.x, fd00::/8, etc. sails through the pre-request gate as plain
// NetworkEgress. With the default HTTP transport the kernel then resolves and
// connects with no second check — an SSRF. The same literal-only gate leaves a
// DNS-rebinding window: the address the policy saw and the address actually
// dialed can differ.
//
// ssrfGuardedDial closes both holes at the transport layer. For a host that
// presented as external it resolves the name itself, refuses the connection if
// ANY answer is an internal range (fail closed), and then pins the dial to an
// already-validated IP so the kernel cannot re-resolve to a rebound address.
// Because every redirect hop dials through the same transport, this also
// re-checks redirect targets without any per-tool redirect logic.

// dialFunc matches (*net.Dialer).DialContext and http.Transport.DialContext.
type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// ipLookupFunc matches (*net.Resolver).LookupIPAddr; injectable for testing.
type ipLookupFunc func(ctx context.Context, host string) ([]net.IPAddr, error)

// ssrfGuardedDial wraps a base dial function with a post-resolution IP check.
// Hosts that are already internal by inspection (a literal internal IP or a
// known-internal hostname) are dialed through unchanged: ClassifyURL already
// surfaced them to the policy gate as SystemWrite, so honouring that decision
// here preserves explicitly-allowed localhost access (and keeps httptest-backed
// tests working). Every other host is resolved via lookup and validated.
func ssrfGuardedDial(base dialFunc, lookup ipLookupFunc) dialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}

		if danger.HostIsImplicitlyInternal(host) {
			return base(ctx, network, addr)
		}

		ips, err := lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no addresses found for %q", host)
		}
		// Validate every answer before dialing any of them: an attacker can
		// return a safe IP alongside an internal one hoping the dialer picks
		// the internal address. Refuse the whole set if any is internal.
		for _, ipa := range ips {
			if danger.IsBlockedIP(ipa.IP) {
				return nil, fmt.Errorf("blocked connection to %q: resolves to internal address %s (possible SSRF / DNS rebinding)", host, ipa.IP)
			}
		}
		// Pin to validated IPs — never hand the hostname back to the kernel,
		// which would resolve a second time and reopen the rebinding window.
		// Try each in order so a single unreachable answer still fails over.
		var dialErr error
		for _, ipa := range ips {
			conn, err := base(ctx, network, net.JoinHostPort(ipa.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			dialErr = err
		}
		return nil, dialErr
	}
}

// ssrfGuardedTransport returns an *http.Transport whose DialContext is the SSRF
// guard above, backed by the real dialer and resolver. It clones the default
// transport when possible so it inherits sane defaults (env proxy handling,
// idle-conn limits, HTTP/2, TLS handshake timeout); if a third-party package
// has swapped http.DefaultTransport for a non-*http.Transport RoundTripper, it
// falls back to a fresh transport with explicit proxy handling rather than
// panicking on the type assertion — this runs at startup, so it must fail safe.
func ssrfGuardedTransport() *http.Transport {
	base := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	tr, ok := http.DefaultTransport.(*http.Transport)
	if ok {
		tr = tr.Clone()
	} else {
		tr = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	tr.DialContext = ssrfGuardedDial(base.DialContext, net.DefaultResolver.LookupIPAddr)
	return tr
}
