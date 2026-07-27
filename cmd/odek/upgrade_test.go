package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		current, latest string
		want            int
	}{
		{"v1.0.0", "v1.0.0", 0},
		{"v1.0.0", "v1.0.1", -1},
		{"v1.0.1", "v1.0.0", 1},
		{"v1.15.10", "v1.16.0", -1},
		{"v2.0.0", "v1.99.99", 1},
		{"1.2.3", "v1.2.3", 0},     // missing v prefix tolerated
		{"dev", "v1.16.0", -1},     // local dev build is always older
		{"c7699de", "v1.16.0", -1}, // commit-hash version is always older
		{"v1.0", "v1.0.0", -1},     // unparseable current treated as older
	}
	for _, tc := range cases {
		if got := compareVersions(tc.current, tc.latest); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.current, tc.latest, got, tc.want)
		}
	}
}

func TestParseChecksums(t *testing.T) {
	data := []byte("e04c760815c30bfe320482bee1e718abaa3088f8726d6cd313b677ec59313bfe  odek-darwin-amd64\n" +
		"36bdb1490e48aa35a4fc104190b5bc1e64e1f99c565b46a8f44f61c575493778  odek-darwin-arm64\n" +
		"garbage line\n")
	got := parseChecksums(data)
	if len(got) != 2 {
		t.Fatalf("parseChecksums returned %d entries, want 2: %v", len(got), got)
	}
	if got["odek-darwin-amd64"] != "e04c760815c30bfe320482bee1e718abaa3088f8726d6cd313b677ec59313bfe" {
		t.Errorf("wrong hash for odek-darwin-amd64: %q", got["odek-darwin-amd64"])
	}
}

func TestUpgradeAssetName(t *testing.T) {
	name, err := upgradeAssetName()
	switch runtime.GOOS {
	case "darwin", "linux":
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "odek-" + runtime.GOOS + "-" + runtime.GOARCH
		if name != want {
			t.Errorf("upgradeAssetName() = %q, want %q", name, want)
		}
	default:
		if err == nil {
			t.Errorf("expected unsupported-platform error on %s/%s, got asset %q", runtime.GOOS, runtime.GOARCH, name)
		}
	}
}

// fakeReleaseServer serves a latest-release JSON payload plus asset and
// checksums.txt downloads for the current platform's asset name.
func fakeReleaseServer(t *testing.T, tag string, binContent []byte, sumOverride string) *httptest.Server {
	t.Helper()
	asset, err := upgradeAssetName()
	if err != nil {
		t.Skipf("no prebuilt asset for this platform: %v", err)
	}
	sum := sumOverride
	if sum == "" {
		h := sha256.Sum256(binContent)
		sum = hex.EncodeToString(h[:])
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	mux.HandleFunc("/repos/"+githubRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[`+
			`{"name":%q,"browser_download_url":%q},`+
			`{"name":"checksums.txt","browser_download_url":%q}]}`,
			tag, asset, srv.URL+"/dl/"+asset, srv.URL+"/dl/checksums.txt")
	})
	mux.HandleFunc("/dl/"+asset, func(w http.ResponseWriter, r *http.Request) {
		w.Write(binContent)
	})
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", sum, asset)
	})
	t.Cleanup(srv.Close)
	return srv
}

func TestPerformUpgrade_EndToEnd(t *testing.T) {
	newBin := []byte("#!/bin/sh\necho odek v9.9.9\n")
	srv := fakeReleaseServer(t, "v9.9.9", newBin, "")

	dir := t.TempDir()
	exe := filepath.Join(dir, "odek")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	got, err := performUpgrade(context.Background(), srv.Client(), srv.URL, exe, "v1.0.0", false, &out)
	if err != nil {
		t.Fatalf("performUpgrade: %v", err)
	}
	if got != "v9.9.9" {
		t.Errorf("installed version = %q, want v9.9.9", got)
	}
	content, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, newBin) {
		t.Errorf("binary not replaced; got %q", content)
	}
	fi, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o, want 755", fi.Mode().Perm())
	}
	if !strings.Contains(out.String(), "upgraded v1.0.0 → v9.9.9") {
		t.Errorf("unexpected output: %q", out.String())
	}
}

func TestPerformUpgrade_UpToDate(t *testing.T) {
	srv := fakeReleaseServer(t, "v1.16.0", []byte("bin"), "")
	exe := filepath.Join(t.TempDir(), "odek")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	got, err := performUpgrade(context.Background(), srv.Client(), srv.URL, exe, "v1.16.0", false, &out)
	if err != nil {
		t.Fatalf("performUpgrade: %v", err)
	}
	if got != "" {
		t.Errorf("returned %q, want empty (no upgrade)", got)
	}
	if !strings.Contains(out.String(), "up to date") {
		t.Errorf("unexpected output: %q", out.String())
	}
}

func TestPerformUpgrade_CheckOnly(t *testing.T) {
	srv := fakeReleaseServer(t, "v9.9.9", []byte("bin"), "")
	exe := filepath.Join(t.TempDir(), "odek")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	got, err := performUpgrade(context.Background(), srv.Client(), srv.URL, exe, "v1.0.0", true, &out)
	if err != nil {
		t.Fatalf("performUpgrade: %v", err)
	}
	if got != "v9.9.9" {
		t.Errorf("returned %q, want v9.9.9", got)
	}
	content, _ := os.ReadFile(exe)
	if string(content) != "old" {
		t.Errorf("--check modified the binary: %q", content)
	}
	if !strings.Contains(out.String(), "upgrade available: v1.0.0 → v9.9.9") {
		t.Errorf("unexpected output: %q", out.String())
	}
}

func TestPerformUpgrade_ChecksumMismatch(t *testing.T) {
	bad := strings.Repeat("0", 64)
	srv := fakeReleaseServer(t, "v9.9.9", []byte("bin"), bad)
	exe := filepath.Join(t.TempDir(), "odek")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_, err := performUpgrade(context.Background(), srv.Client(), srv.URL, exe, "v1.0.0", false, &out)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got %v", err)
	}
	content, _ := os.ReadFile(exe)
	if string(content) != "old" {
		t.Errorf("binary modified despite checksum mismatch: %q", content)
	}
}

func TestFetchLatestRelease(t *testing.T) {
	srv := fakeReleaseServer(t, "v1.16.0", []byte("bin"), "")
	rel, err := fetchLatestRelease(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("fetchLatestRelease: %v", err)
	}
	if rel.TagName != "v1.16.0" {
		t.Errorf("tag = %q, want v1.16.0", rel.TagName)
	}
	if rel.assetURL("checksums.txt") == "" {
		t.Error("checksums.txt asset URL missing")
	}
}
