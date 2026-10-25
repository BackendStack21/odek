package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/maintenance"
)

// rtopsHome points HOME at a fresh temp dir so dispatch-level tests never
// touch the real ~/.odek.
func rtopsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestRED_RtOps_KeyFDStrictParse(t *testing.T) {
	for _, bad := range []string{"3x", "3 ", " 3", "03x", "+3", "3.0", "0x3"} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.WriteString("secret-key\n"); err != nil {
			t.Fatal(err)
		}
		w.Close()
		// A dup of r: if a malformed value were ever accepted, the reader
		// would close its own descriptor, never one r still owns.
		dup := dupFD(t, r)
		fd, _ := strconv.Atoi(dup)
		val := dup
		if bad == "3x" || bad == "03x" {
			val += "x"
		} else if bad == "+3" {
			val = "+" + val
		} else if bad == "3 " {
			val += " "
		} else if bad == " 3" {
			val = " " + val
		} else if bad == "3.0" {
			val += ".0"
		} else {
			val = "0x" + fmt.Sprintf("%x", fd)
		}
		t.Setenv(keyFDEnvVar, val)
		if got := readKeyFromInheritedFD(); got != "" {
			t.Errorf("env %q accepted, key %q", val, got)
		} else {
			// Rejected before it was opened: the dup is still ours to close.
			os.NewFile(uintptr(fd), "rejected-key-fd").Close()
		}
		r.Close()
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString("secret-key\n")
	w.Close()
	defer r.Close()
	// The reader closes the descriptor it is handed, so it gets a dup: r
	// keeps its own, and its finalizer never closes a reused number.
	t.Setenv(keyFDEnvVar, dupFD(t, r))
	if got := readKeyFromInheritedFD(); got != "secret-key" {
		t.Errorf("valid fd rejected: %q", got)
	}
}

func TestRED_RtOps_UpgradeClientHasNoWholeBodyDeadline(t *testing.T) {
	c := newUpgradeClient()
	if c.Timeout != 0 {
		t.Errorf("client Timeout = %v; a whole-request deadline fails slow downloads", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.ResponseHeaderTimeout <= 0 {
		t.Errorf("transport must bound the header wait, got %#v", c.Transport)
	}
}

func TestRED_RtOps_DownloadToleratesSlowProgress(t *testing.T) {
	old := upgradeIdleTimeout
	upgradeIdleTimeout = 400 * time.Millisecond
	t.Cleanup(func() { upgradeIdleTimeout = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 6; i++ {
			fmt.Fprint(w, "chunk")
			w.(http.Flusher).Flush()
			time.Sleep(150 * time.Millisecond)
		}
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "bin")
	if err := downloadToFile(context.Background(), newUpgradeClient(), srv.URL, dst); err != nil {
		t.Fatalf("steady slow download failed: %v", err)
	}
	data, _ := os.ReadFile(dst)
	if string(data) != strings.Repeat("chunk", 6) {
		t.Errorf("short body: %q", data)
	}
}

func TestRED_RtOps_DownloadFailsOnStall(t *testing.T) {
	old := upgradeIdleTimeout
	upgradeIdleTimeout = 200 * time.Millisecond
	t.Cleanup(func() { upgradeIdleTimeout = old })
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "x")
		w.(http.Flusher).Flush()
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(done)
	start := time.Now()
	err := downloadToFile(context.Background(), newUpgradeClient(), srv.URL, filepath.Join(t.TempDir(), "bin"))
	if err == nil {
		t.Fatal("stalled download succeeded")
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("stall took %v to detect", time.Since(start))
	}
	if _, err := downloadBytes(context.Background(), newUpgradeClient(), srv.URL); err == nil {
		t.Error("stalled downloadBytes succeeded")
	}
}

func TestRED_RtOps_HelpExitsZero(t *testing.T) {
	rtopsHome(t)
	cases := [][]string{
		{"session", "--help"}, {"session", "-h"}, {"session", "help"},
		{"schedule", "--help"}, {"schedule", "-h"}, {"schedule", "help"},
		{"schedule", "add", "--help"}, {"schedule", "list", "--help"}, {"schedule", "rm", "-h"},
		{"skill", "--help"}, {"skill", "-h"}, {"skill", "help"},
		{"repl", "--help"}, {"init", "--help"}, {"continue", "--help"},
		{"run", "--help"}, {"subagent", "--help"},
		{"memory", "--help"}, {"audit", "--help"}, {"cleanup", "--help"},
		{"logs", "--help"}, {"upgrade", "--help"}, {"serve", "--help"},
		{"telegram", "--help"}, {"mcp", "--help"},
	}
	for _, c := range cases {
		if code := dispatch(c); code != 0 {
			t.Errorf("odek %s exit = %d, want 0", strings.Join(c, " "), code)
		}
	}
}

func TestRED_RtOps_HelpDoesNotSwallowTaskText(t *testing.T) {
	if rtopsHelpRequested("run", []string{"--model", "x", "explain", "--help", "flag"}) {
		t.Error("--help inside task text must not be treated as a help request")
	}
	if rtopsHelpRequested("session", []string{"show", "abc", "--help"}) {
		t.Error("help detection must only look at the leading arguments")
	}
}

func rtopsHelpRequested(cmd string, args []string) bool { return helpRequested(cmd, args) }

func TestRED_RtOps_CliExitTreatsFlagHelpAsSuccess(t *testing.T) {
	if code := cliExit(flag.ErrHelp); code != 0 {
		t.Errorf("cliExit(flag.ErrHelp) = %d", code)
	}
	if code := cliExit(fmt.Errorf("wrapped: %w", flag.ErrHelp)); code != 0 {
		t.Errorf("cliExit(wrapped ErrHelp) = %d", code)
	}
	if code := cliExit(errors.New("boom")); code != 1 {
		t.Errorf("cliExit(error) = %d", code)
	}
}

func TestRED_RtOps_ReplHistoryRedactsSecrets(t *testing.T) {
	home := rtopsHome(t)
	path := filepath.Join(home, ".odek", historyFilename)
	h := newReplHistory()
	h.Load(path)
	secret := "ghp_" + strings.Repeat("a1B2c3D4e5", 4)
	aws := "AKIA" + "IOSFODNN7EXAMPLE"
	h.Add("export GITHUB_TOKEN=" + secret)
	h.Add("use key " + aws + " please")
	h.Add("list the files in the current directory")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || strings.Contains(string(data), aws) {
		t.Errorf("history file holds a secret in clear:\n%s", data)
	}
	if !strings.Contains(string(data), "list the files in the current directory") {
		t.Errorf("ordinary line altered:\n%s", data)
	}

	// A reload plus a trim-driven rewrite must not reintroduce the secret.
	h2 := newReplHistory()
	h2.Load(path)
	h2.max = 2
	h2.Add("another plain line")
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), secret) {
		t.Errorf("rewrite leaked a secret:\n%s", data)
	}
}

func TestRED_RtOps_EngineToolFilterFollowsProfile(t *testing.T) {
	global := config.ToolConfig{Disabled: []string{"memory"}}
	profile := &config.ToolConfig{Enabled: []string{"memory", "read_file"}}
	got := engineToolFilter(global, profile, nil)
	if len(got.Disabled) != 0 || len(got.Enabled) != 2 {
		t.Errorf("profile filter must replace the global one, got %+v", got)
	}
	got = engineToolFilter(global, nil, nil)
	if len(got.Disabled) != 1 || got.Disabled[0] != "memory" {
		t.Errorf("without a profile the global filter applies, got %+v", got)
	}
}

func TestRED_RtOps_HelpFlagAnywhereInLeadingFlags(t *testing.T) {
	rtopsHome(t)
	cases := [][]string{
		{"run", "--model", "x", "-h"},
		{"run", "--model", "x", "--help", "task"},
		{"continue", "--id", "abc", "-h"},
		{"repl", "--model", "x", "--help"},
		{"init", "--force", "-h"},
		{"subagent", "--goal", "g", "--help"},
		{"session", "list", "-h"},
		{"skill", "list", "--help"},
		{"schedule", "list", "-h"},
	}
	for _, c := range cases {
		if code := dispatch(c); code != 0 {
			t.Errorf("odek %s exit = %d, want 0", strings.Join(c, " "), code)
		}
	}
	// A help-looking token after the task text begins stays an error, and
	// text after "--" is verbatim task text.
	if _, err := parseRunFlags([]string{"explain", "--help"}); err == nil {
		t.Error("--help after task text must stay an unknown-flag error")
	}
	if f, err := parseRunFlags([]string{"--", "--help"}); err != nil || f.Task != "--help" {
		t.Errorf("after --: task=%q err=%v", f.Task, err)
	}
}

func TestRED_RtOps_HelpUsageIsCommandSpecific(t *testing.T) {
	for _, c := range []string{"session", "skill", "schedule"} {
		var b strings.Builder
		printCommandUsage(&b, c)
		if !strings.Contains(b.String(), "odek "+c) {
			t.Errorf("usage for %s lacks its own synopsis: %q", c, b.String())
		}
		if strings.Contains(b.String(), "odek upgrade") {
			t.Errorf("usage for %s is the global usage", c)
		}
	}
}

func TestRED_RtOps_EngineToolFilterNeverExceedsParent(t *testing.T) {
	global := config.ToolConfig{Disabled: []string{"memory"}}
	profile := &config.ToolConfig{Disabled: []string{"shell"}}
	parent := &taskToolsPosture{Disabled: []string{"memory"}}
	got := engineToolFilter(global, profile, parent)
	disabled := map[string]bool{}
	for _, n := range got.Disabled {
		disabled[n] = true
	}
	if !disabled["memory"] || !disabled["shell"] {
		t.Errorf("child engine filter must keep the parent's memory restriction, got %+v", got)
	}
}

func TestRED_RtOps_CleanupDryRunListsHeartbeats(t *testing.T) {
	home := t.TempDir()
	stale := filepath.Join(home, "schedule-host-77.json")
	fresh := filepath.Join(home, "schedule-host-78.json")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	c := collectCleanupCandidates(home, maintenance.DefaultConfig())
	if len(c.heartbeats) != 1 || c.heartbeats[0] != stale {
		t.Errorf("heartbeats = %v, want [%s]", c.heartbeats, stale)
	}
}
