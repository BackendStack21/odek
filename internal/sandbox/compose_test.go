package sandbox

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func isNetLs(args []string) bool {
	return len(args) >= 2 && args[0] == "network" && args[1] == "ls"
}

// ── project name validation ─────────────────────────────────────────────

func TestValidateComposeProject_Valid(t *testing.T) {
	for _, name := range []string{"myapp", "my-app", "my_app", "app.v2", "A1", strings.Repeat("a", 128)} {
		got, err := ValidateComposeProject(name)
		if err != nil || got != name {
			t.Errorf("ValidateComposeProject(%q) = %q, %v; want the name, nil", name, got, err)
		}
	}
	if got, err := ValidateComposeProject(""); err != nil || got != "" {
		t.Errorf("empty project = %q, %v; want empty, nil", got, err)
	}
}

func TestValidateComposeProject_Rejected(t *testing.T) {
	for _, name := range []string{
		" ", "-lead", "_lead", ".lead", "has space", "semi;colon", "new\nline",
		"a/b", "a:b", "a=b", "x,y", "ünï", "esc\x1b[0m", strings.Repeat("a", 129),
	} {
		_, err := ValidateComposeProject(name)
		if err == nil {
			t.Errorf("ValidateComposeProject(%q) = nil error; want rejection", name)
			continue
		}
		if !errors.Is(err, ErrInvalidNetwork) {
			t.Errorf("ValidateComposeProject(%q) error must wrap ErrInvalidNetwork: %v", name, err)
		}
	}
}

// ── label based lookup ──────────────────────────────────────────────────

func TestComposeNetworks_UsesLabelFilter(t *testing.T) {
	f := &netFakeDocker{handler: func(args []string) ([]byte, error) {
		if isNetLs(args) {
			return []byte("myapp_default\nmyapp_db\n"), nil
		}
		return nil, errors.New("unexpected docker call")
	}}
	f.install(t)

	got, err := ComposeNetworks(context.Background(), "myapp")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"myapp_default", "myapp_db"}; !reflect.DeepEqual(got, want) {
		t.Errorf("networks = %v, want %v", got, want)
	}
	if len(f.log) != 1 {
		t.Fatalf("want exactly one docker call, got %v", f.log)
	}
	call := f.log[0]
	if !strings.HasPrefix(call, "network ls") {
		t.Errorf("must list networks: %q", call)
	}
	if !strings.Contains(call, "--filter label=com.docker.compose.project=myapp") {
		t.Errorf("must filter by the compose project label: %q", call)
	}
	if !strings.Contains(call, "--format") {
		t.Errorf("must request a stable --format: %q", call)
	}
}

func TestComposeNetworks_NeverGuessesDefaultName(t *testing.T) {
	f := &netFakeDocker{handler: func(args []string) ([]byte, error) {
		if isNetLs(args) {
			return []byte("custom_net_name\n"), nil
		}
		return nil, errors.New("unexpected docker call")
	}}
	f.install(t)

	got, err := ComposeNetworks(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"custom_net_name"}) {
		t.Errorf("networks = %v; want the labelled network, not p_default", got)
	}
	if f.ran("p_default") {
		t.Errorf("the name p_default must never be derived: %v", f.log)
	}
}

func TestComposeNetworks_NoneFoundHintsAtComposeUp(t *testing.T) {
	for _, out := range []string{"", "\n", "  \n\n"} {
		f := &netFakeDocker{handler: func(args []string) ([]byte, error) { return []byte(out), nil }}
		f.install(t)
		got, err := ComposeNetworks(context.Background(), "myapp")
		if err == nil {
			t.Fatalf("output %q: want error, got %v", out, got)
		}
		if !errors.Is(err, ErrInvalidNetwork) {
			t.Errorf("error must wrap ErrInvalidNetwork: %v", err)
		}
		for _, want := range []string{"myapp", "is it running?", "docker compose -p myapp up -d"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error must contain %q: %v", want, err)
			}
		}
	}
}

func TestComposeNetworks_DockerFailureIsAnError(t *testing.T) {
	f := &netFakeDocker{handler: func(args []string) ([]byte, error) {
		return []byte("Cannot connect to the Docker daemon"), errors.New("exit status 1")
	}}
	f.install(t)
	_, err := ComposeNetworks(context.Background(), "myapp")
	if err == nil || !errors.Is(err, ErrInvalidNetwork) {
		t.Fatalf("want ErrInvalidNetwork, got %v", err)
	}
}

func TestComposeNetworks_InvalidProjectNeverReachesDocker(t *testing.T) {
	f := &netFakeDocker{handler: func(args []string) ([]byte, error) { return nil, errors.New("must not run") }}
	f.install(t)
	for _, bad := range []string{"", "a b", "x;rm", "label=evil", "-n"} {
		if _, err := ComposeNetworks(context.Background(), bad); err == nil || !errors.Is(err, ErrInvalidNetwork) {
			t.Errorf("project %q: want ErrInvalidNetwork, got %v", bad, err)
		}
	}
	if len(f.log) != 0 {
		t.Errorf("docker must not be consulted for invalid projects: %v", f.log)
	}
}

func TestComposeNetworks_DropsHostileLines(t *testing.T) {
	f := &netFakeDocker{handler: func(args []string) ([]byte, error) {
		return []byte("good_net\nhost\nbridge\nnone\nbad name\nevil;x\nok2\r\n"), nil
	}}
	f.install(t)
	got, err := ComposeNetworks(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range got {
		if _, nerr := NormalizeNetworks([]string{n}); nerr != nil {
			t.Errorf("returned network %q does not pass NormalizeNetworks", n)
		}
	}
	if !reflect.DeepEqual(got, []string{"good_net", "ok2"}) {
		t.Errorf("networks = %v, want [good_net ok2]", got)
	}
}

// ── merge ───────────────────────────────────────────────────────────────

func TestMergeNetworkNames(t *testing.T) {
	cases := []struct {
		names, extra, want []string
	}{
		{[]string{"a", "b"}, []string{"c"}, []string{"a", "b", "c"}},
		{[]string{"a", "b"}, []string{"b", "a", "c"}, []string{"a", "b", "c"}},
		{nil, []string{"x", "x"}, []string{"x"}},
		{[]string{"a"}, nil, []string{"a"}},
	}
	for _, c := range cases {
		if got := MergeNetworkNames(c.names, c.extra); !reflect.DeepEqual(got, c.want) {
			t.Errorf("MergeNetworkNames(%v, %v) = %v, want %v", c.names, c.extra, got, c.want)
		}
	}
	if got := MergeNetworkNames(nil, nil); len(got) != 0 {
		t.Errorf("empty merge = %v, want empty", got)
	}
}
