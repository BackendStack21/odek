package main

import (
	"reflect"
	"testing"

	odek "github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/sandbox"
)

// A child that shares the parent's compose project must not re-resolve the
// project by label at its own time: it would pick up networks that joined the
// project after the parent resolved it. The child is limited to the networks
// the parent stamped.

func TestChildPosture_StampsResolvedComposeNetworks(t *testing.T) {
	d := &delegateTasksTool{}
	cfg := sandboxConfig{
		Network:        "none",
		ComposeProject: "p",
		Networks:       []string{"p_default", "p_db"},
		ResolvedNetworks: []sandbox.NetworkInfo{
			{Name: "p_default", ID: "a"}, {Name: "p_db", ID: "b"},
		},
	}
	recordChildSandbox([]odek.Tool{d}, cfg)
	_, sp := d.childPosture()
	if sp == nil || !reflect.DeepEqual(sp.Networks, []string{"p_default", "p_db"}) {
		t.Fatalf("posture = %+v, want the resolved compose networks stamped", sp)
	}
}

func TestApplyTaskSandboxPosture_SharedComposeProjectNotReResolved(t *testing.T) {
	r := config.ResolvedConfig{SandboxComposeProject: "p"}
	applyTaskSandboxPosture(&r, &taskSandboxPosture{ComposeProject: "p", Networks: []string{"p_default", "p_db"}})
	if r.SandboxComposeProject != "" {
		t.Errorf("child compose project = %q, want cleared (no re-resolution by label)", r.SandboxComposeProject)
	}
	if want := []string{"p_default", "p_db"}; !reflect.DeepEqual(r.SandboxNetworks, want) {
		t.Errorf("child networks = %v, want the parent's stamped list %v", r.SandboxNetworks, want)
	}
}

func TestApplyTaskSandboxPosture_ChildNeverWiderThanStampedList(t *testing.T) {
	r := config.ResolvedConfig{SandboxComposeProject: "p", SandboxNetworks: []string{"p_db", "late_joiner"}}
	applyTaskSandboxPosture(&r, &taskSandboxPosture{ComposeProject: "p", Networks: []string{"p_default", "p_db"}})
	for _, n := range r.SandboxNetworks {
		if n != "p_default" && n != "p_db" {
			t.Errorf("child network %q is outside the parent's stamped list", n)
		}
	}
	if r.SandboxComposeProject != "" {
		t.Errorf("compose project must be cleared, got %q", r.SandboxComposeProject)
	}
}

func TestApplyTaskSandboxPosture_DifferentComposeProjectGainsNothing(t *testing.T) {
	r := config.ResolvedConfig{SandboxComposeProject: "other"}
	applyTaskSandboxPosture(&r, &taskSandboxPosture{ComposeProject: "p", Networks: []string{"p_default"}})
	if r.SandboxComposeProject != "" || len(r.SandboxNetworks) != 0 {
		t.Errorf("a child with another project got project=%q networks=%v", r.SandboxComposeProject, r.SandboxNetworks)
	}
}

func TestApplyTaskSandboxPosture_ChildComposeFromEmptyParentGainsNothing(t *testing.T) {
	r := config.ResolvedConfig{SandboxComposeProject: "p"}
	applyTaskSandboxPosture(&r, &taskSandboxPosture{})
	if r.SandboxComposeProject != "" || len(r.SandboxNetworks) != 0 {
		t.Errorf("a child must not widen from a parent without networks: project=%q networks=%v", r.SandboxComposeProject, r.SandboxNetworks)
	}
}
