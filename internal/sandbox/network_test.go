package sandbox

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestValidateNetwork_Table(t *testing.T) {
	t.Setenv(LenientNetworkEnv, "")
	good := map[string]string{"": "none", "none": "none", "bridge": "bridge"}
	for in, want := range good {
		got, err := ValidateNetwork(in)
		if err != nil || got != want {
			t.Errorf("ValidateNetwork(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	for _, bad := range []string{"host", "container:x", "myapp_default", "HOST", "none ", "ns:/proc/1/ns/net"} {
		got, err := ValidateNetwork(bad)
		if err == nil {
			t.Errorf("ValidateNetwork(%q) = %q, nil; want error", bad, got)
			continue
		}
		if !errors.Is(err, ErrInvalidNetwork) {
			t.Errorf("ValidateNetwork(%q) error must wrap ErrInvalidNetwork: %v", bad, err)
		}
		msg := err.Error()
		for _, frag := range []string{strings.TrimSpace(bad), "none", "bridge"} {
			if !strings.Contains(msg, frag) {
				t.Errorf("ValidateNetwork(%q) message %q must mention %q", bad, msg, frag)
			}
		}
	}
}

func TestBuildRunArgsChecked_InvalidNetworkNeverSilentNone(t *testing.T) {
	t.Setenv(LenientNetworkEnv, "")
	for _, bad := range []string{"host", "container:x", "myapp_default"} {
		args, err := BuildRunArgsChecked(Config{Network: bad}, "odek-test", "/workspace", "alpine:3")
		if err == nil {
			t.Errorf("network %q: want error, got args %v", bad, args)
			continue
		}
		if !errors.Is(err, ErrInvalidNetwork) {
			t.Errorf("network %q: error must wrap ErrInvalidNetwork: %v", bad, err)
		}
		if args != nil {
			t.Errorf("network %q: args must be nil on error, got %v", bad, args)
		}
	}
}

func TestBuildRunArgsChecked_ValidNetworks(t *testing.T) {
	t.Setenv(LenientNetworkEnv, "")
	for in, want := range map[string]string{"": "none", "none": "none", "bridge": "bridge"} {
		args, err := BuildRunArgsChecked(Config{Network: in}, "odek-test", "/workspace", "alpine:3")
		if err != nil {
			t.Fatalf("network %q: %v", in, err)
		}
		found := false
		for i, a := range args {
			if a == "--network" && i+1 < len(args) {
				found = true
				if args[i+1] != want {
					t.Errorf("network %q: --network %q, want %q", in, args[i+1], want)
				}
			}
		}
		if !found {
			t.Errorf("network %q: --network missing", in)
		}
	}
}

func captureStderrFn(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	fn()
	w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

func TestBuildRunArgsChecked_LenientDowngradesToNone(t *testing.T) {
	t.Setenv(LenientNetworkEnv, "1")
	var args []string
	var err error
	stderr := captureStderrFn(t, func() {
		args, err = BuildRunArgsChecked(Config{Network: "host"}, "odek-test", "/workspace", "alpine:3")
	})
	if err != nil {
		t.Fatalf("lenient mode must not error: %v", err)
	}
	ok := false
	for i, a := range args {
		if a == "--network" && i+1 < len(args) && args[i+1] == "none" {
			ok = true
		}
		if a == "host" {
			t.Errorf("host must never reach docker args: %v", args)
		}
	}
	if !ok {
		t.Errorf("want --network none, got %v", args)
	}
	if !strings.Contains(stderr, "WARNING") || !strings.Contains(stderr, "host") {
		t.Errorf("want stderr warning naming the value, got %q", stderr)
	}
}

func TestBuildRunArgsChecked_LenientOnlyExactOne(t *testing.T) {
	t.Setenv(LenientNetworkEnv, "true")
	if _, err := BuildRunArgsChecked(Config{Network: "host"}, "n", "/w", "alpine:3"); err == nil {
		t.Error("only ODEK_SANDBOX_NETWORK_LENIENT=1 enables the downgrade")
	}
}

func TestNetworkDocComment_DoesNotAdvertiseHost(t *testing.T) {
	src, err := os.ReadFile("sandbox.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(src), "\n") {
		if strings.Contains(line, "Network ") && strings.Contains(line, "string") && strings.Contains(line, `"host"`) {
			t.Errorf("Config.Network doc must not list host as a valid value: %s", line)
		}
	}
}
