package main

import (
	"testing"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
)

type postureStubTool struct{ name string }

func (s postureStubTool) Name() string        { return s.name }
func (s postureStubTool) Description() string { return "stub" }
func (s postureStubTool) Schema() any         { return map[string]any{"type": "object"} }
func (s postureStubTool) Call(string) (string, error) {
	return "", nil
}

func postureParentTools(names ...string) ([]odek.Tool, *delegateTasksTool) {
	d := &delegateTasksTool{}
	tools := []odek.Tool{d}
	for _, n := range names {
		tools = append(tools, postureStubTool{n})
	}
	return tools, d
}

func postureChildNames(p *taskToolsPosture, child ...string) map[string]bool {
	var tools []odek.Tool
	for _, n := range child {
		tools = append(tools, postureStubTool{n})
	}
	if f, ok := p.filter(); ok {
		tools = filterBuiltinTools(tools, f, nil)
	}
	out := map[string]bool{}
	for _, t := range tools {
		out[t.Name()] = true
	}
	return out
}

// A parent with no filter configured sends no filter: its child keeps tools
// the parent merely did not build (skill and background tools).
func TestChildToolPosture_NoFilterSendsNone(t *testing.T) {
	tools, d := postureParentTools("shell")
	filterBuiltinTools(tools, config.ToolConfig{}, nil)
	tp, _ := d.childPosture()
	if tp != nil {
		t.Fatalf("posture = %+v, want none", tp)
	}
	got := postureChildNames(tp, "shell", "skill_load", "bg_start")
	if !got["skill_load"] || !got["bg_start"] || !got["shell"] {
		t.Errorf("child lost tools: %v", got)
	}
}

// A deny-list is forwarded as written and removes only the denied tools from
// the child's own set.
func TestChildToolPosture_DenyListForwarded(t *testing.T) {
	tools, d := postureParentTools("shell", "write_file")
	filterBuiltinTools(tools, config.ToolConfig{Disabled: []string{"shell"}}, nil)
	tp, _ := d.childPosture()
	got := postureChildNames(tp, "shell", "write_file", "skill_load")
	if got["shell"] || !got["write_file"] || !got["skill_load"] {
		t.Errorf("child tools = %v", got)
	}
}

// An allow-list is forwarded as written: the child never gets more than it
// permits.
func TestChildToolPosture_AllowListForwarded(t *testing.T) {
	tools, d := postureParentTools("shell", "read_file")
	filterBuiltinTools(tools, config.ToolConfig{Enabled: []string{"read_file"}}, nil)
	tp, _ := d.childPosture()
	got := postureChildNames(tp, "shell", "read_file", "skill_load")
	if len(got) != 1 || !got["read_file"] {
		t.Errorf("child tools = %v, want only read_file", got)
	}
}
