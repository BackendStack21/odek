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
