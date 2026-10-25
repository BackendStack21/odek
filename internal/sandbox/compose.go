package sandbox

import (
	"context"
	"strings"
)

// ValidateComposeProject checks a Compose project name with the same
// character rules as network names. An empty name is allowed and returned
// unchanged.
func ValidateComposeProject(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	if !networkNameRE.MatchString(name) {
		return "", invalidNetwork("sandbox compose project %q is invalid: use letters, digits, '_', '.', '-' (max 128 chars)", name)
	}
	return name, nil
}

// ComposeNetworks lists the names of the networks Docker Compose created for
// a project, found by the com.docker.compose.project label through
// DockerRunner. A project without networks is an ErrInvalidNetwork error.
// Lines that are not attachable network names are dropped.
func ComposeNetworks(ctx context.Context, project string) ([]string, error) {
	if project == "" {
		return nil, invalidNetwork("sandbox compose project is empty")
	}
	if _, err := ValidateComposeProject(project); err != nil {
		return nil, err
	}
	out, err := DockerRunner(ctx, "network", "ls",
		"--filter", "label="+composeProjectLabel+"="+project,
		"--format", "{{.Name}}")
	if err != nil {
		return nil, invalidNetwork("cannot list networks of compose project %q: %v (%s)", project, err, strings.TrimSpace(string(out)))
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, nerr := NormalizeNetworks([]string{line}); nerr != nil {
			continue
		}
		names = append(names, line)
	}
	names = MergeNetworkNames(nil, names)
	if len(names) == 0 {
		return nil, invalidNetwork("no networks found for compose project %q (is it running? docker compose -p %s up -d)", project, project)
	}
	return names, nil
}

// MergeNetworkNames returns names followed by extra, with duplicates removed
// and order kept.
func MergeNetworkNames(names, extra []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, list := range [][]string{names, extra} {
		for _, n := range list {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}
