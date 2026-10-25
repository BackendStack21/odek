package sandbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func id(c byte) string { return strings.Repeat(string(c), 64) }

// netFakeDocker records every docker invocation and answers from handler.
type netFakeDocker struct {
	log     []string
	handler func(args []string) ([]byte, error)
}

func (f *netFakeDocker) install(t *testing.T) {
	t.Helper()
	old := DockerRunner
	DockerRunner = func(ctx context.Context, args ...string) ([]byte, error) {
		f.log = append(f.log, strings.Join(args, " "))
		return f.handler(args)
	}
	t.Cleanup(func() { DockerRunner = old })
}

func (f *netFakeDocker) ran(sub string) bool {
	for _, l := range f.log {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func netJSON(name, nid, driver, scope string, internal bool, project string) string {
	labels := "{}"
	if project != "" {
		labels = fmt.Sprintf(`{"com.docker.compose.project":%q}`, project)
	}
	return fmt.Sprintf(`[{"Name":%q,"Id":%q,"Driver":%q,"Scope":%q,"Internal":%t,"Labels":%s}]`, name, nid, driver, scope, internal, labels)
}

func isNetInspect(args []string) bool {
	return len(args) >= 3 && args[0] == "network" && args[1] == "inspect"
}

// ── AC-15 name validation ───────────────────────────────────────────────

func TestNormalizeNetworks_Valid(t *testing.T) {
	got, err := NormalizeNetworks([]string{"app_default", "db-net", "a.b", "Z9", "app_default"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"app_default", "db-net", "a.b", "Z9"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v (order kept, duplicates removed)", got, want)
	}
	if got, err := NormalizeNetworks(nil); err != nil || len(got) != 0 {
		t.Errorf("nil input = %v, %v; want empty, nil", got, err)
	}
}

func TestNormalizeNetworks_Rejected(t *testing.T) {
	bad := []string{
		"host", "none", "bridge", "default", "container:abc", "ns:/proc/1/ns/net",
		"", " ", "-lead", "_lead", ".lead", "has space", "semi;colon", "new\nline",
		"a/b", "a:b", "ünï", strings.Repeat("a", 129),
	}
	for _, name := range bad {
		got, err := NormalizeNetworks([]string{"ok", name})
		if err == nil {
			t.Errorf("NormalizeNetworks(%q) = %v, nil; want error", name, got)
			continue
		}
		if !errors.Is(err, ErrInvalidNetwork) {
			t.Errorf("NormalizeNetworks(%q) error must wrap ErrInvalidNetwork: %v", name, err)
		}
	}
	if _, err := NormalizeNetworks([]string{strings.Repeat("a", 128)}); err != nil {
		t.Errorf("128-char name must be accepted: %v", err)
	}
}

func TestNormalizeNetworks_Cap(t *testing.T) {
	var names []string
	for i := 0; i < MaxNetworks; i++ {
		names = append(names, fmt.Sprintf("net%d", i))
	}
	if got, err := NormalizeNetworks(names); err != nil || len(got) != MaxNetworks {
		t.Fatalf("%d networks: %v, %v", MaxNetworks, got, err)
	}
	names = append(names, "one-too-many")
	if _, err := NormalizeNetworks(names); err == nil || !errors.Is(err, ErrInvalidNetwork) {
		t.Errorf("%d networks must be refused with ErrInvalidNetwork, got %v", len(names), err)
	}
	// Duplicates do not count against the cap.
	dups := append([]string{}, names[:MaxNetworks]...)
	dups = append(dups, "net0", "net1")
	if got, err := NormalizeNetworks(dups); err != nil || len(got) != MaxNetworks {
		t.Errorf("duplicates must not count against the cap: %v, %v", got, err)
	}
}

// ── AC-16 inspect-based resolution ──────────────────────────────────────

func TestResolveNetworks_ReturnsFullIDs(t *testing.T) {
	f := &netFakeDocker{handler: func(args []string) ([]byte, error) {
		if !isNetInspect(args) {
			return nil, fmt.Errorf("unexpected docker call %v", args)
		}
		switch args[len(args)-1] {
		case "a":
			return []byte(netJSON("a", id('a'), "bridge", "local", true, "proj")), nil
		case "b":
			return []byte(netJSON("b", id('b'), "bridge", "local", false, "")), nil
		}
		return nil, errors.New("Error: No such network")
	}}
	f.install(t)
	nets, err := ResolveNetworks(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 2 {
		t.Fatalf("got %d networks: %+v", len(nets), nets)
	}
	if nets[0].Name != "a" || nets[0].ID != id('a') || !nets[0].Internal || nets[0].ComposeProject != "proj" {
		t.Errorf("first network = %+v", nets[0])
	}
	if nets[1].Name != "b" || nets[1].ID != id('b') || nets[1].Internal || nets[1].ComposeProject != "" {
		t.Errorf("second network = %+v", nets[1])
	}
}

func TestResolveNetworks_Refusals(t *testing.T) {
	cases := []struct {
		name   string
		json   string
		err    error
		expect string
	}{
		{"overlay driver", netJSON("n", id('c'), "overlay", "swarm", false, ""), nil, "driver"},
		{"macvlan driver", netJSON("n", id('c'), "macvlan", "local", false, ""), nil, "driver"},
		{"host driver", netJSON("n", id('c'), "host", "local", false, ""), nil, "driver"},
		{"swarm scope", netJSON("n", id('c'), "bridge", "swarm", false, ""), nil, "scope"},
		{"missing network", "", errors.New("Error: No such network: n"), "n"},
		{"not json", "garbage", nil, "n"},
		{"empty list", "[]", nil, "n"},
		{"short id", netJSON("n", "abc", "bridge", "local", false, ""), nil, "n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &netFakeDocker{handler: func(args []string) ([]byte, error) { return []byte(tc.json), tc.err }}
			f.install(t)
			nets, err := ResolveNetworks(context.Background(), []string{"n"})
			if err == nil {
				t.Fatalf("want error, got %+v", nets)
			}
			if !errors.Is(err, ErrInvalidNetwork) {
				t.Errorf("error must wrap ErrInvalidNetwork: %v", err)
			}
			if !strings.Contains(err.Error(), tc.expect) {
				t.Errorf("message %q must mention %q", err.Error(), tc.expect)
			}
			if nets != nil {
				t.Errorf("no networks may be returned on error, got %+v", nets)
			}
		})
	}
}

func TestResolveNetworks_InvalidNameNeverReachesDocker(t *testing.T) {
	f := &netFakeDocker{handler: func(args []string) ([]byte, error) { return nil, errors.New("must not be called") }}
	f.install(t)
	if _, err := ResolveNetworks(context.Background(), []string{"host"}); err == nil || !errors.Is(err, ErrInvalidNetwork) {
		t.Errorf("want ErrInvalidNetwork, got %v", err)
	}
	if len(f.log) != 0 {
		t.Errorf("docker must not be called for an invalid name: %v", f.log)
	}
}

// ── AC-17 argument construction ─────────────────────────────────────────

func twoNets() []NetworkInfo {
	return []NetworkInfo{
		{Name: "a", ID: id('a'), Driver: "bridge", Scope: "local", Internal: true},
		{Name: "b", ID: id('b'), Driver: "bridge", Scope: "local", Internal: true},
	}
}

func TestBuildRunArgsChecked_NetworksReplaceNone(t *testing.T) {
	t.Setenv(LenientNetworkEnv, "")
	for _, mode := range []string{"", "none"} {
		cfg := Config{Network: mode, Networks: []string{"a", "b"}, ResolvedNetworks: twoNets()}
		args, err := BuildRunArgsChecked(cfg, "odek-test", "/workspace", "alpine:3")
		if err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		if v, ok := argValue(args, "--network"); !ok || v != id('a') {
			t.Errorf("mode %q: --network = %q (present=%v), want the full ID of the first network\nargs: %v", mode, v, ok, args)
		}
		if contains(args, "none") {
			t.Errorf("mode %q: attached networks must replace --network none: %v", mode, args)
		}
		n := 0
		for _, a := range args {
			if a == "--network" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("mode %q: --network appears %d times, want 1: %v", mode, n, args)
		}
		for _, a := range args {
			if a == "--network-alias" || strings.HasPrefix(a, "--network-alias=") || a == "--link" || a == "--add-host" {
				t.Errorf("mode %q: no alias, link or host entry may be added: %v", mode, args)
			}
		}
		if contains(args, id('b')) {
			t.Errorf("mode %q: the second network is attached by connect, not run: %v", mode, args)
		}
	}
}

func TestBuildRunArgsChecked_BridgePlusNetworks(t *testing.T) {
	t.Setenv(LenientNetworkEnv, "")
	cfg := Config{Network: "bridge", Networks: []string{"a"}, ResolvedNetworks: twoNets()[:1]}
	args, err := BuildRunArgsChecked(cfg, "odek-test", "/workspace", "alpine:3")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := argValue(args, "--network"); v != "bridge" {
		t.Errorf("--network = %q, want bridge when bridge is requested", v)
	}
	if extra := ExtraNetworkIDs("bridge", twoNets()[:1]); len(extra) != 1 || extra[0] != id('a') {
		t.Errorf("bridge mode must connect every attached network, got %v", extra)
	}
}

func TestBuildRunArgsChecked_UnresolvedNetworksRefused(t *testing.T) {
	t.Setenv(LenientNetworkEnv, "1") // leniency covers the mode only, never attached networks
	args, err := BuildRunArgsChecked(Config{Networks: []string{"a"}}, "odek-test", "/workspace", "alpine:3")
	if err == nil {
		t.Fatalf("networks without inspect results must be refused, got %v", args)
	}
	if !errors.Is(err, ErrInvalidNetwork) {
		t.Errorf("error must wrap ErrInvalidNetwork: %v", err)
	}
}

func TestExtraNetworkIDs(t *testing.T) {
	got := ExtraNetworkIDs("none", twoNets())
	if len(got) != 1 || got[0] != id('b') {
		t.Errorf("none mode: extra = %v, want [id-b]", got)
	}
	if got := ExtraNetworkIDs("none", twoNets()[:1]); len(got) != 0 {
		t.Errorf("single network: extra = %v, want none", got)
	}
	if got := ExtraNetworkIDs("bridge", twoNets()); len(got) != 2 || got[0] != id('a') || got[1] != id('b') {
		t.Errorf("bridge mode: extra = %v, want both ids in order", got)
	}
	if got := ExtraNetworkIDs("none", nil); len(got) != 0 {
		t.Errorf("no networks: extra = %v", got)
	}
}

// ── AC-18 attach mechanics ──────────────────────────────────────────────

func containerInspect(nets ...NetworkInfo) string {
	var parts []string
	for _, n := range nets {
		parts = append(parts, fmt.Sprintf(`%q:{"NetworkID":%q,"Aliases":["svc"]}`, n.Name, n.ID))
	}
	return `[{"NetworkSettings":{"Networks":{` + strings.Join(parts, ",") + `}}}]`
}

func TestAttachNetworks_ConnectsExtrasThenVerifies(t *testing.T) {
	nets := append(twoNets(), NetworkInfo{Name: "c", ID: id('c'), Driver: "bridge", Scope: "local"})
	f := &netFakeDocker{handler: func(args []string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "network" && args[1] == "connect" {
			return nil, nil
		}
		return []byte(containerInspect(nets...)), nil
	}}
	f.install(t)
	if err := AttachNetworks(context.Background(), "odek-1", "none", nets); err != nil {
		t.Fatal(err)
	}
	if !f.ran("network connect "+id('b')+" odek-1") || !f.ran("network connect "+id('c')+" odek-1") {
		t.Errorf("extra networks must be connected by full ID: %v", f.log)
	}
	if f.ran("network connect " + id('a')) {
		t.Errorf("the run-time network must not be connected again: %v", f.log)
	}
	if len(f.log) == 0 {
		t.Fatal("no docker calls were made")
	}
	last := f.log[len(f.log)-1]
	if !strings.Contains(last, "inspect") || !strings.Contains(last, "odek-1") || strings.Contains(last, "network") {
		t.Errorf("the final call must re-inspect the container, got %q (log %v)", last, f.log)
	}
	if f.ran("rm ") {
		t.Errorf("a successful attach must not remove the container: %v", f.log)
	}
}

func TestAttachNetworks_ConnectFailureRemovesContainer(t *testing.T) {
	nets := twoNets()
	f := &netFakeDocker{handler: func(args []string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "network" && args[1] == "connect" {
			return []byte("network not found"), errors.New("exit status 1")
		}
		return nil, nil
	}}
	f.install(t)
	err := AttachNetworks(context.Background(), "odek-1", "none", nets)
	if err == nil || !errors.Is(err, ErrInvalidNetwork) {
		t.Fatalf("want error wrapping ErrInvalidNetwork, got %v", err)
	}
	if !f.ran("rm -f odek-1") {
		t.Errorf("a connect failure must run docker rm -f on the container: %v", f.log)
	}
	if f.ran("network rm") {
		t.Errorf("networks are never removed: %v", f.log)
	}
}

func TestAttachNetworks_MissingAfterConnectIsFatal(t *testing.T) {
	nets := twoNets()
	f := &netFakeDocker{handler: func(args []string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "network" && args[1] == "connect" {
			return nil, nil
		}
		return []byte(containerInspect(nets[0])), nil // b never shows up
	}}
	f.install(t)
	err := AttachNetworks(context.Background(), "odek-1", "none", nets)
	if err == nil || !errors.Is(err, ErrInvalidNetwork) {
		t.Fatalf("want error wrapping ErrInvalidNetwork, got %v", err)
	}
	if !strings.Contains(err.Error(), "b") {
		t.Errorf("message must name the missing network: %v", err)
	}
	if !f.ran("rm -f odek-1") {
		t.Errorf("an unverified attach must remove the container: %v", f.log)
	}
	if f.ran("network rm") {
		t.Errorf("networks are never removed: %v", f.log)
	}
}

func TestAttachNetworks_InspectFailureIsFatal(t *testing.T) {
	nets := twoNets()
	f := &netFakeDocker{handler: func(args []string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "network" && args[1] == "connect" {
			return nil, nil
		}
		return []byte("boom"), errors.New("exit status 1")
	}}
	f.install(t)
	if err := AttachNetworks(context.Background(), "odek-1", "none", nets); err == nil || !errors.Is(err, ErrInvalidNetwork) {
		t.Fatalf("want error wrapping ErrInvalidNetwork, got %v", err)
	}
	if !f.ran("rm -f odek-1") {
		t.Errorf("container must be removed: %v", f.log)
	}
}

func TestAttachNetworks_NoNetworksIsNoop(t *testing.T) {
	f := &netFakeDocker{handler: func(args []string) ([]byte, error) { return nil, errors.New("must not be called") }}
	f.install(t)
	if err := AttachNetworks(context.Background(), "odek-1", "none", nil); err != nil {
		t.Errorf("no networks: %v", err)
	}
	if len(f.log) != 0 {
		t.Errorf("no docker calls expected: %v", f.log)
	}
}

// ── AC-19 warnings ──────────────────────────────────────────────────────

func TestNetworkWarnings(t *testing.T) {
	internal := twoNets()
	if w := NetworkWarnings("none", internal); len(w) != 0 {
		t.Errorf("internal networks without bridge need no warning, got %v", w)
	}
	if w := NetworkWarnings("none", nil); len(w) != 0 {
		t.Errorf("no networks, no warnings, got %v", w)
	}
	w := NetworkWarnings("bridge", internal)
	if len(w) == 0 || !strings.Contains(strings.Join(w, "\n"), "internet") {
		t.Errorf("bridge plus networks must warn that the internet is reachable, got %v", w)
	}
	open := []NetworkInfo{{Name: "open-net", ID: id('d'), Driver: "bridge", Scope: "local", Internal: false}}
	w = NetworkWarnings("none", open)
	joined := strings.Join(w, "\n")
	if len(w) == 0 || !strings.Contains(joined, "open-net") || !strings.Contains(joined, "internet") {
		t.Errorf("a non-internal network must be named in an internet warning, got %v", w)
	}
}
