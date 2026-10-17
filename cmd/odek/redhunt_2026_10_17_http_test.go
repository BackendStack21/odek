package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The embedded UI directive (//go:embed ui) includes the frontend unit tests
// (ui/js/*.test.js), and the /js/ route serves any embedded *.js name, so the
// production server ships and serves test files that are not part of the UI.
func TestRED_HTTPStaticServesFrontendTestFiles(t *testing.T) {
	handler := handleStatic("tok")
	r := httptest.NewRequest(http.MethodGet, "/js/api.test.js", nil)
	w := httptest.NewRecorder()
	handler(w, r)
	if w.Code == http.StatusOK {
		t.Fatalf("GET /js/api.test.js = 200 (%d bytes); frontend test files must not be embedded or served", w.Body.Len())
	}
}

// openInBrowser must run an opener resolved from a trusted directory by
// absolute path, never a name resolved against the current directory (the
// workspace), and must actually launch the opener it found.
func TestRED_HTTPOpenBrowserExecutesWorkspaceBinary(t *testing.T) {
	ws := t.TempDir()
	trusted := t.TempDir()
	plantedMarker := filepath.Join(ws, "planted-ran")
	trustedMarker := filepath.Join(trusted, "trusted-ran")
	for _, c := range []string{"xdg-open", "open", "gnome-open"} {
		planted := "#!/bin/sh\necho \"$1\" > '" + plantedMarker + "'\n"
		if err := os.WriteFile(filepath.Join(ws, c), []byte(planted), 0o755); err != nil {
			t.Fatal(err)
		}
		real := "#!/bin/sh\necho \"$1\" > '" + trustedMarker + "'\n"
		if err := os.WriteFile(filepath.Join(trusted, c), []byte(real), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := browserOpenerDirs
	browserOpenerDirs = []string{trusted}
	t.Cleanup(func() { browserOpenerDirs = old })
	t.Chdir(ws)
	openInBrowser("http://127.0.0.1:1/?token=secret-token")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(trustedMarker); err == nil && len(b) > 0 {
			if _, err := os.Stat(plantedMarker); err == nil {
				t.Fatal("workspace-planted opener also executed")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	if b, err := os.ReadFile(plantedMarker); err == nil && len(b) > 0 {
		t.Fatalf("serve --open executed the workspace-planted opener with argv %q", strings.TrimSpace(string(b)))
	}
	t.Fatal("serve --open never launched the opener from the trusted directory")
}

func TestRED_HTTPFindBrowserOpenerIgnoresRelativeAndMissingDirs(t *testing.T) {
	old := browserOpenerDirs
	t.Cleanup(func() { browserOpenerDirs = old })
	ws := t.TempDir()
	for _, c := range []string{"xdg-open", "open", "gnome-open"} {
		if err := os.WriteFile(filepath.Join(ws, c), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(ws)
	browserOpenerDirs = []string{".", "", "relative/dir", filepath.Join(ws, "missing")}
	if p := findBrowserOpener(); p != "" {
		t.Fatalf("findBrowserOpener resolved %q from a relative or missing directory", p)
	}
	openInBrowser("http://127.0.0.1:1/") // must be a harmless no-op
}

// With a wildcard --addr (0.0.0.0:<port>, ":<port>" — the docker-compose
// setup uses 0.0.0.0:8080), serve prints http://0.0.0.0:<port>/?token=... (or
// http://[::]:...). That URL is unusable: requireLocalHost rejects Host
// "0.0.0.0" on every /api route and the WebSocket origin check rejects
// Origin http://0.0.0.0, so a user (or --open) following the printed URL
// gets a UI that cannot do anything. The printed URL must name a loopback
// host the server accepts.
func TestRED_HTTPWildcardBindPrintsUnusableTokenURL(t *testing.T) {
	t.Cleanup(func() {
		serveShutdownOnce = sync.Once{}
		serveShutdownCh = make(chan struct{})
		resetServeRuns()
	})
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".odek"), 0o700); err != nil {
		t.Fatal(err)
	}

	origStderr := os.Stderr
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = pw
	urlCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 1024)
		sent := false
		for {
			n, rerr := pr.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if !sent {
				if i := strings.Index(string(buf), "http://"); i >= 0 {
					rest := string(buf[i:])
					if j := strings.IndexByte(rest, '\n'); j >= 0 {
						urlCh <- strings.TrimSpace(rest[:j])
						sent = true
					}
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- serveCmd([]string{"--addr", "0.0.0.0:0", "--no-sandbox"}) }()

	var tokenURL string
	select {
	case tokenURL = <-urlCh:
	case err := <-done:
		os.Stderr = origStderr
		t.Fatalf("serveCmd returned early: %v", err)
	case <-time.After(15 * time.Second):
		os.Stderr = origStderr
		t.Fatal("serve did not print its URL")
	}
	defer func() {
		requestServeShutdown()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
		}
		os.Stderr = origStderr
		pw.Close()
	}()

	i := strings.Index(tokenURL, "/?token=")
	if i < 0 {
		t.Fatalf("unexpected token URL %q", tokenURL)
	}
	base, token := tokenURL[:i], tokenURL[i+len("/?token="):]
	req, _ := http.NewRequest(http.MethodGet, base+"/api/health", nil)
	req.Header.Set("X-Odek-Ws-Token", token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("printed URL %q unreachable: %v", tokenURL, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("printed token URL %q cannot reach the API: GET /api/health with the token = %d %q", tokenURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

func TestRED_HTTPServeDisplayAddr(t *testing.T) {
	for _, tc := range []struct {
		in   *net.TCPAddr
		want string
	}{
		{&net.TCPAddr{IP: net.IPv4zero, Port: 8080}, "127.0.0.1:8080"},
		{&net.TCPAddr{IP: net.IPv6unspecified, Port: 9}, "127.0.0.1:9"},
		{&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 7}, "127.0.0.1:7"},
		{&net.TCPAddr{IP: net.ParseIP("::1"), Port: 7}, "[::1]:7"},
	} {
		if got := serveDisplayAddr(tc.in); got != tc.want {
			t.Errorf("serveDisplayAddr(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
