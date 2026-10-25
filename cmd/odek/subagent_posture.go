package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/sandbox"
)

// taskToolsPosture is the parent's configured tool filter as written into a
// task file: the allow and deny sets the parent itself was configured with.
// It is absent when the parent has no filter, so a child then gets its normal
// tool set. A child applies it on top of its own configuration.
type taskToolsPosture struct {
	Enabled  []string `json:"enabled"`
	Disabled []string `json:"disabled,omitempty"`
}

// filter returns the child-side tool filter for this posture.
func (p *taskToolsPosture) filter() (config.ToolConfig, bool) {
	if p == nil || (p.Enabled == nil && len(p.Disabled) == 0) {
		return config.ToolConfig{}, false
	}
	return config.ToolConfig{Enabled: p.Enabled, Disabled: p.Disabled}, true
}

// mergeToolFilters combines two filters so the result permits only what both
// permit: allow-lists intersect, deny-lists union.
func mergeToolFilters(a, b config.ToolConfig) config.ToolConfig {
	out := config.ToolConfig{}
	switch {
	case a.Enabled == nil:
		out.Enabled = b.Enabled
	case b.Enabled == nil:
		out.Enabled = a.Enabled
	default:
		in := make(map[string]bool, len(b.Enabled))
		for _, n := range b.Enabled {
			in[n] = true
		}
		out.Enabled = []string{}
		for _, n := range a.Enabled {
			if in[n] {
				out.Enabled = append(out.Enabled, n)
			}
		}
	}
	out.Disabled = append(append([]string{}, a.Disabled...), b.Disabled...)
	if len(out.Disabled) == 0 {
		out.Disabled = nil
	}
	return out
}

// taskSandboxPosture is the parent's container posture as written into a
// task file. Empty strings mean "not set by the parent".
type taskSandboxPosture struct {
	Readonly       bool     `json:"readonly,omitempty"`
	Image          string   `json:"image,omitempty"`
	Network        string   `json:"network,omitempty"`
	Memory         string   `json:"memory,omitempty"`
	CPUs           string   `json:"cpus,omitempty"`
	PidsLimit      string   `json:"pids_limit,omitempty"`
	Networks       []string `json:"networks,omitempty"`
	ComposeProject string   `json:"compose_project,omitempty"`
	User           string   `json:"user,omitempty"`
	// ApprovalKeys are the project-sandbox approval keys the parent was
	// granted this session (project directory + content hash bound).
	ApprovalKeys []string `json:"approval_keys,omitempty"`
}

var approvalKeyPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const maxTaskApprovalKeys = 16

// recordChildToolFilter stamps the parent's configured tool filter onto every
// delegate_tasks tool in the list. An unconfigured filter clears it, so the
// child gets its normal tool set.
func recordChildToolFilter(all []odek.Tool, cfg config.ToolConfig) {
	for _, t := range all {
		if d, ok := t.(*delegateTasksTool); ok {
			d.setChildToolFilter(cfg)
		}
	}
}

func (t *delegateTasksTool) setChildToolFilter(cfg config.ToolConfig) {
	t.sessMu.Lock()
	defer t.sessMu.Unlock()
	if cfg.Enabled == nil && len(cfg.Disabled) == 0 {
		t.childToolFilter = nil
		return
	}
	c := config.ToolConfig{Disabled: append([]string(nil), cfg.Disabled...)}
	if cfg.Enabled != nil {
		c.Enabled = append([]string{}, cfg.Enabled...)
	}
	t.childToolFilter = &c
}

// recordChildSandbox stamps the parent's container settings onto every
// delegate_tasks tool in the list.
func recordChildSandbox(tools []odek.Tool, cfg sandboxConfig) {
	for _, t := range tools {
		if d, ok := t.(*delegateTasksTool); ok {
			d.sessMu.Lock()
			d.childSandbox = &taskSandboxPosture{
				Readonly: cfg.Readonly, Image: cfg.Image, Network: cfg.Network,
				Memory: cfg.Memory, CPUs: cfg.CPUs, PidsLimit: cfg.PidsLimit, Networks: cfg.Networks, ComposeProject: cfg.ComposeProject, User: cfg.User,
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
	if f := t.childToolFilter; f != nil {
		tp = &taskToolsPosture{Enabled: f.Enabled, Disabled: f.Disabled}
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
	if p.PidsLimit != "" {
		resolved.SandboxPidsLimit = lowerPidsLimit(p.PidsLimit, resolved.SandboxPidsLimit)
	}
	// The parent stamps the networks it resolved (compose members included).
	// A child never re-resolves a project by label, which could attach a
	// network that joined after the parent resolved it: it keeps a subset of
	// the stamped list, and inherits that list when it names the same project.
	shared := p.ComposeProject != "" && resolved.SandboxComposeProject == p.ComposeProject
	nets := intersectNames(resolved.SandboxNetworks, p.Networks)
	if shared {
		nets = mergeNames(p.Networks, nets)
	}
	resolved.SandboxNetworks = nets
	resolved.SandboxComposeProject = ""
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

// overrideGrants holds the project-override approval ("y" = once) granted
// for a project directory. It is never consulted by this process's own gate;
// it exists only so a spawned child can be handed the grant.
var (
	overrideGrantsMu sync.Mutex
	overrideGrants   = map[string]string{}
)

func recordOverrideGrant(projectDir, key string) {
	overrideGrantsMu.Lock()
	defer overrideGrantsMu.Unlock()
	overrideGrants[projectDir] = key
}

// sessionSandboxApprovalKeys lists the approvals to hand a child: only keys
// for THIS working directory — the Dockerfile.odek approval for its current
// content hash and the project-override grant for the directory — so a stale
// grant for another directory or an edited Dockerfile is never forwarded.
func sessionSandboxApprovalKeys() []string {
	wd, err := os.Getwd()
	if err != nil {
		return nil
	}
	wd, err = filepath.Abs(wd)
	if err != nil {
		return nil
	}
	var keys []string
	if data, err := os.ReadFile(sandbox.DockerfileName); err == nil {
		sum := sha256.Sum256(data)
		if k := dockerfileApprovalKey(wd, hex.EncodeToString(sum[:])); sessionDockerfileApproved(k) {
			keys = append(keys, k)
		}
	}
	overrideGrantsMu.Lock()
	if k, ok := overrideGrants[wd]; ok {
		keys = append(keys, k)
	}
	overrideGrantsMu.Unlock()
	return keys
}

// lowerPidsLimit returns the smaller of the parent and child limits. An
// empty or unparsable child value yields the parent's, so a child never
// runs with a higher process limit than the sandbox it was spawned from.
func lowerPidsLimit(parent, child string) string {
	p, perr := strconv.Atoi(parent)
	c, cerr := strconv.Atoi(child)
	if perr != nil || p <= 0 {
		return child
	}
	if cerr != nil || c <= 0 || c > p {
		return parent
	}
	return child
}

// intersectNames keeps the child names that the parent also has, in the
// child's order. A child never gains a name from the parent.
func intersectNames(child, parent []string) []string {
	allowed := make(map[string]bool, len(parent))
	for _, n := range parent {
		allowed[n] = true
	}
	var out []string
	for _, n := range child {
		if allowed[n] {
			out = append(out, n)
		}
	}
	return out
}

// mergeNames returns base followed by the names of extra that base lacks.
func mergeNames(base, extra []string) []string {
	out := append([]string(nil), base...)
	seen := make(map[string]bool, len(base))
	for _, n := range base {
		seen[n] = true
	}
	for _, n := range extra {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}
