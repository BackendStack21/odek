package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/sandbox"
)

// ── flag parsing: run, repl, continue ───────────────────────────────────

func TestParseRunFlags_SandboxNetworks(t *testing.T) {
	f, err := parseRunFlags([]string{"--sandbox-networks", "app_default, db-net,,cache", "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"app_default", "db-net", "cache"}; !reflect.DeepEqual(f.SandboxNetworks, want) {
		t.Errorf("SandboxNetworks = %v, want %v", f.SandboxNetworks, want)
	}
	if _, err := parseRunFlags([]string{"--sandbox-networks"}); err == nil || !strings.Contains(err.Error(), "--sandbox-networks requires a value") {
		t.Errorf("missing value error = %v", err)
	}
	if got := cliFlagsFromRun(f).SandboxNetworks; !reflect.DeepEqual(got, f.SandboxNetworks) {
		t.Errorf("cliFlagsFromRun dropped the networks: %v", got)
	}
}

func TestParseReplFlags_SandboxNetworks(t *testing.T) {
	f, err := parseReplFlags([]string{"--sandbox-networks", "a,b"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(f.SandboxNetworks, want) {
		t.Errorf("SandboxNetworks = %v, want %v", f.SandboxNetworks, want)
	}
}

func TestContinuePinnedFlag_SandboxNetworks(t *testing.T) {
	if got := continuePinnedFlag(runFlags{SandboxNetworks: []string{"a"}}); got != "--sandbox-networks" {
		t.Errorf("continuePinnedFlag = %q, want --sandbox-networks", got)
	}
	_, _, err := parseContinueArgs([]string{"--id", "s1", "--sandbox-networks", "a", "go on"})
	if err == nil || !strings.Contains(err.Error(), "--sandbox-networks") {
		t.Errorf("continue must refuse --sandbox-networks by name, got %v", err)
	}
}

// ── help and init templates ─────────────────────────────────────────────

func TestHelp_DocumentsSandboxNetworks(t *testing.T) {
	usage := captureStdout(printUsage)
	for _, want := range []string{"--sandbox-networks", "ODEK_SANDBOX_NETWORKS"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage missing %q", want)
		}
	}
	if serve := captureStdout(printServeHelp); !strings.Contains(serve, "--sandbox-networks") {
		t.Error("serve help missing --sandbox-networks")
	}
}

func TestInitTemplates_SandboxNetworksEmpty(t *testing.T) {
	for name, tpl := range map[string]string{"global": globalConfigTemplate, "local": localConfigTemplate} {
		var m map[string]any
		if err := json.Unmarshal([]byte(tpl), &m); err != nil {
			t.Fatalf("%s template invalid JSON: %v", name, err)
		}
		v, ok := m["sandbox_networks"]
		if !ok {
			t.Errorf("%s template missing sandbox_networks", name)
			continue
		}
		arr, isArr := v.([]any)
		if !isArr || len(arr) != 0 {
			t.Errorf("%s template sandbox_networks = %v, want an empty list", name, v)
		}
	}
}

// ── wiring ──────────────────────────────────────────────────────────────

func TestSandboxNetworks_FlagWiringPresent(t *testing.T) {
	read := func(f string) string {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	serve := read("serve.go")
	if !strings.Contains(serve, `case "--sandbox-networks"`) {
		t.Error("serve.go does not parse --sandbox-networks")
	}
	if !regexp.MustCompile(`SandboxNetworks:\s+sandboxNetworks`).MatchString(serve) {
		t.Error("serve.go does not pass the flag into config.CLIFlags")
	}
	if !regexp.MustCompile(`SandboxNetworks:\s+f\.SandboxNetworks`).MatchString(read("repl.go")) {
		t.Error("repl.go does not pass the flag into config.CLIFlags")
	}
	main := read("main.go")
	if !strings.Contains(main, `case "--sandbox-networks"`) {
		t.Error("main.go does not parse --sandbox-networks")
	}
	if !strings.Contains(read("external_ref.go"), `"--sandbox-networks"`) {
		t.Error("continuePinnedFlag does not name --sandbox-networks")
	}
}

func TestSandboxConfigFromResolved_CarriesNetworks(t *testing.T) {
	got := sandboxConfigFromResolved(config.ResolvedConfig{SandboxNetworks: []string{"a", "b"}})
	if !reflect.DeepEqual(got.Networks, []string{"a", "b"}) {
		t.Errorf("Networks = %v, want [a b]", got.Networks)
	}
}

// ── setupSandbox validates before any container exists ──────────────────

func TestSetupSandbox_InvalidNetworkNamesAreConfigErrors(t *testing.T) {
	t.Setenv("ODEK_SANDBOX_NETWORK_LENIENT", "1") // leniency never covers attached networks
	old := sandbox.DockerRunner
	var calls []string
	sandbox.DockerRunner = func(ctx context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return nil, errors.New("must not be called")
	}
	t.Cleanup(func() { sandbox.DockerRunner = old })

	for _, bad := range []string{"host", "none", "bridge", "container:x", "has space", "a;b"} {
		_, cleanup, err := setupSandbox(nil, sandboxConfig{Image: "alpine:3", Networks: []string{bad}})
		if cleanup != nil {
			_ = cleanup()
		}
		if err == nil || !errors.Is(err, errSandboxConfig) {
			t.Errorf("network %q: want error wrapping errSandboxConfig, got %v", bad, err)
		}
		if err != nil && !strings.Contains(err.Error(), bad) {
			t.Errorf("network %q: message must name the value: %v", bad, err)
		}
	}
	if len(calls) != 0 {
		t.Errorf("docker must not be consulted for invalid names: %v", calls)
	}
}

func TestSetupSandbox_UnresolvableNetworkIsConfigError(t *testing.T) {
	old := sandbox.DockerRunner
	sandbox.DockerRunner = func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "network" && args[1] == "inspect" {
			return []byte("Error: No such network: ghost_net"), errors.New("exit status 1")
		}
		return nil, fmt.Errorf("unexpected docker call %v", args)
	}
	t.Cleanup(func() { sandbox.DockerRunner = old })

	_, cleanup, err := setupSandbox(nil, sandboxConfig{Image: "alpine:3", Networks: []string{"ghost_net"}})
	if cleanup != nil {
		_ = cleanup()
	}
	if err == nil || !errors.Is(err, errSandboxConfig) {
		t.Fatalf("want error wrapping errSandboxConfig, got %v", err)
	}
	if !strings.Contains(err.Error(), "ghost_net") {
		t.Errorf("message must name the network: %v", err)
	}
}

func TestEnsureSandbox_NetworkConfigErrorNeverFallsBackToHost(t *testing.T) {
	t.Setenv("ODEK_REQUIRE_SANDBOX", "")
	t.Setenv("ODEK_NO_SANDBOX", "")
	old := sandbox.DockerRunner
	sandbox.DockerRunner = func(ctx context.Context, args ...string) ([]byte, error) {
		return []byte("Error: No such network"), errors.New("exit status 1")
	}
	t.Cleanup(func() { sandbox.DockerRunner = old })

	cfg := sandboxConfig{Image: "alpine:3", Networks: []string{"ghost_net"}}
	_, _, sandboxed, err := ensureSandbox(config.ResolvedConfig{}, nil, cfg)
	if err == nil || sandboxed {
		t.Fatalf("an unresolvable network must be fatal in implicit mode, got sandboxed=%v err=%v", sandboxed, err)
	}
}

// ── effective posture ───────────────────────────────────────────────────

func TestSandboxPostureLine_ListsNetworks(t *testing.T) {
	got := sandboxPostureLine(sandboxConfig{Networks: []string{"app_default", "db-net"}}, "alpine:3")
	if !strings.HasPrefix(got, "odek: sandbox network=attached pids=4096 image=alpine:3") {
		t.Errorf("line prefix changed: %q", got)
	}
	if !strings.Contains(got, "networks=app_default,db-net") {
		t.Errorf("line must list attached networks: %q", got)
	}
	if plain := sandboxPostureLine(sandboxConfig{}, "alpine:3"); strings.Contains(plain, "networks") {
		t.Errorf("no networks, no networks field: %q", plain)
	}
}

func TestSandboxPostureLine_SanitizesNetworks(t *testing.T) {
	got := sandboxPostureLine(sandboxConfig{Networks: []string{"evil\x1b[31m\nodek: injected‮"}}, "alpine:3")
	for _, bad := range []string{"\x1b", "\n", "\r", "‮"} {
		if strings.Contains(got, bad) {
			t.Errorf("line contains raw %q: %q", bad, got)
		}
	}
}

func TestConfigView_ReportsSandboxNetworks(t *testing.T) {
	resolved := config.ResolvedConfig{Sandbox: true, SandboxNetworks: []string{"a", "b"}}
	sb, ok := buildConfigView(resolved)["sandbox"].(map[string]any)
	if !ok {
		t.Fatal("config view has no sandbox map")
	}
	if got := fmt.Sprint(sb["networks"]); got != "[a b]" {
		t.Errorf("sandbox networks = %v, want [a b]", sb["networks"])
	}
}

// ── approval key and gate ───────────────────────────────────────────────

func TestProjectSandboxApprovalKey_NetworksGolden(t *testing.T) {
	base := config.ProjectSandboxOverride{
		HasImage: true, Image: "alpine:3", HasNetwork: true, Network: "bridge",
		HasEnv: true, EnvKeys: []string{"A"}, Env: map[string]string{"A": "1"},
		HasVolumes: true, Volumes: []string{"./data:/data"},
		HasUser: true, User: "1000:1000", HasMemory: true, Memory: "512m", HasCPUs: true, CPUs: "2",
	}
	const golden = "d73c22ca03909f6d99e24ee57234da59b51889fa3ba4764723c8f3ab3813df88"
	if got := projectSandboxApprovalKey("/proj/golden", base); got != golden {
		t.Errorf("key for a config without networks changed: got %s want %s", got, golden)
	}
	with := base
	with.HasNetworks = true
	with.Networks = []string{"app_default"}
	if projectSandboxApprovalKey("/proj/golden", with) == golden {
		t.Error("project networks must change the approval key")
	}
	other := with
	other.Networks = []string{"other_net"}
	if projectSandboxApprovalKey("/proj/golden", other) == projectSandboxApprovalKey("/proj/golden", with) {
		t.Error("different networks must produce different approval keys")
	}
}

func TestApproveProjectSandbox_NetworksOverrideRequiresApproval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_APPROVE_PROJECT_SANDBOX", "")
	t.Chdir(t.TempDir())
	resolved := config.ResolvedConfig{ProjectSandboxOverride: config.ProjectSandboxOverride{HasNetworks: true, Networks: []string{"app_default"}}}
	if err := approveProjectSandboxWithTTY(resolved, strings.NewReader(""), io.Discard, false); err == nil {
		t.Error("project sandbox_networks must go through the operator approval gate")
	}
}

// ── sub-agent posture ───────────────────────────────────────────────────

func TestChildSandboxPosture_StampsNetworks(t *testing.T) {
	d := &delegateTasksTool{}
	recordChildSandbox([]odek.Tool{d}, sandboxConfig{Network: "none", Networks: []string{"a", "b"}})
	_, sp := d.childPosture()
	if sp == nil || !reflect.DeepEqual(sp.Networks, []string{"a", "b"}) {
		t.Fatalf("posture = %+v, want Networks [a b]", sp)
	}
}
