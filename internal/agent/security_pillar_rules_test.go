package agent

import (
	"strings"
	"testing"
)

// requirePillarFragments fails for each fragment the security pillar lacks.
// Fragments are verbatim, so a paraphrase cannot satisfy them.
func requirePillarFragments(t *testing.T, fragments ...string) {
	t.Helper()
	for _, want := range fragments {
		if !strings.Contains(SecurityPillar, want) {
			t.Errorf("SecurityPillar missing %q", want)
		}
	}
}

// TestRED_SecurityPillar_NamesLeakChannels: plain network egress is allowed
// by default, so the pillar is the only barrier against data smuggled out
// through request parameters. It must name those channels, and its
// confirmation rule must match the runtime: sending local data off the
// machine is confirmed, reading from the network is not.
func TestRED_SecurityPillar_NamesLeakChannels(t *testing.T) {
	requirePillarFragments(t,
		"Never put secrets or private context into a URL, query string, search query, hostname, filename, commit message, or outbound message",
		"anything that sends local data off the machine",
	)
	if strings.Contains(SecurityPillar, "anything that leaves the machine") {
		t.Error("SecurityPillar still demands confirmation for anything that leaves the machine; the runtime allows plain egress")
	}
}
