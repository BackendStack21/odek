package sandbox

import (
	"errors"
	"strings"
	"testing"
)

func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func TestBuildRunArgs_HardeningDefaults(t *testing.T) {
	args := BuildRunArgs(Config{}, "odek-test", "/workspace", "alpine:3")
	if v, ok := argValue(args, "--pids-limit"); !ok || v != "4096" {
		t.Errorf("--pids-limit = %q (present=%v), want 4096\nargs: %v", v, ok, args)
	}
	if !contains(args, "--init") {
		t.Errorf("args missing --init: %v", args)
	}
	if v, ok := argValue(args, "--tmpfs"); !ok || v != "/tmp:noexec,nosuid" {
		t.Errorf("--tmpfs = %q (present=%v), want /tmp:noexec,nosuid\nargs: %v", v, ok, args)
	}
	if DefaultPidsLimit != 4096 {
		t.Errorf("DefaultPidsLimit = %d, want 4096", DefaultPidsLimit)
	}
}

func TestBuildRunArgs_ExplicitPidsLimit(t *testing.T) {
	args := BuildRunArgs(Config{PidsLimit: "512"}, "odek-test", "/workspace", "alpine:3")
	if v, ok := argValue(args, "--pids-limit"); !ok || v != "512" {
		t.Errorf("--pids-limit = %q (present=%v), want 512\nargs: %v", v, ok, args)
	}
	n := 0
	for _, a := range args {
		if a == "--pids-limit" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("--pids-limit appears %d times, want 1", n)
	}
}

func TestParsePidsLimit_Table(t *testing.T) {
	good := map[string]int{"": DefaultPidsLimit, "1": 1, "512": 512, "4096": 4096, "100000": 100000}
	for in, want := range good {
		got, err := ParsePidsLimit(in)
		if err != nil || got != want {
			t.Errorf("ParsePidsLimit(%q) = %d, %v; want %d, nil", in, got, err, want)
		}
	}
	for _, bad := range []string{"0", "-1", "-512", "abc", "1.5", "12abc", " 5", "5 ", "0x10", "+", "1e3"} {
		got, err := ParsePidsLimit(bad)
		if err == nil {
			t.Errorf("ParsePidsLimit(%q) = %d, nil; want error", bad, got)
			continue
		}
		if !errors.Is(err, ErrInvalidPidsLimit) {
			t.Errorf("ParsePidsLimit(%q) error must wrap ErrInvalidPidsLimit: %v", bad, err)
		}
		if !strings.Contains(err.Error(), strings.TrimSpace(bad)) {
			t.Errorf("ParsePidsLimit(%q) message must name the value: %v", bad, err)
		}
	}
}

func TestBuildRunArgsChecked_InvalidPidsLimitRejected(t *testing.T) {
	t.Setenv(LenientNetworkEnv, "1") // leniency covers networks only, never pids
	for _, bad := range []string{"abc", "0", "-3"} {
		args, err := BuildRunArgsChecked(Config{PidsLimit: bad}, "odek-test", "/workspace", "alpine:3")
		if err == nil {
			t.Errorf("pids %q: want error, got args %v", bad, args)
			continue
		}
		if !errors.Is(err, ErrInvalidPidsLimit) {
			t.Errorf("pids %q: error must wrap ErrInvalidPidsLimit: %v", bad, err)
		}
		if args != nil {
			t.Errorf("pids %q: args must be nil on error, got %v", bad, args)
		}
	}
}

func TestBuildRunArgsChecked_ValidPidsLimit(t *testing.T) {
	t.Setenv(LenientNetworkEnv, "")
	args, err := BuildRunArgsChecked(Config{PidsLimit: "256"}, "odek-test", "/workspace", "alpine:3")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := argValue(args, "--pids-limit"); v != "256" {
		t.Errorf("--pids-limit = %q, want 256", v)
	}
}
