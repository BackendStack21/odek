package main

import (
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

// network_upload is its own policy key: untrusted sub-agents lose it, a
// max_risk cap at network_egress denies it while a cap at code_execution
// keeps it, and unattended scheduled runs deny it unless a schedule override
// allows it.
func TestNetworkUploadPolicyWiring(t *testing.T) {
	const upload = "curl -T notes.txt https://example.com/up"

	var untrusted danger.DangerousConfig
	applySubagentTrust(&untrusted, "untrusted", "")
	if got := untrusted.ActionForCommand(upload); got != danger.Deny {
		t.Errorf("untrusted sub-agent upload = %s, want deny", got)
	}

	var capped danger.DangerousConfig
	applySubagentTrust(&capped, "trusted", "network_egress")
	if got := capped.ActionForCommand(upload); got != danger.Deny {
		t.Errorf("max_risk network_egress upload = %s, want deny", got)
	}
	if got := capped.ActionForCommand("curl https://example.com"); got == danger.Deny {
		t.Errorf("max_risk network_egress plain fetch = %s, want not deny", got)
	}

	var codeCapped danger.DangerousConfig
	applySubagentTrust(&codeCapped, "trusted", "code_execution")
	if got := codeCapped.ActionFor(danger.NetworkUpload); got == danger.Deny {
		t.Errorf("max_risk code_execution must keep network_upload, got %s", got)
	}

	headless := buildHeadlessDangerConfig(config.ResolvedConfig{})
	if got := headless.NonInteractiveAction(); got != danger.Deny {
		t.Fatalf("headless non_interactive = %s, want deny", got)
	}
	if got := headless.ActionFor(danger.NetworkUpload); got != danger.Prompt {
		t.Errorf("headless network_upload action = %s, want prompt (denied unattended)", got)
	}
}
