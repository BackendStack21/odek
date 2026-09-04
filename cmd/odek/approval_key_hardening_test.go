package main

import (
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/mcpclient"
)

// Security review wave B: persisted-approval keys must be collision-free
// and complete. Three pinned properties:
//
// 1. NUL-boundary safety — fields are length-prefixed, so a "\x00" inside
//    one field cannot reshape the hash stream into a different field split
//    (stale approval auto-applying to an attacker-chosen config).
// 2. sandbox_env VALUES are hashed — after an approval is persisted, a
//    repo editing a value (e.g. to "${ANY_HOST_SECRET}") must NOT reuse
//    the approval (host-secret exfiltration via key-names-only hashing).
// 3. sandbox_user/memory/cpus are gated — a project setting
//    sandbox_user="0:0" must change the key (and trigger approval).

func TestProjectSandboxApprovalKey_NULBoundaryNoCollision(t *testing.T) {
	// volumes ["b\x00env:c"] vs volumes ["b"] + env key "c" — identical
	// hash input under bare-NUL concatenation.
	a := config.ProjectSandboxOverride{
		HasVolumes: true,
		Volumes:    []string{"b\x00env:c"},
	}
	b := config.ProjectSandboxOverride{
		HasVolumes: true,
		Volumes:    []string{"b"},
		HasEnv:     true,
		EnvKeys:    []string{"c"},
	}
	if projectSandboxApprovalKey("/proj", a) == projectSandboxApprovalKey("/proj", b) {
		t.Fatal("NUL-boundary collision: crafted volume string hashes identically to a volumes+env split")
	}
}

func TestProjectSandboxApprovalKey_EnvValueSwapChangesKey(t *testing.T) {
	base := config.ProjectSandboxOverride{
		HasEnv:  true,
		EnvKeys: []string{"TOKEN"},
		Env:     map[string]string{"TOKEN": "benign-value"},
	}
	swapped := config.ProjectSandboxOverride{
		HasEnv:  true,
		EnvKeys: []string{"TOKEN"},
		Env:     map[string]string{"TOKEN": "${AWS_SECRET_ACCESS_KEY}"},
	}
	if projectSandboxApprovalKey("/proj", base) == projectSandboxApprovalKey("/proj", swapped) {
		t.Fatal("sandbox_env value swap reuses the persisted approval — host-secret exfiltration")
	}
}

func TestProjectSandboxApprovalKey_UserChangeChangesKey(t *testing.T) {
	base := config.ProjectSandboxOverride{HasUser: true, User: "1000:1000"}
	root := config.ProjectSandboxOverride{HasUser: true, User: "0:0"}
	if projectSandboxApprovalKey("/proj", base) == projectSandboxApprovalKey("/proj", root) {
		t.Fatal("sandbox_user change (uid mapping bypass) reuses the persisted approval")
	}
}

func TestMCPToolApprovalKey_NULBoundaryNoCollision(t *testing.T) {
	// server name "legit\x00evil" with attacker command must not collide
	// with name "legit" + a different command stream.
	a := mcpclient.ServerConfig{Command: "attacker-server"}
	b := mcpclient.ServerConfig{Command: "evil-continuation", Args: []string{"--steal"}}
	ka := mcpToolApprovalKey("/proj", "legit\x00evil", "tool", a, "schema", "desc")
	kb := mcpToolApprovalKey("/proj", "legit", "tool", b, "schema", "desc")
	if ka == kb {
		t.Fatal("NUL-in-server-name collision across different server configs")
	}
}
