package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

// proxyWarnOnce ensures the proxy-disabled warning is logged only once per
// process even though ssrfGuardedTransport is called for several tools.
var proxyWarnOnce sync.Once

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
//
// allowedHosts lists hostnames (without ports) that the operator explicitly
// trusts, e.g. the configured web_search base_url. These hosts bypass the
// internal-IP block so that container-internal services such as SearXNG can be
// reached by name, while still being pinned to the resolved IP so the kernel
// cannot re-resolve to a rebound address.
func ssrfGuardedDial(base dialFunc, lookup ipLookupFunc, allowedHosts ...string) dialFunc {
	allowed := make(map[string]struct{}, len(allowedHosts))
	for _, h := range allowedHosts {
		if h != "" {
			allowed[h] = struct{}{}
		}
	}

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}

		if danger.HostIsImplicitlyInternal(host) {
			return base(ctx, network, addr)
		}

		_, hostAllowed := allowed[host]

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
		// Operator-allowed hosts bypass this check so configured internal
		// backends (e.g. SearXNG under Docker) remain reachable.
		if !hostAllowed {
			for _, ipa := range ips {
				if danger.IsBlockedIP(ipa.IP) {
					// Do not include the resolved IP in the error: it would leak an
					// internal DNS oracle to the model/audit log.
					return nil, fmt.Errorf("blocked connection to %q: resolves to an internal address (possible SSRF / DNS rebinding)", host)
				}
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
// guard above, backed by the real dialer and resolver. allowedHosts are
// operator-trusted hostnames (e.g. the web_search base_url host) that may
// resolve to internal addresses. It clones the default transport when possible
// so it inherits sane defaults (env proxy handling, idle-conn limits, HTTP/2,
// TLS handshake timeout); if a third-party package has swapped
// http.DefaultTransport for a non-*http.Transport RoundTripper, it falls back
// to a fresh transport with explicit proxy handling rather than panicking on
// the type assertion — this runs at startup, so it must fail safe.
func ssrfGuardedTransport(allowedHosts ...string) *http.Transport {
	base := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	tr, ok := http.DefaultTransport.(*http.Transport)
	if ok {
		tr = tr.Clone()
	} else {
		tr = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}

	// Proxies bypass the dial-layer SSRF guard: the transport dials the proxy,
	// and the target address is sent inside the CONNECT/request envelope. An
	// attacker-controlled proxy (or a rebinding target reachable through a
	// legitimate proxy) would therefore defeat the guard. Fail closed: if a proxy
	// is configured for a request, refuse it rather than silently void protection.
	if tr.Proxy != nil {
		if proxyEnvSet() {
			proxyWarnOnce.Do(func() {
				fmt.Fprintf(os.Stderr, "warning: HTTP(S)_PROXY is set but odek's SSRF guard cannot validate target addresses through a proxy; proxy routing is disabled for outbound tool traffic\n")
			})
		}
		originalProxy := tr.Proxy
		tr.Proxy = func(req *http.Request) (*url.URL, error) {
			proxyURL, err := originalProxy(req)
			if err != nil {
				return nil, err
			}
			if proxyURL != nil {
				return nil, fmt.Errorf("refusing request through HTTP(S)_PROXY: SSRF guard cannot validate target address via a proxy")
			}
			return nil, nil
		}
	}

	tr.DialContext = ssrfGuardedDial(base.DialContext, net.DefaultResolver.LookupIPAddr, allowedHosts...)
	return tr
}

// proxyEnvSet reports whether any of the standard HTTP(S)_PROXY environment
// variables are set, so callers can warn that outbound proxying is disabled.
func proxyEnvSet() bool {
	for _, v := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "no_proxy"} {
		if os.Getenv(v) != "" {
			return true
		}
	}
	return false
}
