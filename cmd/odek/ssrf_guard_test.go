package main

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

// recordingDial returns a dialFunc that records the addr it was asked to dial
// and never actually connects (returns nil, nil). Tests assert on *what* the
// guard decided to dial, not on a live connection.
func recordingDial(dialed *[]string) dialFunc {
	return func(_ context.Context, _ string, addr string) (net.Conn, error) {
		*dialed = append(*dialed, addr)
		return nil, nil
	}
}

func stubLookup(ips ...string) ipLookupFunc {
	return func(_ context.Context, _ string) ([]net.IPAddr, error) {
		out := make([]net.IPAddr, 0, len(ips))
		for _, s := range ips {
			out = append(out, net.IPAddr{IP: net.ParseIP(s)})
		}
		return out, nil
	}
}

func TestSSRFGuardedDial_ImplicitlyInternalDialedDirect(t *testing.T) {
	// A literal internal IP was already gated as SystemWrite by ClassifyURL;
	// the guard must dial it through unchanged without a DNS lookup (this is
	// what keeps httptest-on-127.0.0.1 tests working).
	var dialed []string
	lookupCalled := false
	lookup := func(_ context.Context, _ string) ([]net.IPAddr, error) {
		lookupCalled = true
		return nil, nil
	}
	guard := ssrfGuardedDial(recordingDial(&dialed), lookup)

	if _, err := guard(context.Background(), "tcp", "127.0.0.1:8080"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lookupCalled {
		t.Error("lookup must NOT be called for an implicitly-internal literal IP")
	}
	if len(dialed) != 1 || dialed[0] != "127.0.0.1:8080" {
		t.Errorf("dialed = %v, want [127.0.0.1:8080]", dialed)
	}
}

func TestSSRFGuardedDial_ErrorOmitsInternalIP(t *testing.T) {
	var dialed []string
	guard := ssrfGuardedDial(recordingDial(&dialed), stubLookup("10.0.0.1"))

	_, err := guard(context.Background(), "tcp", "evil.example.com:80")
	if err == nil {
		t.Fatal("expected refusal")
	}
	if strings.Contains(err.Error(), "10.0.0.1") {
		t.Errorf("error must not leak resolved IP: %v", err)
	}
}

func TestSSRFGuardedDial_ExternalResolvingInternalRefused(t *testing.T) {
	// The SSRF / rebinding core case: a host that presents as external but
	// resolves to an internal address must be refused, and no dial attempted.
	cases := []struct {
		name string
		ip   string
	}{
		{"cloud metadata", "169.254.169.254"},
		{"rfc1918", "10.1.2.3"},
		{"loopback", "127.0.0.1"},
		{"ipv6 ula", "fd00::1"},
		{"unspecified", "0.0.0.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var dialed []string
			guard := ssrfGuardedDial(recordingDial(&dialed), stubLookup(tc.ip))

			_, err := guard(context.Background(), "tcp", "evil.example.com:80")
			if err == nil {
				t.Fatal("expected the connection to be refused, got nil error")
			}
			if !strings.Contains(err.Error(), "internal address") {
				t.Errorf("error %q should mention the internal address", err)
			}
			if len(dialed) != 0 {
				t.Errorf("no dial must be attempted, dialed = %v", dialed)
			}
		})
	}
}

func TestSSRFGuardedDial_MixedAnswersFailClosed(t *testing.T) {
	// An answer set mixing a safe and an internal IP must be refused entirely —
	// an attacker cannot smuggle an internal target past the guard by padding
	// the DNS response with a public address.
	var dialed []string
	guard := ssrfGuardedDial(recordingDial(&dialed), stubLookup("93.184.216.34", "10.0.0.1"))

	if _, err := guard(context.Background(), "tcp", "evil.example.com:80"); err == nil {
		t.Fatal("expected refusal for mixed safe+internal answers")
	}
	if len(dialed) != 0 {
		t.Errorf("no dial must be attempted, dialed = %v", dialed)
	}
}

func TestSSRFGuardedDial_ExternalPinnedToValidatedIP(t *testing.T) {
	// A genuinely external host is dialed — but pinned to the validated IP, not
	// the hostname, so the kernel cannot re-resolve to a rebound address.
	var dialed []string
	guard := ssrfGuardedDial(recordingDial(&dialed), stubLookup("93.184.216.34"))

	if _, err := guard(context.Background(), "tcp", "example.com:443"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dialed) != 1 || dialed[0] != "93.184.216.34:443" {
		t.Errorf("dialed = %v, want [93.184.216.34:443] (pinned IP, not hostname)", dialed)
	}
}

func TestSSRFGuardedDial_AllowedHostPermitsInternal(t *testing.T) {
	// The configured web_search backend (e.g. SearXNG under Docker) may resolve
	// to a private container IP. An explicit allowlist lets it through while
	// still pinning the dial to the resolved IP.
	var dialed []string
	guard := ssrfGuardedDial(recordingDial(&dialed), stubLookup("172.18.0.3"), "searxng")

	if _, err := guard(context.Background(), "tcp", "searxng:8080"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dialed) != 1 || dialed[0] != "172.18.0.3:8080" {
		t.Errorf("dialed = %v, want [172.18.0.3:8080] (allowed internal backend pinned to IP)", dialed)
	}
}

func TestSSRFGuardedDial_AllowedHostPortIgnored(t *testing.T) {
	// The allowlist matches the hostname only; different ports on the same host
	// are still allowed.
	var dialed []string
	guard := ssrfGuardedDial(recordingDial(&dialed), stubLookup("172.18.0.3"), "searxng")

	if _, err := guard(context.Background(), "tcp", "searxng:9090"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dialed) != 1 || dialed[0] != "172.18.0.3:9090" {
		t.Errorf("dialed = %v, want [172.18.0.3:9090]", dialed)
	}
}

// TestBrowser_SSRF_ResolvesInternal exercises the guard through the real
// browser navigate path: the hostname classifies as NetworkEgress (so the
// policy gate lets it through) but resolves to the cloud-metadata IP, and the
// dial guard refuses it.
func TestBrowser_SSRF_ResolvesInternal(t *testing.T) {
	allow := "allow"
	b := &browserTool{
		state:           &browserState{nextRef: 1},
		dangerousConfig: danger.DangerousConfig{NonInteractive: &allow},
	}
	b.client = &http.Client{
		CheckRedirect: b.checkRedirect,
		Transport: &http.Transport{
			DialContext: ssrfGuardedDial((&net.Dialer{}).DialContext, stubLookup("169.254.169.254")),
		},
	}

	result := callJSON(t, b, `{"action":"navigate","url":"http://internal-disguised.example.com/"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" {
		t.Fatal("expected navigate to be blocked by the dial guard")
	}
	if !strings.Contains(r.Error, "internal address") && !strings.Contains(r.Error, "SSRF") {
		t.Errorf("error %q should explain the SSRF block", r.Error)
	}
}

// TestHTTPBatch_SSRF_ResolvesInternal exercises the guard through the real
// http_batch fetch path.
func TestHTTPBatch_SSRF_ResolvesInternal(t *testing.T) {
	tool := newHTTPBatchTool(danger.DangerousConfig{})
	tool.client = &http.Client{
		CheckRedirect: tool.checkRedirect,
		Transport: &http.Transport{
			DialContext: ssrfGuardedDial((&net.Dialer{}).DialContext, stubLookup("10.0.0.1")),
		},
	}

	result := callJSON(t, tool, `{"requests":[{"url":"http://internal-disguised.example.com/"}]}`)
	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) != 1 {
		t.Fatalf("Results = %d, want 1", len(r.Results))
	}
	if r.Results[0].Error == "" {
		t.Fatal("expected fetch to be blocked by the dial guard")
	}
}

// TestWebSearch_SSRF_ResolvesInternal exercises the guard through the real
// web_search query path. The configured base_url hostname classifies as external
// but resolves to the cloud-metadata IP; the dial guard must refuse it.
func TestWebSearch_SSRF_ResolvesInternal(t *testing.T) {
	tool := newWebSearchTool(allowAllDanger(), config.WebSearchConfig{BaseURL: "http://internal-disguised.example.com"})
	tool.client = &http.Client{
		Timeout:       tool.client.Timeout,
		CheckRedirect: tool.checkRedirect,
		Transport: &http.Transport{
			DialContext: ssrfGuardedDial((&net.Dialer{}).DialContext, stubLookup("169.254.169.254")),
		},
	}

	raw, _ := tool.Call(`{"query":"x"}`)
	out := decodeWebSearch(t, raw)
	if out.Error == "" {
		t.Fatal("expected web_search query to be blocked by the dial guard")
	}
	if !strings.Contains(out.Error, "internal address") && !strings.Contains(out.Error, "SSRF") {
		t.Errorf("error %q should explain the SSRF block", out.Error)
	}
}

// TestSSRFGuardedTransport_Installed is a guard against regressions that would
// silently drop the SSRF protection from the production constructors.
func TestSSRFGuardedTransport_RefusesProxy(t *testing.T) {
	// Replace the default transport with one whose Proxy function always returns
	// a proxy URL. This avoids depending on the test environment's proxy env
	// vars and proves the guard refuses proxy-routed requests regardless of how
	// the proxy was configured.
	orig := http.DefaultTransport
	http.DefaultTransport = &http.Transport{
		Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: "proxy.example.com:8080"}),
	}
	defer func() { http.DefaultTransport = orig }()

	tr := ssrfGuardedTransport()
	client := &http.Client{Transport: tr}

	resp, err := client.Get("http://example.com/")
	if err == nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatal("expected request to be refused when a proxy is configured")
	}
	if !strings.Contains(err.Error(), "refusing request through HTTP(S)_PROXY") {
		t.Errorf("error %q should mention proxy refusal", err)
	}
}

func TestSSRFGuardedTransport_Installed(t *testing.T) {
	b := newBrowserTool(danger.DangerousConfig{})
	if b.client.Transport == nil {
		t.Error("browser tool client has no Transport — SSRF guard not installed")
	}
	if tr, ok := b.client.Transport.(*http.Transport); !ok || tr.DialContext == nil {
		t.Error("browser tool Transport is missing the guarded DialContext")
	}

	h := newHTTPBatchTool(danger.DangerousConfig{})
	if h.client.Transport == nil {
		t.Error("http_batch tool client has no Transport — SSRF guard not installed")
	}
	if tr, ok := h.client.Transport.(*http.Transport); !ok || tr.DialContext == nil {
		t.Error("http_batch tool Transport is missing the guarded DialContext")
	}

	w := newWebSearchTool(danger.DangerousConfig{}, config.WebSearchConfig{})
	if w.client.Transport == nil {
		t.Error("web_search tool client has no Transport — SSRF guard not installed")
	}
	if tr, ok := w.client.Transport.(*http.Transport); !ok || tr.DialContext == nil {
		t.Error("web_search tool Transport is missing the guarded DialContext")
	}
}
