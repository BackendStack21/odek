package main

import (
	"regexp"
	"sort"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
)

// taskToolsPosture is the parent's effective tool allow-list as written into
// a task file: the names of the tools that survived the parent's own
// filtering. A child intersects its configuration with it.
type taskToolsPosture struct {
	Enabled []string `json:"enabled"`
}

// filter returns the child-side tool filter for this posture.
func (p *taskToolsPosture) filter() (config.ToolConfig, bool) {
	if p == nil || p.Enabled == nil {
		return config.ToolConfig{}, false
	}
	return config.ToolConfig{Enabled: p.Enabled}, true
}

// taskSandboxPosture is the parent's container posture as written into a
// task file. Empty strings mean "not set by the parent".
type taskSandboxPosture struct {
	Readonly bool   `json:"readonly,omitempty"`
	Image    string `json:"image,omitempty"`
	Network  string `json:"network,omitempty"`
	Memory   string `json:"memory,omitempty"`
	CPUs     string `json:"cpus,omitempty"`
	User     string `json:"user,omitempty"`
	// ApprovalKeys are the project-sandbox approval keys the parent was
	// granted this session (project directory + content hash bound).
	ApprovalKeys []string `json:"approval_keys,omitempty"`
}

var approvalKeyPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const maxTaskApprovalKeys = 16

// recordChildToolAllow stamps the names that survived the parent's tool
// filter onto every delegate_tasks tool in the unfiltered list.
func recordChildToolAllow(all, survivors []odek.Tool) {
	names := make([]string, 0, len(survivors))
	for _, t := range survivors {
		names = append(names, t.Name())
	}
	for _, t := range all {
		if d, ok := t.(*delegateTasksTool); ok {
			d.setChildToolAllow(names)
		}
	}
}

func (t *delegateTasksTool) setChildToolAllow(names []string) {
	t.sessMu.Lock()
	defer t.sessMu.Unlock()
	t.childToolAllow = append([]string{}, names...)
	t.childToolAllowSet = true
}

// recordChildSandbox stamps the parent's container settings onto every
// delegate_tasks tool in the list.
func recordChildSandbox(tools []odek.Tool, cfg sandboxConfig) {
	for _, t := range tools {
		if d, ok := t.(*delegateTasksTool); ok {
			d.sessMu.Lock()
			d.childSandbox = &taskSandboxPosture{
				Readonly: cfg.Readonly, Image: cfg.Image, Network: cfg.Network,
				Memory: cfg.Memory, CPUs: cfg.CPUs, User: cfg.User,
			}
			d.sessMu.Unlock()
		}
	}
}

// childPosture returns the posture to stamp into a task file.
func (t *delegateTasksTool) childPosture() (*taskToolsPosture, *taskSandboxPosture) {
	t.sessMu.Lock()
	defer t.sessMu.Unlock()
	var tp *taskToolsPosture
	if t.childToolAllowSet {
		tp = &taskToolsPosture{Enabled: append([]string{}, t.childToolAllow...)}
	}
	var sp *taskSandboxPosture
	if t.childSandbox != nil {
		c := *t.childSandbox
		c.ApprovalKeys = sessionSandboxApprovalKeys()
		sp = &c
	} else if t.sandboxed {
		sp = &taskSandboxPosture{ApprovalKeys: sessionSandboxApprovalKeys()}
	}
	return tp, sp
}

// applyTaskSandboxPosture makes the child's resolved sandbox settings at
// least as strict as the parent's and imports the parent's granted approval
// keys. A read-only parent mount stays read-only; any explicit parent value
// replaces the child's own.
func applyTaskSandboxPosture(resolved *config.ResolvedConfig, p *taskSandboxPosture) {
	if p == nil {
		return
	}
	if p.Readonly {
		resolved.SandboxReadonly = true
	}
	if p.Image != "" {
		resolved.SandboxImage = p.Image
	}
	if p.Network != "" {
		resolved.SandboxNetwork = p.Network
	}
	if p.Memory != "" {
		resolved.SandboxMemory = p.Memory
	}
	if p.CPUs != "" {
		resolved.SandboxCPUs = p.CPUs
	}
	if p.User != "" {
		resolved.SandboxUser = p.User
	}
	n := 0
	for _, k := range p.ApprovalKeys {
		if n >= maxTaskApprovalKeys {
			break
		}
		if approvalKeyPattern.MatchString(k) {
			recordSessionDockerfileApproval(k)
			n++
		}
	}
}

// sessionSandboxApprovalKeys lists the approval keys granted in this
// process, sorted for stable task files.
func sessionSandboxApprovalKeys() []string {
	sessionDockerfileApprovalsMu.Lock()
	defer sessionDockerfileApprovalsMu.Unlock()
	keys := make([]string, 0, len(sessionDockerfileApprovals))
	for k, ok := range sessionDockerfileApprovals {
		if ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > maxTaskApprovalKeys {
		keys = keys[:maxTaskApprovalKeys]
	}
	return keys
}
