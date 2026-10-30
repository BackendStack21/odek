package main

import (
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

// RED: the Telegram daemon built its agent with the operator's raw global
// danger config and starts no sandbox container, so a global
// `code_execution: allow` (tuned for an interactive terminal session) let
// remote chat input run attacker-steered shell on the host with no per-action
// gate. Telegram does have a human — the per-turn chat approver — so the
// right posture is not a hard deny: an interactive-grade `allow` for the
// classes that execute code is downgraded to `prompt`, so the person in the
// chat approves each one. Everything the operator set explicitly below
// `allow` is left exactly as configured.
func TestRED_TelegramDangerConfig_DowngradesCodeGrantsToPrompt(t *testing.T) {
	resolved := config.ResolvedConfig{
		Dangerous: danger.DangerousConfig{
			Classes: map[danger.RiskClass]danger.Action{
				danger.CodeExecution: danger.Allow,
				danger.Install:       danger.Allow,
				danger.NetworkEgress: danger.Allow, // plain egress is not code; untouched
				danger.SystemWrite:   danger.Prompt,
				danger.Destructive:   danger.Deny,
			},
		},
	}
	cfg := buildTelegramDangerConfig(resolved)
	for _, cls := range []danger.RiskClass{danger.CodeExecution, danger.Install} {
		if got := cfg.Classes[cls]; got != danger.Prompt {
			t.Errorf("%s: global allow must become prompt on the telegram surface, got %s", cls, got)
		}
	}
	if got := cfg.Classes[danger.NetworkEgress]; got != danger.Allow {
		t.Errorf("network_egress must be left as configured, got %s", got)
	}
	if got := cfg.Classes[danger.SystemWrite]; got != danger.Prompt {
		t.Errorf("explicit prompt must be left alone, got %s", got)
	}
	if got := cfg.Classes[danger.Destructive]; got != danger.Deny {
		t.Errorf("explicit deny must be left alone, got %s", got)
	}
}

func TestTelegramDangerConfig_LeavesPromptAndDenyUntouched(t *testing.T) {
	resolved := config.ResolvedConfig{
		Dangerous: danger.DangerousConfig{
			Classes: map[danger.RiskClass]danger.Action{
				danger.CodeExecution: danger.Deny,
				danger.Install:       danger.Prompt,
			},
		},
	}
	cfg := buildTelegramDangerConfig(resolved)
	if got := cfg.Classes[danger.CodeExecution]; got != danger.Deny {
		t.Errorf("an explicit deny must never be loosened, got %s", got)
	}
	if got := cfg.Classes[danger.Install]; got != danger.Prompt {
		t.Errorf("an explicit prompt must stay prompt, got %s", got)
	}
}

// resolved.Dangerous is shared by every chat and turn in the daemon process;
// the telegram copy must never write through to it.
func TestRED_TelegramDangerConfig_DoesNotMutateSharedConfig(t *testing.T) {
	resolved := config.ResolvedConfig{
		Dangerous: danger.DangerousConfig{
			Classes: map[danger.RiskClass]danger.Action{danger.CodeExecution: danger.Allow},
		},
	}
	_ = buildTelegramDangerConfig(resolved)
	if got := resolved.Dangerous.Classes[danger.CodeExecution]; got != danger.Allow {
		t.Errorf("shared global config was mutated: code_execution now %s", got)
	}
}

// With no classes configured at all the copy must still be usable (nil map
// safe) and must not invent grants.
func TestTelegramDangerConfig_NilClassesSafe(t *testing.T) {
	cfg := buildTelegramDangerConfig(config.ResolvedConfig{})
	if cfg.Classes[danger.CodeExecution] == danger.Allow {
		t.Errorf("empty config must not grant code_execution")
	}
}
