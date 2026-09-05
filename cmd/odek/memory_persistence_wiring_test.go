package main

import (
	"os"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

// TestRED_CLIAgentConfigs_PassDangerousConfig pins the high IPI finding:
// run / continue / serve / repl / schedule construct odek.Config and call
// odek.New without DangerousConfig. The memory tool is created inside New,
// so omitting the field leaves checkPersistence fail-open (dc == nil).
// Telegram and sub-agents already pass &resolved.Dangerous; these surfaces
// must do the same.
func TestRED_CLIAgentConfigs_PassDangerousConfig(t *testing.T) {
	cases := []struct {
		file string
		name string
	}{
		{"main.go", "runCfg"},
		{"main.go", "contCfg"},
		{"serve.go", "serveCfg"},
		{"repl.go", "replCfg"},
		{"schedule.go", "schedCfg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			text := string(src)
			marker := tc.name + " := odek.Config{"
			start := strings.Index(text, marker)
			if start < 0 {
				t.Fatalf("could not find %q", marker)
			}
			newCall := "odek.New(" + tc.name + ")"
			end := strings.Index(text[start:], newCall)
			if end < 0 {
				t.Fatalf("could not find %q after %s", newCall, tc.name)
			}
			block := text[start : start+end]
			if !strings.Contains(block, "DangerousConfig:") {
				t.Errorf("%s %s is passed to odek.New without DangerousConfig — memory mutations skip the persistence gate", tc.file, tc.name)
			}
		})
	}
}

// TestRED_HeadlessDangerFloor_IsDroppedBeforeNew: scheduled runs build a
// persistence-deny floor for builtinTools, then drop it when constructing
// the agent. The floor is useless against memory.add unless it reaches New.
func TestRED_HeadlessDangerFloor_IsDroppedBeforeNew(t *testing.T) {
	floor := buildHeadlessDangerConfig(config.ResolvedConfig{})
	if floor.ActionFor(danger.Persistence) != danger.Deny {
		t.Fatalf("buildHeadlessDangerConfig Persistence = %v, want Deny", floor.ActionFor(danger.Persistence))
	}
	src, err := os.ReadFile("schedule.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	start := strings.Index(text, "schedCfg := odek.Config{")
	end := strings.Index(text[start:], "odek.New(schedCfg)")
	if start < 0 || end < 0 {
		t.Fatal("could not locate schedCfg construction")
	}
	block := text[start : start+end]
	if !strings.Contains(block, "DangerousConfig:") {
		t.Fatal("schedCfg drops the headless persistence floor: builtinTools get dangerCfg, odek.New does not")
	}
}
