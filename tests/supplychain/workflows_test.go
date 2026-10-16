// Package supplychain_test pins the CI supply-chain posture: every third-party
// action is referenced by full commit SHA, tools run in CI are version-pinned,
// and dependency updates are proposed by Dependabot instead of floating refs.
package supplychain_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	usesLine   = regexp.MustCompile(`^\s*(?:-\s*)?uses:\s*(\S+)(.*)$`)
	pinnedUses = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40}$`)
	versionTag = regexp.MustCompile(`#\s*v\d+\.\d+\.\d+\b`)
	floatingGo = regexp.MustCompile(`go (?:run|install)\s+\S+@(?:latest|master|main)\b`)
)

func workflowFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no workflow files found: %v", err)
	}
	return files
}

func TestRED_WorkflowActionsArePinnedBySHA(t *testing.T) {
	for _, f := range workflowFiles(t) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			m := usesLine.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			ref := m[1]
			if strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "docker://") {
				continue
			}
			if !pinnedUses.MatchString(ref) {
				t.Errorf("%s:%d: action %q is not pinned to a full commit SHA", filepath.Base(f), i+1, ref)
			}
			if !versionTag.MatchString(m[2]) {
				t.Errorf("%s:%d: pinned action %q lacks a `# vX.Y.Z` comment naming the release", filepath.Base(f), i+1, ref)
			}
		}
	}
}

func TestRED_WorkflowToolsAreVersionPinned(t *testing.T) {
	for _, f := range workflowFiles(t) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if floatingGo.MatchString(line) {
				t.Errorf("%s:%d: tool installed from a floating ref: %s", filepath.Base(f), i+1, strings.TrimSpace(line))
			}
		}
	}
}

func TestRED_CheckoutDoesNotPersistCredentials(t *testing.T) {
	for _, f := range workflowFiles(t) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			m := usesLine.FindStringSubmatch(line)
			if m == nil || !strings.HasPrefix(m[1], "actions/checkout@") {
				continue
			}
			found := false
			for j := i + 1; j < len(lines) && j <= i+4; j++ {
				if strings.Contains(lines[j], "persist-credentials: false") {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s:%d: actions/checkout must set persist-credentials: false", filepath.Base(f), i+1)
			}
		}
	}
}

func TestRED_DependabotCoversActionsAndGoModules(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "dependabot.yml"))
	if err != nil {
		t.Fatalf("dependabot config missing: %v", err)
	}
	cfg := string(data)
	for _, eco := range []string{`package-ecosystem: "github-actions"`, `package-ecosystem: "gomod"`} {
		if !strings.Contains(cfg, eco) {
			t.Errorf("dependabot.yml does not cover %s", eco)
		}
	}
}
