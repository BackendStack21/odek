package main

import (
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	odek "github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/sandbox"
)

// A project that requests networks is held to an approval recorded in this
// process: setup that finds no grant refuses instead of attaching them.
func TestRED_ProjectNetworkGrant_MissingGrantIsRefused(t *testing.T) {
	approvalEnv(t)
	resetProjectNetworkGrants()
	d := &mutableComposeDocker{project: "app", nets: map[string]fakeNet{
		"app_default": {id: id("a"), internal: true, compose: "app"},
	}}
	d.install(t)
	resolved := config.ResolvedConfig{
		SandboxComposeProject:  "app",
		ProjectSandboxOverride: config.ProjectSandboxOverride{HasComposeProject: true, ComposeProject: "app"},
	}
	cfg, err := resolveSandboxNetworks(sandboxConfigFromResolved(resolved), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	err = checkProjectNetworkGrant(cfg)
	if err == nil || !errors.Is(err, errSandboxConfig) {
		t.Fatalf("project networks without a recorded approval must be refused, got %v", err)
	}
	t.Setenv("ODEK_APPROVE_PROJECT_SANDBOX", "1")
	if err := checkProjectNetworkGrant(cfg); err != nil {
		t.Fatalf("env approval covers setup: %v", err)
	}
}

// The parent stamps the IDs of the networks it attached; a child attaches a
// network only while it still has one of those IDs.
func TestRED_SubagentNetworks_StampedIDsAreEnforced(t *testing.T) {
	d := &delegateTasksTool{}
	recordChildSandbox([]odek.Tool{d}, sandboxConfig{
		Networks:         []string{"db"},
		ResolvedNetworks: []sandbox.NetworkInfo{{Name: "db", ID: id("a")}},
	})
	_, posture := d.childPosture()
	if posture == nil || !reflect.DeepEqual(posture.NetworkIDs, []string{id("a")}) {
		t.Fatalf("parent must stamp the resolved network IDs, got %+v", posture)
	}

	resolved := config.ResolvedConfig{SandboxNetworks: []string{"db"}}
	applyTaskSandboxPosture(&resolved, &taskSandboxPosture{
		Networks: []string{"db"}, NetworkIDs: []string{id("a"), "not-an-id", strings.ToUpper(id("b"))},
	})
	cfg := sandboxConfigFromResolved(resolved)
	if !reflect.DeepEqual(cfg.AllowedNetworkIDs, []string{id("a")}) {
		t.Fatalf("child must keep only valid stamped IDs, got %v", cfg.AllowedNetworkIDs)
	}

	cfg.ResolvedNetworks = []sandbox.NetworkInfo{{Name: "db", ID: id("a")}}
	if err := checkAllowedNetworkIDs(cfg); err != nil {
		t.Fatalf("the parent's network must be accepted: %v", err)
	}
	cfg.ResolvedNetworks = []sandbox.NetworkInfo{{Name: "db", ID: id("c")}}
	err := checkAllowedNetworkIDs(cfg)
	if err == nil || !errors.Is(err, errSandboxConfig) || !strings.Contains(err.Error(), "db") {
		t.Fatalf("a network recreated under the parent's name must be refused, got %v", err)
	}
	// No stamped IDs: nothing to enforce (the child of an unsandboxed or
	// network-less parent attaches no networks anyway).
	if err := checkAllowedNetworkIDs(sandboxConfig{ResolvedNetworks: cfg.ResolvedNetworks}); err != nil {
		t.Fatalf("no stamped IDs must not refuse: %v", err)
	}
}

func TestRED_SetupSandbox_ChecksStampedNetworkIDs(t *testing.T) {
	src := mustReadFile(t, "main.go")
	i := strings.Index(src, "func setupSandbox(")
	j := strings.Index(src[i:], "sandbox.BuildRunArgsChecked(")
	if i < 0 || j < 0 || !strings.Contains(src[i:i+j], "checkAllowedNetworkIDs(") {
		t.Fatal("setupSandbox must check stamped network IDs before creating the container")
	}
}

func mustReadFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
