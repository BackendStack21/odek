package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
