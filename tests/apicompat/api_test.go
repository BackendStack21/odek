package apicompat_test

import (
	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/agent"
	"testing"
)

// Assignments verify alias identity rather than merely matching field layouts.
var (
	_ *odek.Agent                            = (*agent.Agent)(nil)
	_ odek.Config                            = agent.Config{}
	_ odek.Tool                              = (agent.Tool)(nil)
	_ func(odek.Config) (*odek.Agent, error) = odek.New
)

func TestPublicFacadePreservesRuntimeHelpers(t *testing.T) {
	if odek.ProjectFileName != "AGENTS.md" || odek.ProfileLabel("fixture-model") != "fixture-model" {
		t.Fatal("public helpers changed")
	}
	if odek.ComposeSecureSystem("identity") != agent.ComposeSecureSystem("identity") {
		t.Fatal("security policy forwarding changed")
	}
	if odek.SecurityPillar != agent.SecurityPillar {
		t.Fatal("public security policy changed")
	}
}
