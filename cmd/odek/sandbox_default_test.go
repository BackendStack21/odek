package main

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
)

// ── H-8: the sandbox defaults on — with a loud, explicit fallback ────────
//
// The warning was honest but the feature was opt-in: users had to discover
// the one control that actually contains the "ran attacker-controlled
// code" class. Default-on (explicit opt-out) flips that: isolation is what
// you get unless you deliberately give it up.

func TestSandboxIntent_DefaultOnWhenUnset(t *testing.T) {
	t.Setenv("ODEK_NO_SANDBOX", "")
	t.Setenv("ODEK_REQUIRE_SANDBOX", "")

	want, explicit := sandboxIntent(config.ResolvedConfig{})
	if !want || explicit {
		t.Errorf("unset sandbox: want=%v explicit=%v, want true/false (default on)", want, explicit)
	}
}

func TestSandboxIntent_ExplicitTrueAndFalse(t *testing.T) {
	t.Setenv("ODEK_NO_SANDBOX", "")
	want, explicit := sandboxIntent(config.ResolvedConfig{Sandbox: true, SandboxExplicit: true})
	if !want || !explicit {
		t.Errorf("explicit true: want=%v explicit=%v, want true/true", want, explicit)
	}
	want, explicit = sandboxIntent(config.ResolvedConfig{Sandbox: false, SandboxExplicit: true})
	if want || !explicit {
		t.Errorf("explicit false (--no-sandbox): want=%v explicit=%v, want false/true", want, explicit)
	}
}

func TestSandboxIntent_EnvOptOut(t *testing.T) {
	t.Setenv("ODEK_NO_SANDBOX", "1")
	want, explicit := sandboxIntent(config.ResolvedConfig{})
	if want || !explicit {
		t.Errorf("ODEK_NO_SANDBOX=1: want=%v explicit=%v, want false/true", want, explicit)
	}
}

func TestParseRunFlags_NoSandboxFlag(t *testing.T) {
	f, err := parseRunFlags([]string{"--no-sandbox", "do work"})
	if err != nil {
		t.Fatalf("parseRunFlags: %v", err)
	}
	if f.Sandbox == nil || *f.Sandbox {
		t.Error("--no-sandbox should explicitly disable the sandbox")
	}
	f, err = parseRunFlags([]string{"do work", "--no-sandbox"})
	if err != nil {
		t.Fatalf("parseRunFlags (trailing): %v", err)
	}
	if f.Sandbox == nil || *f.Sandbox {
		t.Error("trailing --no-sandbox should explicitly disable the sandbox")
	}
}

// An invalid image reference makes setupSandbox fail fast (malformed ref —
// no pull attempt), deterministically on machines with and without Docker.
const unbuildableImage = "!!!invalid-image-ref!!!"

func TestEnsureSandbox_ImplicitFailureDegradesLoudly(t *testing.T) {
	t.Setenv("ODEK_NO_SANDBOX", "")
	t.Setenv("ODEK_REQUIRE_SANDBOX", "")

	resolved := config.ResolvedConfig{} // implicit default-on
	flush := captureStderr(t)
	name, cleanup, sandboxed, err := ensureSandbox(resolved, nil, sandboxConfig{Image: unbuildableImage})
	stderr := flush()

	if err != nil {
		t.Fatalf("implicit default must degrade, not fail: %v", err)
	}
	if sandboxed || name != "" || cleanup != nil {
		t.Errorf("degraded run must not report a sandbox: %q %v", name, sandboxed)
	}
	if !strings.Contains(stderr, "WITHOUT sandbox") {
		t.Errorf("degradation must be loud, stderr:\n%s", stderr)
	}
}

func TestEnsureSandbox_ExplicitFailureIsFatal(t *testing.T) {
	t.Setenv("ODEK_NO_SANDBOX", "")
	t.Setenv("ODEK_REQUIRE_SANDBOX", "")

	resolved := config.ResolvedConfig{Sandbox: true, SandboxExplicit: true}
	_, _, _, err := ensureSandbox(resolved, nil, sandboxConfig{Image: unbuildableImage})
	if err == nil {
		t.Fatal("explicit --sandbox + failure must be fatal (pre-existing behavior)")
	}
}

func TestEnsureSandbox_RequireSandboxEnvMakesImplicitFatal(t *testing.T) {
	t.Setenv("ODEK_NO_SANDBOX", "")
	t.Setenv("ODEK_REQUIRE_SANDBOX", "1")

	resolved := config.ResolvedConfig{} // implicit default-on
	_, _, _, err := ensureSandbox(resolved, nil, sandboxConfig{Image: unbuildableImage})
	if err == nil {
		t.Fatal("ODEK_REQUIRE_SANDBOX=1 must make implicit fallback fatal")
	}
}

func TestEnsureSandbox_OptOutWarnsOnce(t *testing.T) {
	t.Setenv("ODEK_NO_SANDBOX", "1")
	name, cleanup, sandboxed, err := ensureSandbox(config.ResolvedConfig{}, nil, sandboxConfig{})
	if err != nil || sandboxed || name != "" || cleanup != nil {
		t.Fatalf("opt-out must stay unsandboxed: err=%v sandboxed=%v", err, sandboxed)
	}
}

func TestEnsureSandbox_RequireOutranksOptOut(t *testing.T) {
	// Review MED-003: the operator's hard constraint beats every opt-out,
	// including an explicit one — contradictory instructions fail loudly.
	t.Setenv("ODEK_NO_SANDBOX", "1")
	t.Setenv("ODEK_REQUIRE_SANDBOX", "1")
	if _, _, _, err := ensureSandbox(config.ResolvedConfig{}, nil, sandboxConfig{}); err == nil {
		t.Fatal("ODEK_REQUIRE_SANDBOX=1 + opt-out must be fatal")
	}

	// Same for an explicit config-level false.
	t.Setenv("ODEK_NO_SANDBOX", "")
	if _, _, _, err := ensureSandbox(config.ResolvedConfig{Sandbox: false, SandboxExplicit: true}, nil, sandboxConfig{}); err == nil {
		t.Fatal("ODEK_REQUIRE_SANDBOX=1 + explicit false must be fatal")
	}
}

func TestSandboxExplicit_TracksConfigLayer(t *testing.T) {
	t.Setenv("ODEK_NO_SANDBOX", "")

	// Unset everywhere → not explicit.
	cfg := config.LoadConfig(config.CLIFlags{})
	if cfg.SandboxExplicit {
		t.Error("nothing set → SandboxExplicit must be false")
	}

	// CLI layer sets it → explicit.
	on := true
	cfg = config.LoadConfig(config.CLIFlags{Sandbox: &on})
	if !cfg.SandboxExplicit || !cfg.Sandbox {
		t.Error("CLI --sandbox → explicit true")
	}
	off := false
	cfg = config.LoadConfig(config.CLIFlags{Sandbox: &off})
	if !cfg.SandboxExplicit || cfg.Sandbox {
		t.Error("CLI --no-sandbox → explicit false")
	}
}
