package main

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/sandbox"
)

// The model, the startup line and the approval card must describe the
// network the container actually has: attached networks replace "none", so
// a run with networks never reads as "none", and internet reachability
// follows the attached networks.

func TestRED_SandboxEnvironmentBlock_NetworkMatchesAttachment(t *testing.T) {
	cases := []struct {
		name         string
		cfg          sandboxConfig
		want, absent []string
	}{
		{"none", sandboxConfig{Network: "none"},
			[]string{"- Network: none\n", "- Internet: not reachable\n"}, []string{"Attached networks"}},
		{"empty reads as none", sandboxConfig{},
			[]string{"- Network: none\n", "- Internet: not reachable\n"}, nil},
		{"bridge", sandboxConfig{Network: "bridge"},
			[]string{"- Network: bridge\n", "- Internet: reachable\n"}, nil},
		{"lenient invalid mode is shown as applied", sandboxConfig{Network: "host"},
			[]string{"- Network: none\n"}, []string{"Network: host"}},
		{"internal networks only", sandboxConfig{Network: "none", Networks: []string{"db"},
			ResolvedNetworks: []sandbox.NetworkInfo{envNet("db", true)}},
			[]string{"- Network: attached networks only\n", "- Internet: not reachable\n"}, []string{"Network: none", "Network mode"}},
		{"a non-internal network reaches the internet", sandboxConfig{Network: "none", Networks: []string{"db", "app"},
			ResolvedNetworks: []sandbox.NetworkInfo{envNet("db", true), envNet("app", false)}},
			[]string{"- Network: attached networks only\n", "- Internet: reachable\n"}, []string{"Network: none"}},
		{"bridge plus networks", sandboxConfig{Network: "bridge", Networks: []string{"db"},
			ResolvedNetworks: []sandbox.NetworkInfo{envNet("db", true)}},
			[]string{"- Network: bridge plus attached networks\n", "- Internet: reachable\n"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := sandboxEnvironmentBlock(tc.cfg, "c1")
			for _, w := range tc.want {
				if !strings.Contains(b, w) {
					t.Errorf("block missing %q:\n%s", w, b)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(b, a) {
					t.Errorf("block must not contain %q:\n%s", a, b)
				}
			}
		})
	}
}

func TestRED_SandboxPostureLine_NetworkMatchesAttachment(t *testing.T) {
	cases := []struct {
		cfg  sandboxConfig
		want string
	}{
		{sandboxConfig{}, "network=none "},
		{sandboxConfig{Network: "bridge"}, "network=bridge "},
		{sandboxConfig{Networks: []string{"app_default"}}, "network=attached "},
		{sandboxConfig{Network: "bridge", Networks: []string{"app_default"}}, "network=bridge+attached "},
	}
	for _, tc := range cases {
		if got := sandboxPostureLine(tc.cfg, "alpine:3"); !strings.Contains(got, tc.want) {
			t.Errorf("posture line for %+v = %q, want %q", tc.cfg, got, tc.want)
		}
	}
}

func TestRED_SandboxCardNote_NetworkMatchesAttachment(t *testing.T) {
	cfg := sandboxConfig{Network: "none", Networks: []string{"db"}, ResolvedNetworks: []sandbox.NetworkInfo{envNet("db", true)}}
	note := sandboxCardNote(cfg, "c1")
	if strings.Contains(note, "network: none") || !strings.Contains(note, "attached networks only") || !strings.Contains(note, "db") {
		t.Errorf("card note = %q, want the attached-networks mode and the network names", note)
	}
}
