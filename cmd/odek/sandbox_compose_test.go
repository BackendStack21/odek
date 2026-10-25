package main

import (
	"bytes"
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

func TestParseRunFlags_SandboxCompose(t *testing.T) {
	f, err := parseRunFlags([]string{"--sandbox-compose", "myapp", "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if f.SandboxComposeProject != "myapp" {
		t.Errorf("SandboxComposeProject = %q, want myapp", f.SandboxComposeProject)
	}
	if _, err := parseRunFlags([]string{"--sandbox-compose"}); err == nil || !strings.Contains(err.Error(), "--sandbox-compose requires a value") {
		t.Errorf("missing value error = %v", err)
	}
	if got := cliFlagsFromRun(f).SandboxComposeProject; got != "myapp" {
		t.Errorf("cliFlagsFromRun dropped the compose project: %q", got)
	}
}

func TestParseReplFlags_SandboxCompose(t *testing.T) {
	f, err := parseReplFlags([]string{"--sandbox-compose", "myapp"})
	if err != nil {
		t.Fatal(err)
	}
	if f.SandboxComposeProject != "myapp" {
		t.Errorf("SandboxComposeProject = %q, want myapp", f.SandboxComposeProject)
	}
}

func TestContinuePinnedFlag_SandboxCompose(t *testing.T) {
	if got := continuePinnedFlag(runFlags{SandboxComposeProject: "p"}); got != "--sandbox-compose" {
		t.Errorf("continuePinnedFlag = %q, want --sandbox-compose", got)
	}
	_, _, err := parseContinueArgs([]string{"--id", "s1", "--sandbox-compose", "p", "go on"})
	if err == nil || !strings.Contains(err.Error(), "--sandbox-compose") {
		t.Errorf("continue must refuse --sandbox-compose by name, got %v", err)
	}
}

// ── help and init templates ─────────────────────────────────────────────

func TestHelp_DocumentsSandboxCompose(t *testing.T) {
	usage := captureStdout(printUsage)
	for _, want := range []string{"--sandbox-compose", "ODEK_SANDBOX_COMPOSE_PROJECT"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage missing %q", want)
		}
	}
	if serve := captureStdout(printServeHelp); !strings.Contains(serve, "--sandbox-compose") {
		t.Error("serve help missing --sandbox-compose")
	}
}

func TestInitTemplates_SandboxComposeProjectEmpty(t *testing.T) {
	for name, tpl := range map[string]string{"global": globalConfigTemplate, "local": localConfigTemplate} {
		var m map[string]any
		if err := json.Unmarshal([]byte(tpl), &m); err != nil {
			t.Fatalf("%s template invalid JSON: %v", name, err)
		}
		v, ok := m["sandbox_compose_project"]
		if !ok {
			t.Errorf("%s template missing sandbox_compose_project", name)
			continue
		}
		if s, isStr := v.(string); !isStr || s != "" {
			t.Errorf("%s template sandbox_compose_project = %v, want an empty string", name, v)
		}
	}
}

// ── wiring ──────────────────────────────────────────────────────────────

func TestSandboxCompose_FlagWiringPresent(t *testing.T) {
	read := func(f string) string {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	serve := read("serve.go")
	if !strings.Contains(serve, `case "--sandbox-compose"`) {
		t.Error("serve.go does not parse --sandbox-compose")
	}
	if !regexp.MustCompile(`SandboxComposeProject:\s+sandboxComposeProject`).MatchString(serve) {
		t.Error("serve.go does not pass the flag into config.CLIFlags")
	}
	if !regexp.MustCompile(`SandboxComposeProject:\s+f\.SandboxComposeProject`).MatchString(read("repl.go")) {
		t.Error("repl.go does not pass the flag into config.CLIFlags")
	}
	if !strings.Contains(read("main.go"), `case "--sandbox-compose"`) {
		t.Error("main.go does not parse --sandbox-compose")
	}
	if !strings.Contains(read("external_ref.go"), `"--sandbox-compose"`) {
		t.Error("continuePinnedFlag does not name --sandbox-compose")
	}
}

func TestSandboxConfigFromResolved_CarriesComposeProject(t *testing.T) {
	got := sandboxConfigFromResolved(config.ResolvedConfig{SandboxComposeProject: "myapp"})
	if got.ComposeProject != "myapp" {
		t.Errorf("ComposeProject = %q, want myapp", got.ComposeProject)
	}
}

// ── resolution: label lookup, merge, listing on stderr ──────────────────

func composeRunner(t *testing.T, lsOut string, calls *[]string) {
	t.Helper()
	old := sandbox.DockerRunner
	sandbox.DockerRunner = func(ctx context.Context, args ...string) ([]byte, error) {
		*calls = append(*calls, strings.Join(args, " "))
		switch {
		case len(args) >= 2 && args[0] == "network" && args[1] == "ls":
			return []byte(lsOut), nil
		case len(args) >= 3 && args[0] == "network" && args[1] == "inspect":
			name := args[len(args)-1]
			nid := fmt.Sprintf("%064x", len(name)*7919+int(name[0]))
			return []byte(fmt.Sprintf(`[{"Name":%q,"Id":%q,"Driver":"bridge","Scope":"local","Internal":true,"Labels":{}}]`, name, nid)), nil
		}
		return nil, fmt.Errorf("unexpected docker call %v", args)
	}
	t.Cleanup(func() { sandbox.DockerRunner = old })
}

func TestResolveSandboxNetworks_ComposeProjectAttachesLabelledNetworks(t *testing.T) {
	var calls []string
	composeRunner(t, "myapp_default\nmyapp_db\n", &calls)

	var stderr bytes.Buffer
	got, err := resolveSandboxNetworks(sandboxConfig{ComposeProject: "myapp"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"myapp_default", "myapp_db"}; !reflect.DeepEqual(got.Networks, want) {
		t.Errorf("Networks = %v, want %v", got.Networks, want)
	}
	if len(got.ResolvedNetworks) != 2 || got.ResolvedNetworks[0].Name != "myapp_default" || len(got.ResolvedNetworks[0].ID) != 64 {
		t.Errorf("ResolvedNetworks = %+v, want both inspected with full IDs", got.ResolvedNetworks)
	}
	ls := ""
	for _, c := range calls {
		if strings.HasPrefix(c, "network ls") {
			ls = c
		}
	}
	if !strings.Contains(ls, "label=com.docker.compose.project=myapp") {
		t.Errorf("must look the project up by label, calls: %v", calls)
	}
	out := stderr.String()
	for _, want := range []string{"myapp", "myapp_default", "myapp_db"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr must list %q: %q", want, out)
		}
	}
}

func TestResolveSandboxNetworks_MergesExplicitAndComposeDeduped(t *testing.T) {
	var calls []string
	composeRunner(t, "myapp_default\nshared\n", &calls)

	got, err := resolveSandboxNetworks(sandboxConfig{
		Networks:       []string{"shared", "extra"},
		ComposeProject: "myapp",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"shared", "extra", "myapp_default"}; !reflect.DeepEqual(got.Networks, want) {
		t.Errorf("Networks = %v, want %v (explicit first, deduped)", got.Networks, want)
	}
	if len(got.ResolvedNetworks) != 3 {
		t.Errorf("ResolvedNetworks = %d entries, want 3", len(got.ResolvedNetworks))
	}
}

func TestResolveSandboxNetworks_NoProjectNoDockerCall(t *testing.T) {
	var calls []string
	composeRunner(t, "", &calls)
	got, err := resolveSandboxNetworks(sandboxConfig{}, io.Discard)
	if err != nil || len(got.Networks) != 0 || len(got.ResolvedNetworks) != 0 {
		t.Errorf("empty config = %+v, %v; want untouched", got, err)
	}
	if len(calls) != 0 {
		t.Errorf("docker must not be consulted without networks: %v", calls)
	}
}

func TestResolveSandboxNetworks_ExplicitOnlyStillResolves(t *testing.T) {
	var calls []string
	composeRunner(t, "", &calls)
	got, err := resolveSandboxNetworks(sandboxConfig{Networks: []string{"a", "b"}}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Networks, []string{"a", "b"}) || len(got.ResolvedNetworks) != 2 {
		t.Errorf("got %+v, want both networks inspected", got)
	}
}

func TestResolveSandboxNetworks_NoNetworksForProjectIsFatalWithHint(t *testing.T) {
	var calls []string
	composeRunner(t, "\n", &calls)
	_, err := resolveSandboxNetworks(sandboxConfig{ComposeProject: "myapp"}, io.Discard)
	if err == nil || !errors.Is(err, errSandboxConfig) {
		t.Fatalf("want error wrapping errSandboxConfig, got %v", err)
	}
	for _, want := range []string{"myapp", "is it running?", "docker compose -p myapp up -d"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message must contain %q: %v", want, err)
		}
	}
}

func TestResolveSandboxNetworks_InvalidProjectNameIsConfigError(t *testing.T) {
	t.Setenv("ODEK_SANDBOX_NETWORK_LENIENT", "1")
	var calls []string
	composeRunner(t, "x\n", &calls)
	for _, bad := range []string{"has space", "a;b", "label=x", "-lead", "new\nline"} {
		_, err := resolveSandboxNetworks(sandboxConfig{ComposeProject: bad}, io.Discard)
		if err == nil || !errors.Is(err, errSandboxConfig) {
			t.Errorf("project %q: want errSandboxConfig, got %v", bad, err)
		}
	}
	if len(calls) != 0 {
		t.Errorf("docker must not be consulted for invalid project names: %v", calls)
	}
}

func TestResolveSandboxNetworks_MergedListIsCapped(t *testing.T) {
	var calls []string
	var lines []string
	for i := 0; i < 6; i++ {
		lines = append(lines, fmt.Sprintf("proj_net%d", i))
	}
	composeRunner(t, strings.Join(lines, "\n"), &calls)
	_, err := resolveSandboxNetworks(sandboxConfig{
		Networks:       []string{"a", "b", "c", "d"},
		ComposeProject: "proj",
	}, io.Discard)
	if err == nil || !errors.Is(err, errSandboxConfig) {
		t.Fatalf("10 merged networks must exceed the cap with errSandboxConfig, got %v", err)
	}
}

func TestResolveSandboxNetworks_ComposeNetworkMustBeLocalBridge(t *testing.T) {
	old := sandbox.DockerRunner
	sandbox.DockerRunner = func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "ls" {
			return []byte("p_overlay\n"), nil
		}
		return []byte(fmt.Sprintf(`[{"Name":"p_overlay","Id":%q,"Driver":"overlay","Scope":"swarm","Labels":{}}]`, strings.Repeat("a", 64))), nil
	}
	t.Cleanup(func() { sandbox.DockerRunner = old })
	_, err := resolveSandboxNetworks(sandboxConfig{ComposeProject: "p"}, io.Discard)
	if err == nil || !errors.Is(err, errSandboxConfig) {
		t.Fatalf("a non-bridge compose network must be refused with errSandboxConfig, got %v", err)
	}
}

func TestSetupSandbox_ComposeProjectErrorsAreConfigErrors(t *testing.T) {
	t.Setenv("ODEK_SANDBOX_NETWORK_LENIENT", "1")
	var calls []string
	composeRunner(t, "", &calls) // project has no networks
	_, cleanup, err := setupSandbox(nil, sandboxConfig{Image: "alpine:3", ComposeProject: "myapp"})
	if cleanup != nil {
		_ = cleanup()
	}
	if err == nil || !errors.Is(err, errSandboxConfig) {
		t.Fatalf("want error wrapping errSandboxConfig, got %v", err)
	}
	if !strings.Contains(err.Error(), "docker compose -p myapp up -d") {
		t.Errorf("message must carry the hint: %v", err)
	}
}

func TestEnsureSandbox_ComposeConfigErrorNeverFallsBackToHost(t *testing.T) {
	t.Setenv("ODEK_REQUIRE_SANDBOX", "")
	t.Setenv("ODEK_NO_SANDBOX", "")
	var calls []string
	composeRunner(t, "", &calls)
	cfg := sandboxConfig{Image: "alpine:3", ComposeProject: "myapp"}
	_, _, sandboxed, err := ensureSandbox(config.ResolvedConfig{}, nil, cfg)
	if err == nil || sandboxed {
		t.Fatalf("a compose project without networks must be fatal in implicit mode, got sandboxed=%v err=%v", sandboxed, err)
	}
}

// ── effective posture ───────────────────────────────────────────────────

func TestConfigView_ReportsSandboxComposeProject(t *testing.T) {
	resolved := config.ResolvedConfig{Sandbox: true, SandboxComposeProject: "myapp"}
	sb, ok := buildConfigView(resolved)["sandbox"].(map[string]any)
	if !ok {
		t.Fatal("config view has no sandbox map")
	}
	if got := fmt.Sprint(sb["compose_project"]); got != "myapp" {
		t.Errorf("sandbox compose_project = %v, want myapp", sb["compose_project"])
	}
}

// ── approval key and gate ───────────────────────────────────────────────

func TestProjectSandboxApprovalKey_ComposeProject(t *testing.T) {
	base := config.ProjectSandboxOverride{
		HasImage: true, Image: "alpine:3", HasNetwork: true, Network: "bridge",
		HasEnv: true, EnvKeys: []string{"A"}, Env: map[string]string{"A": "1"},
		HasVolumes: true, Volumes: []string{"./data:/data"},
		HasUser: true, User: "1000:1000", HasMemory: true, Memory: "512m", HasCPUs: true, CPUs: "2",
	}
	const golden = "d73c22ca03909f6d99e24ee57234da59b51889fa3ba4764723c8f3ab3813df88"
	if got := projectSandboxApprovalKey("/proj/golden", base); got != golden {
		t.Errorf("key for a config without a compose project changed: got %s want %s", got, golden)
	}
	with := base
	with.HasComposeProject = true
	with.ComposeProject = "myapp"
	k := projectSandboxApprovalKey("/proj/golden", with)
	if k == golden {
		t.Error("a project compose project must change the approval key")
	}
	other := with
	other.ComposeProject = "other"
	if projectSandboxApprovalKey("/proj/golden", other) == k {
		t.Error("different compose projects must produce different approval keys")
	}
}

func TestApproveProjectSandbox_ComposeOverrideRequiresApproval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_APPROVE_PROJECT_SANDBOX", "")
	t.Chdir(t.TempDir())
	resolved := config.ResolvedConfig{ProjectSandboxOverride: config.ProjectSandboxOverride{HasComposeProject: true, ComposeProject: "myapp"}}
	if err := approveProjectSandboxWithTTY(resolved, strings.NewReader(""), io.Discard, false); err == nil {
		t.Error("project sandbox_compose_project must go through the operator approval gate")
	}
}

func TestApproveProjectSandbox_PromptShowsComposeProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_APPROVE_PROJECT_SANDBOX", "")
	t.Chdir(t.TempDir())
	resolved := config.ResolvedConfig{ProjectSandboxOverride: config.ProjectSandboxOverride{HasComposeProject: true, ComposeProject: "myapp\x1b[31m"}}
	var out bytes.Buffer
	_ = approveProjectSandboxWithTTY(resolved, strings.NewReader("n\n"), &out, true)
	if !strings.Contains(out.String(), "myapp") {
		t.Errorf("prompt must show the compose project: %q", out.String())
	}
	if strings.Contains(out.String(), "\x1b[31m") {
		t.Errorf("prompt must sanitize the compose project: %q", out.String())
	}
}

// ── sub-agent posture ───────────────────────────────────────────────────

func TestChildSandboxPosture_StampsComposeProject(t *testing.T) {
	d := &delegateTasksTool{}
	recordChildSandbox([]odek.Tool{d}, sandboxConfig{Network: "none", ComposeProject: "myapp"})
	_, sp := d.childPosture()
	if sp == nil || sp.ComposeProject != "myapp" {
		t.Fatalf("posture = %+v, want ComposeProject myapp", sp)
	}
}
