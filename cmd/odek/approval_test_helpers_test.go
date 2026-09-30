package main

import (
	"github.com/BackendStack21/odek/internal/danger"
)

// promptLocalWriteConfig returns a DangerousConfig that treats local file
// writes as Prompt, so simple commands like `touch` can exercise the approval
// path deterministically without requiring sudo or network access.
func promptLocalWriteConfig() danger.DangerousConfig {
	return danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.LocalWrite: danger.Prompt,
		},
	}
}
