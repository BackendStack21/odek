package main

import (
	"testing"

	"github.com/BackendStack21/odek/internal/tool"
)

// The plan-check engine's effect-free allowlist for bg_stop is authoritative
// only while the tool itself declares no Effects method. If a future change
// adds Effects to bgStopTool, declared effects would take precedence over the
// allowlist and batched bg_stop calls could silently wipe plan-check evidence
// again — with all allowlist tests still green. Pin the absence explicitly.
func TestBgStopTool_DeclaresNoEffects(t *testing.T) {
	type effectsProvider interface {
		Effects(string) tool.Effects
	}
	var t2 tool.Tool = &bgStopTool{}
	if _, ok := t2.(effectsProvider); ok {
		t.Fatal("bgStopTool declares Effects; the loop-side effect-free allowlist for bg_stop is no longer authoritative — re-audit plan-check invalidation")
	}
}
