package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func TestDocs_SandboxingNetworkModes(t *testing.T) {
	doc := readRepoFile(t, "docs/SANDBOXING.md")
	for _, stale := range []string{
		"no access to `localhost:port` on the host",
		"isolates it from the host's network stack",
		"are downgraded to `none` at container start",
		"forces `none` with a warning",
	} {
		if strings.Contains(doc, stale) {
			t.Errorf("SANDBOXING.md still carries the old claim %q", stale)
		}
	}
	for _, want := range []string{
		"sandbox_networks", "ODEK_SANDBOX_NETWORKS", "--sandbox-networks",
		"sandbox_compose_project", "--sandbox-compose",
		"ODEK_SANDBOX_NETWORK_LENIENT", "sandbox_pids_limit",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("SANDBOXING.md does not document %q", want)
		}
	}
	// bridge reaches host services: the doc must say so.
	if !regexp.MustCompile(`(?i)host\.docker\.internal|metadata|IMDS|169\.254`).MatchString(doc) {
		t.Error("SANDBOXING.md must state that bridge can reach host-side services / the metadata endpoint")
	}
}

func TestDocs_SandboxingDefaultImagePinned(t *testing.T) {
	doc := readRepoFile(t, "docs/SANDBOXING.md")
	const digest = "sha256:2e0b81d60597f449baa56dc25d355367869d2dca4cd57e866f1e2b52af3eb55f"
	if !strings.Contains(doc, digest) {
		t.Errorf("SANDBOXING.md must record the pinned default image digest %s", digest)
	}
	if !strings.Contains(doc, "docker buildx imagetools inspect alpine:3.22") {
		t.Error("SANDBOXING.md must record how the digest was looked up")
	}
	for _, line := range strings.Split(doc, "\n") {
		if strings.Contains(line, "`sandbox_image` | `ODEK_SANDBOX_IMAGE`") && strings.Contains(line, "alpine:latest") {
			t.Errorf("sandbox_image default still documented as alpine:latest: %s", line)
		}
		if strings.Contains(line, "Neither") && strings.Contains(line, "alpine:latest") {
			t.Errorf("image priority still falls back to alpine:latest: %s", line)
		}
	}
}

func TestDocs_ConfigAndSecurityCoverNewKnobs(t *testing.T) {
	cfgDoc := readRepoFile(t, "docs/CONFIG.md")
	for _, want := range []string{"sandbox_networks", "ODEK_SANDBOX_NETWORKS", "--sandbox-networks",
		"sandbox_compose_project", "ODEK_SANDBOX_COMPOSE_PROJECT", "--sandbox-compose",
		"sandbox_pids_limit", "ODEK_SANDBOX_PIDS_LIMIT"} {
		if !strings.Contains(cfgDoc, want) {
			t.Errorf("CONFIG.md does not document %q", want)
		}
	}
	sec := readRepoFile(t, "docs/SECURITY.md")
	for _, want := range []string{"sandbox_networks", "sandbox_compose_project", "ODEK_SANDBOX_NETWORK_LENIENT"} {
		if !strings.Contains(sec, want) {
			t.Errorf("SECURITY.md does not mention %q", want)
		}
	}
	if strings.Contains(sec, "`host` is coerced back to `none` with a warning") {
		t.Error("SECURITY.md still claims host is coerced to none")
	}
}

func TestDocs_AgentsGuide(t *testing.T) {
	agents := readRepoFile(t, "AGENTS.md")
	if strings.Contains(agents, "/api/diagnostics") {
		t.Error("AGENTS.md mentions a /api/diagnostics endpoint that does not exist")
	}
	for _, want := range []string{"sandbox_environment.go", "sandbox_compose.go", "sandbox_networks"} {
		if !strings.Contains(agents, want) {
			t.Errorf("AGENTS.md does not mention %q", want)
		}
	}
}

// Comments describe behaviour, not plan tickets.
func TestSandboxNetworkSources_NoPlanTicketIDs(t *testing.T) {
	ticket := regexp.MustCompile(`\b(SL0\d|AC-\d+|REQ-\d+|PR [1-6]\b)`)
	for _, rel := range []string{
		"cmd/odek/sandbox_environment.go", "cmd/odek/sandbox_compose.go",
		"internal/sandbox/networks.go", "internal/sandbox/compose.go", "internal/sandbox/network.go",
	} {
		if loc := ticket.FindString(readRepoFile(t, rel)); loc != "" {
			t.Errorf("%s names a plan ticket %q", rel, loc)
		}
	}
}

// introspect.go keeps standard-library imports in their own group, ahead of
// module imports.
func TestIntrospectImportGrouping(t *testing.T) {
	src := readRepoFile(t, "cmd/odek/introspect.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "introspect.go", src, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	lastLine := 0
	seenModule := false
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		line := fset.Position(imp.Pos()).Line
		isModule := strings.Contains(strings.SplitN(path, "/", 2)[0], ".")
		if isModule {
			seenModule = true
		} else if seenModule {
			t.Errorf("standard import %q follows a module import (line %d)", path, line)
		}
		if !isModule && lastLine != 0 && line != lastLine+1 {
			t.Errorf("standard import %q is separated from the standard group (line %d)", path, line)
		}
		if !isModule {
			lastLine = line
		}
	}
}
