package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
)

// The recorded network grant always reflects the latest approval for the
// directory: an approval whose override requests no networks drops an
// earlier network grant instead of leaving it in place.
func TestRED_ProjectNetworkGrant_LaterApprovalWithoutNetworksClearsIt(t *testing.T) {
	approvalEnv(t)
	resetProjectNetworkGrants()
	d := &mutableComposeDocker{nets: map[string]fakeNet{"shared": {id: id("a"), internal: true}}}
	d.install(t)
	if err := approveProjectSandboxWithTTY(netOverride("shared"), strings.NewReader("y\n"), io.Discard, true); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	dir, _ := filepath.Abs(wd)
	projectNetworkGrantsMu.Lock()
	_, ok := projectNetworkGrants[dir]
	projectNetworkGrantsMu.Unlock()
	if !ok {
		t.Fatal("the first approval must record a network grant")
	}
	memOnly := config.ResolvedConfig{ProjectSandboxOverride: config.ProjectSandboxOverride{HasMemory: true, Memory: "512m"}}
	if err := approveProjectSandboxWithTTY(memOnly, strings.NewReader("y\n"), io.Discard, true); err != nil {
		t.Fatal(err)
	}
	projectNetworkGrantsMu.Lock()
	_, stale := projectNetworkGrants[dir]
	projectNetworkGrantsMu.Unlock()
	if stale {
		t.Error("an approval without network fields must drop the earlier network grant")
	}
}

// The same holds for approvals that carry no override at all: a Dockerfile.odek
// build approval, or a run with nothing to approve.
func TestRED_ProjectNetworkGrant_ApprovalWithoutOverrideClearsIt(t *testing.T) {
	approvalEnv(t)
	d := &mutableComposeDocker{nets: map[string]fakeNet{"shared": {id: id("a"), internal: true}}}
	d.install(t)
	wd, _ := os.Getwd()
	dir, _ := filepath.Abs(wd)
	granted := func() bool {
		projectNetworkGrantsMu.Lock()
		defer projectNetworkGrantsMu.Unlock()
		_, ok := projectNetworkGrants[dir]
		return ok
	}
	grant := func() {
		t.Helper()
		resetProjectNetworkGrants()
		if err := approveProjectSandboxWithTTY(netOverride("shared"), strings.NewReader("y\n"), io.Discard, true); err != nil || !granted() {
			t.Fatalf("setup: network approval must record a grant (err=%v)", err)
		}
	}

	grant()
	if err := approveProjectSandboxWithTTY(config.ResolvedConfig{}, strings.NewReader(""), io.Discard, false); err != nil {
		t.Fatal(err)
	}
	if granted() {
		t.Error("a run with nothing to approve must drop the earlier network grant")
	}

	grant()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile.odek"), []byte("FROM alpine:3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := approveProjectSandboxWithTTY(config.ResolvedConfig{Sandbox: true}, strings.NewReader("y\n"), io.Discard, true); err != nil {
		t.Fatal(err)
	}
	if granted() {
		t.Error("a Dockerfile-only approval must drop the earlier network grant")
	}
}
