package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// MaxNetworks is the most networks one sandbox may attach.
const MaxNetworks = 8

// NetworkInfo is the validated, inspected form of one Docker network.
type NetworkInfo struct {
	Name           string
	ID             string
	Driver         string
	Scope          string
	Internal       bool
	ComposeProject string
	// ServiceNames are the names of the containers already attached to the
	// network when it was inspected (their DNS names). Raw inspect data: callers
	// must validate before showing it to the model.
	ServiceNames []string
}

// DockerRunner runs one docker command and returns its combined output. It is
// a package variable so tests can inject results without a Docker daemon.
var DockerRunner = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.Bytes(), err
}

var networkNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var networkIDRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// composeProjectLabel is the label Docker Compose sets on project networks.
const composeProjectLabel = "com.docker.compose.project"

func invalidNetwork(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidNetwork, fmt.Sprintf(format, a...))
}

// NormalizeNetworks validates, de-duplicates and caps a list of network names.
// Reserved modes (host, none, bridge, default) and container:/ns: forms are
// refused; order is kept.
func NormalizeNetworks(names []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		switch n {
		case "host", "none", "bridge", "default":
			return nil, invalidNetwork("sandbox network %q is a reserved mode, not an attachable network", n)
		}
		if !networkNameRE.MatchString(n) {
			return nil, invalidNetwork("sandbox network name %q is invalid: use letters, digits, '_', '.', '-' (max 128 chars)", n)
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) > MaxNetworks {
		return nil, invalidNetwork("at most %d sandbox networks may be attached, got %d", MaxNetworks, len(out))
	}
	return out, nil
}

// ResolveNetworks inspects each named network through DockerRunner. Only
// local bridge networks are accepted; the full network ID is returned.
func ResolveNetworks(ctx context.Context, names []string) ([]NetworkInfo, error) {
	names, err := NormalizeNetworks(names)
	if err != nil {
		return nil, err
	}
	var nets []NetworkInfo
	for _, name := range names {
		out, err := DockerRunner(ctx, "network", "inspect", name)
		if err != nil {
			return nil, invalidNetwork("cannot inspect sandbox network %q: %v (%s)", name, err, strings.TrimSpace(string(out)))
		}
		var res []struct {
			Name       string
			ID         string `json:"Id"`
			Driver     string
			Scope      string
			Internal   bool
			Labels     map[string]string
			Containers map[string]struct{ Name string }
		}
		if err := json.Unmarshal(out, &res); err != nil || len(res) != 1 {
			return nil, invalidNetwork("unexpected docker inspect output for sandbox network %q", name)
		}
		r := res[0]
		if r.Driver != "bridge" {
			return nil, invalidNetwork("sandbox network %q has driver %q: only bridge networks are allowed", name, r.Driver)
		}
		if r.Scope != "local" {
			return nil, invalidNetwork("sandbox network %q has scope %q: only local networks are allowed", name, r.Scope)
		}
		if !networkIDRE.MatchString(r.ID) {
			return nil, invalidNetwork("sandbox network %q has an invalid ID", name)
		}
		nets = append(nets, NetworkInfo{Name: name, ServiceNames: serviceNames(ctx, name, r.Containers), ID: r.ID,
			Driver: r.Driver, Scope: r.Scope, Internal: r.Internal, ComposeProject: r.Labels[composeProjectLabel]})
	}
	return nets, nil
}

// maxServiceInspect bounds how many containers one network's service-name
// lookup inspects.
const maxServiceInspect = 64

var shortContainerIDRE = regexp.MustCompile(`^[0-9a-f]{12}$`)

// serviceNames returns the DNS names other containers answer to on network:
// each container's aliases there (Compose registers the service name as an
// alias) minus its own name and short ID, or the container name when it has
// no other alias. When the containers cannot be inspected the container
// names are used. The result is sorted and de-duplicated.
func serviceNames(ctx context.Context, network string, containers map[string]struct{ Name string }) []string {
	ids := make([]string, 0, len(containers))
	for cid := range containers {
		ids = append(ids, cid)
	}
	sort.Strings(ids)
	if len(ids) > maxServiceInspect {
		ids = ids[:maxServiceInspect]
	}
	aliases := map[string][]string{}
	if len(ids) > 0 {
		if out, err := DockerRunner(ctx, append([]string{"inspect"}, ids...)...); err == nil {
			var res []struct {
				ID              string `json:"Id"`
				NetworkSettings struct {
					Networks map[string]struct{ Aliases []string }
				}
			}
			if json.Unmarshal(out, &res) == nil {
				for _, c := range res {
					aliases[c.ID] = c.NetworkSettings.Networks[network].Aliases
				}
			}
		}
	}
	seen := map[string]bool{}
	var names []string
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, cid := range ids {
		own := containers[cid].Name
		found := false
		for _, a := range aliases[cid] {
			if a == own || shortContainerIDRE.MatchString(a) || strings.HasPrefix(cid, a) {
				continue
			}
			add(a)
			found = true
		}
		if !found {
			add(own)
		}
	}
	sort.Strings(names)
	return names
}

// ExtraNetworkIDs returns the network IDs that must be attached with
// "docker network connect" after the container starts. In none mode the
// first network is attached by "docker run" itself.
func ExtraNetworkIDs(mode string, nets []NetworkInfo) []string {
	var ids []string
	for i, n := range nets {
		if i == 0 && mode != "bridge" {
			continue
		}
		ids = append(ids, n.ID)
	}
	return ids
}

// AttachNetworks connects the container to its extra networks and verifies
// every requested network is present afterwards. Any failure removes the
// container; networks are never removed.
func AttachNetworks(ctx context.Context, container, mode string, nets []NetworkInfo) error {
	if len(nets) == 0 {
		return nil
	}
	fail := func(err error) error {
		_, _ = DockerRunner(context.Background(), "rm", "-f", container)
		return err
	}
	for _, id := range ExtraNetworkIDs(mode, nets) {
		if out, err := DockerRunner(ctx, "network", "connect", id, container); err != nil {
			return fail(invalidNetwork("cannot connect sandbox to network %s: %v (%s)", id, err, strings.TrimSpace(string(out))))
		}
	}
	out, err := DockerRunner(ctx, "inspect", container)
	if err != nil {
		return fail(invalidNetwork("cannot verify sandbox networks: %v", err))
	}
	var res []struct {
		NetworkSettings struct {
			Networks map[string]struct{ NetworkID string }
		}
	}
	if err := json.Unmarshal(out, &res); err != nil || len(res) != 1 {
		return fail(invalidNetwork("unexpected docker inspect output for container %s", container))
	}
	have := map[string]bool{}
	for _, n := range res[0].NetworkSettings.Networks {
		have[n.NetworkID] = true
	}
	for _, n := range nets {
		if !have[n.ID] {
			return fail(invalidNetwork("sandbox is not attached to requested network %q", n.Name))
		}
	}
	return nil
}

// NetworkWarnings lists the warnings to show for the attached networks.
func NetworkWarnings(mode string, nets []NetworkInfo) []string {
	var w []string
	if mode == "bridge" && len(nets) > 0 {
		w = append(w, "sandbox network bridge combined with attached networks: the internet is reachable")
	}
	for _, n := range nets {
		if !n.Internal {
			w = append(w, fmt.Sprintf("sandbox network %q is not internal: the internet is reachable through it", n.Name))
		}
	}
	return w
}
