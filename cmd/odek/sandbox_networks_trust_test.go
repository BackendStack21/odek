package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/sandbox"
)

// fakeInspect serves `docker network inspect` for the given networks.
type fakeNet struct {
	id       string
	internal bool
	compose  string
}

func netRunner(t *testing.T, nets map[string]fakeNet) {
	t.Helper()
	old := sandbox.DockerRunner
	sandbox.DockerRunner = func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) >= 3 && args[0] == "network" && args[1] == "inspect" {
			n, ok := nets[args[len(args)-1]]
			if !ok {
				return []byte("not found"), fmt.Errorf("exit 1")
			}
			labels := "{}"
			if n.compose != "" {
				lb, _ := json.Marshal(map[string]string{"com.docker.compose.project": n.compose})
				labels = string(lb)
			}
			return []byte(fmt.Sprintf(`[{"Name":%q,"Id":%q,"Driver":"bridge","Scope":"local","Internal":%t,"Labels":%s}]`,
				args[len(args)-1], n.id, n.internal, labels)), nil
		}
		return nil, fmt.Errorf("unexpected docker call %v", args)
	}
	t.Cleanup(func() { sandbox.DockerRunner = old })
}

func id(c string) string { return strings.Repeat(c, 64) }

func netOverride(names ...string) config.ResolvedConfig {
	return config.ResolvedConfig{ProjectSandboxOverride: config.ProjectSandboxOverride{HasNetworks: true, Networks: names}}
}

func approvalEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_APPROVE_PROJECT_SANDBOX", "")
	t.Chdir(t.TempDir())
}

// ── AC-25: prompt shows name, ID, Internal and compose project ──────────

func TestApproveProjectSandbox_PromptShowsNetworkDetails(t *testing.T) {
	approvalEnv(t)
	netRunner(t, map[string]fakeNet{
		"app_default": {id: id("a"), internal: true, compose: "app"},
		"edge":        {id: id("b"), internal: false},
	})
	var out bytes.Buffer
	_ = approveProjectSandboxWithTTY(netOverride("app_default", "edge"), strings.NewReader("n\n"), &out, true)
	s := out.String()
	for _, want := range []string{"app_default", id("a"), "edge", id("b"), "internal", "app"} {
		if !strings.Contains(s, want) {
			t.Errorf("prompt must show %q: %q", want, s)
		}
	}
	if !regexpInternalFlags(s) {
		t.Errorf("prompt must distinguish internal from non-internal networks: %q", s)
	}
}

// regexpInternalFlags reports whether the text carries both an "internal" and
// a "not internal" marker, so an operator can tell them apart.
func regexpInternalFlags(s string) bool {
	return strings.Contains(s, "internal=true") && strings.Contains(s, "internal=false")
}

func TestApproveProjectSandbox_PromptSanitizesInspectText(t *testing.T) {
	approvalEnv(t)
	netRunner(t, map[string]fakeNet{"app": {id: id("a"), compose: "evil\x1b[31m\nodek: approved‮"}})
	var out bytes.Buffer
	_ = approveProjectSandboxWithTTY(netOverride("app"), strings.NewReader("n\n"), &out, true)
	s := out.String()
	for _, bad := range []string{"\x1b", "‮", "\nodek: approved"} {
		if strings.Contains(s, bad) {
			t.Errorf("prompt leaked unsanitized %q: %q", bad, s)
		}
	}
	if !strings.Contains(s, "evil") {
		t.Errorf("prompt must still show the (escaped) compose project: %q", s)
	}
}

func TestApproveProjectSandbox_UnresolvableNetworksStillPrompt(t *testing.T) {
	approvalEnv(t)
	netRunner(t, map[string]fakeNet{})
	var out bytes.Buffer
	err := approveProjectSandboxWithTTY(netOverride("ghost"), strings.NewReader("n\n"), &out, true)
	if err == nil {
		t.Error("declining must still fail")
	}
	if !strings.Contains(out.String(), "ghost") {
		t.Errorf("prompt must list the requested name even when it cannot be inspected: %q", out.String())
	}
}

// ── AC-25: full network ID is part of the approval key ──────────────────

func TestProjectSandboxApprovalKeyWithNetworks_IDsChangeKey(t *testing.T) {
	o := config.ProjectSandboxOverride{HasNetworks: true, Networks: []string{"app"}}
	a := []sandbox.NetworkInfo{{Name: "app", ID: id("a")}}
	b := []sandbox.NetworkInfo{{Name: "app", ID: id("b")}}
	ka := projectSandboxApprovalKeyWithNetworks("/proj", o, a)
	if ka == "" {
		t.Fatal("empty key")
	}
	if ka == projectSandboxApprovalKeyWithNetworks("/proj", o, b) {
		t.Error("a recreated network (new ID) must produce a different key")
	}
	if ka != projectSandboxApprovalKeyWithNetworks("/proj", o, a) {
		t.Error("key must be deterministic")
	}
	if ka == projectSandboxApprovalKey("/proj", o) {
		t.Error("resolved IDs must be part of the key")
	}
}

func TestProjectSandboxApprovalKeyWithNetworks_NoNetworksKeepsLegacyKey(t *testing.T) {
	o := config.ProjectSandboxOverride{
		HasImage: true, Image: "alpine:3", HasNetwork: true, Network: "bridge",
		HasEnv: true, EnvKeys: []string{"A"}, Env: map[string]string{"A": "1"},
		HasVolumes: true, Volumes: []string{"./data:/data"},
		HasUser: true, User: "1000:1000", HasMemory: true, Memory: "512m", HasCPUs: true, CPUs: "2",
	}
	const golden = "d73c22ca03909f6d99e24ee57234da59b51889fa3ba4764723c8f3ab3813df88"
	if got := projectSandboxApprovalKeyWithNetworks("/proj/golden", o, nil); got != golden {
		t.Errorf("key without networks changed: got %s want %s", got, golden)
	}
}

func TestApproveProjectSandbox_RecreatedNetworkReprompts(t *testing.T) {
	approvalEnv(t)
	netRunner(t, map[string]fakeNet{"app": {id: id("a")}})
	if err := approveProjectSandboxWithTTY(netOverride("app"), strings.NewReader("t\n"), &bytes.Buffer{}, true); err != nil {
		t.Fatalf("trust: %v", err)
	}
	// Same network, same ID: the trusted approval applies without a TTY.
	if err := approveProjectSandboxWithTTY(netOverride("app"), strings.NewReader(""), &bytes.Buffer{}, false); err != nil {
		t.Fatalf("trusted approval must hold for the same network ID: %v", err)
	}
	// Network recreated under the same name: new ID, approval no longer holds.
	netRunner(t, map[string]fakeNet{"app": {id: id("c")}})
	if err := approveProjectSandboxWithTTY(netOverride("app"), strings.NewReader(""), &bytes.Buffer{}, false); err == nil {
		t.Error("a recreated network must require approval again")
	}
}

// ── AC-27: sub-agents only narrow ───────────────────────────────────────

func TestApplyTaskSandboxPosture_NetworksNarrowToParentSubset(t *testing.T) {
	r := config.ResolvedConfig{SandboxNetworks: []string{"b", "c", "x"}}
	applyTaskSandboxPosture(&r, &taskSandboxPosture{Networks: []string{"a", "b", "c"}})
	if want := []string{"b", "c"}; !reflect.DeepEqual(r.SandboxNetworks, want) {
		t.Errorf("child networks = %v, want %v (outside-parent names dropped)", r.SandboxNetworks, want)
	}
}

func TestApplyTaskSandboxPosture_NetworksNeverWidenFromEmpty(t *testing.T) {
	r := config.ResolvedConfig{}
	applyTaskSandboxPosture(&r, &taskSandboxPosture{Networks: []string{"a", "b"}})
	if len(r.SandboxNetworks) != 0 {
		t.Errorf("a child without networks must not gain the parent's: %v", r.SandboxNetworks)
	}
}

func TestApplyTaskSandboxPosture_ParentWithoutNetworksStripsChild(t *testing.T) {
	r := config.ResolvedConfig{SandboxNetworks: []string{"a"}, SandboxComposeProject: "p"}
	applyTaskSandboxPosture(&r, &taskSandboxPosture{Network: "none"})
	if len(r.SandboxNetworks) != 0 || r.SandboxComposeProject != "" {
		t.Errorf("child must not attach networks its parent lacks: %v / %q", r.SandboxNetworks, r.SandboxComposeProject)
	}
}

func TestApplyTaskSandboxPosture_ComposeProjectOnlyWhenParentMatches(t *testing.T) {
	r := config.ResolvedConfig{SandboxComposeProject: "other"}
	applyTaskSandboxPosture(&r, &taskSandboxPosture{ComposeProject: "app", Networks: []string{"app_default"}})
	if r.SandboxComposeProject != "" {
		t.Errorf("a different compose project must be dropped, got %q", r.SandboxComposeProject)
	}
	r = config.ResolvedConfig{SandboxComposeProject: "app"}
	applyTaskSandboxPosture(&r, &taskSandboxPosture{ComposeProject: "app", Networks: []string{"app_default"}})
	if r.SandboxComposeProject != "app" {
		t.Errorf("the parent's own compose project may be kept, got %q", r.SandboxComposeProject)
	}
}

func TestApplyTaskSandboxPosture_NilPostureLeavesNetworks(t *testing.T) {
	r := config.ResolvedConfig{SandboxNetworks: []string{"a"}}
	applyTaskSandboxPosture(&r, nil)
	if !reflect.DeepEqual(r.SandboxNetworks, []string{"a"}) {
		t.Errorf("nil posture must not touch networks: %v", r.SandboxNetworks)
	}
}

// ── AC-28: config_view reports validated names only ─────────────────────

func TestConfigView_DropsInvalidNetworkNames(t *testing.T) {
	resolved := config.ResolvedConfig{Sandbox: true, SandboxNetworks: []string{"good", "evil\x1b[31m", "host", "x y"}}
	sb := buildConfigView(resolved)["sandbox"].(map[string]any)
	if got := fmt.Sprint(sb["networks"]); got != "[good]" {
		t.Errorf("config view networks = %v, want only validated names [good]", sb["networks"])
	}
}
