package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/sandbox"
)

// mutableComposeDocker serves a compose project whose network list and IDs
// can change between the approval prompt and container setup.
type mutableComposeDocker struct {
	project string
	nets    map[string]fakeNet // name → network; the project's networks carry compose == project
}

func (m *mutableComposeDocker) install(t *testing.T) {
	t.Helper()
	old := sandbox.DockerRunner
	sandbox.DockerRunner = func(ctx context.Context, args ...string) ([]byte, error) {
		switch {
		case len(args) >= 2 && args[0] == "network" && args[1] == "ls":
			var names []string
			for name, n := range m.nets {
				if n.compose == m.project {
					names = append(names, name)
				}
			}
			return []byte(strings.Join(names, "\n") + "\n"), nil
		case len(args) >= 3 && args[0] == "network" && args[1] == "inspect":
			name := args[len(args)-1]
			n, ok := m.nets[name]
			if !ok {
				return []byte("not found"), errors.New("exit 1")
			}
			return fmt.Appendf(nil, `[{"Name":%q,"Id":%q,"Driver":"bridge","Scope":"local","Internal":%t,"Labels":{"com.docker.compose.project":%q}}]`,
				name, n.id, n.internal, n.compose), nil
		}
		return nil, fmt.Errorf("unexpected docker call %v", args)
	}
	t.Cleanup(func() { sandbox.DockerRunner = old })
}

// setupNets resolves networks the way setupSandbox does for cfg and returns
// the config setupSandbox checks.
func setupNets(t *testing.T, cfg sandboxConfig) sandboxConfig {
	t.Helper()
	got, err := resolveSandboxNetworks(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func composeOverride(project string) config.ResolvedConfig {
	return config.ResolvedConfig{ProjectSandboxOverride: config.ProjectSandboxOverride{HasComposeProject: true, ComposeProject: project}}
}

func TestRED_ProjectNetworkGrant_NetworkJoiningAfterApprovalIsRefused(t *testing.T) {
	approvalEnv(t)
	d := &mutableComposeDocker{project: "app", nets: map[string]fakeNet{
		"app_default": {id: id("a"), internal: true, compose: "app"},
	}}
	d.install(t)
	if err := approveProjectSandboxWithTTY(composeOverride("app"), strings.NewReader("y\n"), io.Discard, true); err != nil {
		t.Fatal(err)
	}
	cfg := sandboxConfig{ComposeProject: "app", ProjectComposeProject: "app"}

	if err := checkProjectNetworkGrant(setupNets(t, cfg)); err != nil {
		t.Fatalf("the approved networks must be accepted at setup: %v", err)
	}

	// A network joins the project after the prompt: setup would attach a
	// network the operator never saw.
	d.nets["app_admin"] = fakeNet{id: id("c"), internal: false, compose: "app"}
	err := checkProjectNetworkGrant(setupNets(t, cfg))
	if err == nil || !errors.Is(err, errSandboxConfig) {
		t.Fatalf("a network that joined after approval must be refused as a config error, got %v", err)
	}
	if !strings.Contains(err.Error(), "app_admin") {
		t.Errorf("the refusal must name the unapproved network: %v", err)
	}
}

func TestRED_ProjectNetworkGrant_RecreatedNetworkIsRefused(t *testing.T) {
	approvalEnv(t)
	d := &mutableComposeDocker{nets: map[string]fakeNet{"shared": {id: id("a"), internal: true}}}
	d.install(t)
	if err := approveProjectSandboxWithTTY(netOverride("shared"), strings.NewReader("y\n"), io.Discard, true); err != nil {
		t.Fatal(err)
	}
	d.nets["shared"] = fakeNet{id: id("d"), internal: false}
	if err := checkProjectNetworkGrant(setupNets(t, sandboxConfig{Networks: []string{"shared"}, ProjectNetworks: []string{"shared"}})); err == nil {
		t.Fatal("a network recreated under the approved name must be refused")
	}
}

func TestRED_ProjectNetworkGrant_PersistedTrustIsCheckedToo(t *testing.T) {
	approvalEnv(t)
	d := &mutableComposeDocker{project: "app", nets: map[string]fakeNet{
		"app_default": {id: id("a"), internal: true, compose: "app"},
	}}
	d.install(t)
	if err := approveProjectSandboxWithTTY(composeOverride("app"), strings.NewReader("t\n"), io.Discard, true); err != nil {
		t.Fatal(err)
	}
	resetProjectNetworkGrants()
	// A later run is approved from the persisted trust without a prompt…
	if err := approveProjectSandboxWithTTY(composeOverride("app"), strings.NewReader(""), io.Discard, false); err != nil {
		t.Fatal(err)
	}
	// …and its setup is still held to the networks that trust covered.
	d.nets["app_admin"] = fakeNet{id: id("c"), compose: "app"}
	if err := checkProjectNetworkGrant(setupNets(t, sandboxConfig{ComposeProject: "app", ProjectComposeProject: "app"})); err == nil {
		t.Fatal("setup after a persisted approval must still refuse an unapproved network")
	}
}

func TestRED_ProjectNetworkGrant_OperatorNetworksAndEnvApprovalUnaffected(t *testing.T) {
	approvalEnv(t)
	resetProjectNetworkGrants()
	d := &mutableComposeDocker{nets: map[string]fakeNet{"opnet": {id: id("e"), internal: true}}}
	d.install(t)
	// No project override: operator-configured networks need no grant.
	if err := checkProjectNetworkGrant(setupNets(t, sandboxConfig{Networks: []string{"opnet"}})); err != nil {
		t.Fatalf("operator networks must not need a project grant: %v", err)
	}
	// ODEK_APPROVE_PROJECT_SANDBOX=1 approves whatever the project asks for.
	t.Setenv("ODEK_APPROVE_PROJECT_SANDBOX", "1")
	if err := approveProjectSandboxWithTTY(netOverride("opnet"), strings.NewReader(""), io.Discard, false); err != nil {
		t.Fatal(err)
	}
	if err := checkProjectNetworkGrant(setupNets(t, sandboxConfig{Networks: []string{"opnet"}, ProjectNetworks: []string{"opnet"}})); err != nil {
		t.Fatalf("env approval must cover setup: %v", err)
	}
}

func TestRED_SetupSandbox_ChecksProjectNetworkGrant(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	i := strings.Index(s, "func setupSandbox(")
	j := strings.Index(s[i:], "sandbox.BuildRunArgsChecked(")
	if i < 0 || j < 0 || !strings.Contains(s[i:i+j], "checkProjectNetworkGrant(") {
		t.Fatal("setupSandbox must check the project network grant before creating the container")
	}
}
