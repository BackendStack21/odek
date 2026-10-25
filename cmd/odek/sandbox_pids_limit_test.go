package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
)

// ── setupSandbox rejects a bad pids limit as a configuration error ─────

func TestSetupSandbox_InvalidPidsLimitIsConfigError(t *testing.T) {
	t.Setenv("ODEK_SANDBOX_NETWORK_LENIENT", "")
	for _, bad := range []string{"abc", "0", "-5", "1.5"} {
		_, cleanup, err := setupSandbox(nil, sandboxConfig{Image: "alpine:3", PidsLimit: bad})
		if cleanup != nil {
			_ = cleanup()
		}
		if err == nil || !errors.Is(err, errSandboxConfig) {
			t.Errorf("pids %q: want error wrapping errSandboxConfig, got %v", bad, err)
		}
		if err != nil && !strings.Contains(err.Error(), bad) {
			t.Errorf("pids %q: message must name the value: %v", bad, err)
		}
	}
}

// ── flag parsing: run, repl, continue ───────────────────────────────────

func TestParseRunFlags_SandboxPidsLimit(t *testing.T) {
	f, err := parseRunFlags([]string{"--sandbox-pids-limit", "512", "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if f.SandboxPidsLimit != "512" {
		t.Errorf("SandboxPidsLimit = %q, want 512", f.SandboxPidsLimit)
	}
	if _, err := parseRunFlags([]string{"--sandbox-pids-limit"}); err == nil || !strings.Contains(err.Error(), "--sandbox-pids-limit requires a value") {
		t.Errorf("missing value error = %v", err)
	}
}

func TestParseReplFlags_SandboxPidsLimit(t *testing.T) {
	f, err := parseReplFlags([]string{"--sandbox-pids-limit", "256"})
	if err != nil {
		t.Fatal(err)
	}
	if f.SandboxPidsLimit != "256" {
		t.Errorf("SandboxPidsLimit = %q, want 256", f.SandboxPidsLimit)
	}
}

func TestContinuePinnedFlag_SandboxPidsLimit(t *testing.T) {
	if got := continuePinnedFlag(runFlags{SandboxPidsLimit: "64"}); got != "--sandbox-pids-limit" {
		t.Errorf("continuePinnedFlag = %q, want --sandbox-pids-limit", got)
	}
	_, _, err := parseContinueArgs([]string{"--id", "s1", "--sandbox-pids-limit", "64", "go on"})
	if err == nil || !strings.Contains(err.Error(), "--sandbox-pids-limit") {
		t.Errorf("continue must refuse --sandbox-pids-limit by name, got %v", err)
	}
}

// ── help and init templates ─────────────────────────────────────────────

func TestHelp_DocumentsSandboxPidsLimit(t *testing.T) {
	usage := captureStdout(printUsage)
	for _, want := range []string{"--sandbox-pids-limit", "ODEK_SANDBOX_PIDS_LIMIT"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage missing %q", want)
		}
	}
	if serve := captureStdout(printServeHelp); !strings.Contains(serve, "--sandbox-pids-limit") {
		t.Error("serve help missing --sandbox-pids-limit")
	}
}

func TestInitTemplates_SandboxPidsLimitEmpty(t *testing.T) {
	for name, tpl := range map[string]string{"global": globalConfigTemplate, "local": localConfigTemplate} {
		var m map[string]any
		if err := json.Unmarshal([]byte(tpl), &m); err != nil {
			t.Fatalf("%s template invalid JSON: %v", name, err)
		}
		v, ok := m["sandbox_pids_limit"]
		if !ok {
			t.Errorf("%s template missing sandbox_pids_limit", name)
			continue
		}
		if s, _ := v.(string); s != "" {
			t.Errorf("%s template sandbox_pids_limit = %v, want empty", name, v)
		}
	}
}

// ── wiring: every sandboxConfig{} literal and flag parser carries the knob ─

func TestSandboxPidsLimit_AllSandboxConfigLiteralsCarryKnob(t *testing.T) {
	literal := regexp.MustCompile(`sandboxConfig\{`)
	knob := regexp.MustCompile(`\bPidsLimit:\s+resolved\.SandboxPidsLimit`)
	total := 0
	for _, file := range []string{"main.go", "repl.go", "serve.go", "subagent.go", "mcp.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		lits := len(literal.FindAll(src, -1))
		got := len(knob.FindAll(src, -1))
		total += lits
		if lits != got {
			t.Errorf("%s: %d sandboxConfig{} literals but %d carry PidsLimit: resolved.SandboxPidsLimit", file, lits, got)
		}
	}
	if total != 6 {
		t.Errorf("expected 6 sandboxConfig{} literals across the CLI, found %d", total)
	}
}

func TestSandboxPidsLimit_FlagWiringPresent(t *testing.T) {
	read := func(f string) string {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	serve := read("serve.go")
	if !strings.Contains(serve, `case "--sandbox-pids-limit"`) {
		t.Error("serve.go does not parse --sandbox-pids-limit")
	}
	if !regexp.MustCompile(`SandboxPidsLimit:\s+sandboxPidsLimit`).MatchString(serve) {
		t.Error("serve.go does not pass the flag into config.CLIFlags")
	}
	if !regexp.MustCompile(`SandboxPidsLimit:\s+f\.SandboxPidsLimit`).MatchString(read("repl.go")) {
		t.Error("repl.go does not pass the flag into config.CLIFlags")
	}
	main := read("main.go")
	if len(regexp.MustCompile(`SandboxPidsLimit:\s+f\.SandboxPidsLimit`).FindAllString(main, -1)) < 1 {
		t.Error("main.go does not pass the run flag into config.CLIFlags")
	}
	if !strings.Contains(main, `case "--sandbox-pids-limit"`) {
		t.Error("main.go does not parse --sandbox-pids-limit")
	}
	if !strings.Contains(read("external_ref.go"), `"--sandbox-pids-limit"`) {
		t.Error("continuePinnedFlag does not name --sandbox-pids-limit")
	}
}

func TestSandboxPidsLimit_StartupLineWired(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "sandboxPostureLine(") {
		t.Error("setupSandbox does not print the effective posture line")
	}
}

// ── approval key and gate ───────────────────────────────────────────────

func TestProjectSandboxApprovalKey_PidsLimitGolden(t *testing.T) {
	base := config.ProjectSandboxOverride{
		HasImage: true, Image: "alpine:3", HasNetwork: true, Network: "bridge",
		HasEnv: true, EnvKeys: []string{"A"}, Env: map[string]string{"A": "1"},
		HasVolumes: true, Volumes: []string{"./data:/data"},
		HasUser: true, User: "1000:1000", HasMemory: true, Memory: "512m", HasCPUs: true, CPUs: "2",
	}
	const golden = "d73c22ca03909f6d99e24ee57234da59b51889fa3ba4764723c8f3ab3813df88"
	if got := projectSandboxApprovalKey("/proj/golden", base); got != golden {
		t.Errorf("key for a config without pids limit changed: got %s want %s", got, golden)
	}
	const emptyGolden = "3518d911dc55f2f94d93c1bf3864021a9fd37b488c468c3f387b695b9cd24aab"
	if got := projectSandboxApprovalKey("/proj/golden", config.ProjectSandboxOverride{}); got != emptyGolden {
		t.Errorf("key for an empty override changed: got %s want %s", got, emptyGolden)
	}
	withPids := base
	withPids.HasPidsLimit = true
	withPids.PidsLimit = "256"
	if projectSandboxApprovalKey("/proj/golden", withPids) == golden {
		t.Error("a project pids limit must change the approval key")
	}
	other := withPids
	other.PidsLimit = "128"
	if projectSandboxApprovalKey("/proj/golden", other) == projectSandboxApprovalKey("/proj/golden", withPids) {
		t.Error("different pids limits must produce different approval keys")
	}
}

func TestApproveProjectSandbox_PidsLimitOverrideRequiresApproval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_APPROVE_PROJECT_SANDBOX", "")
	t.Chdir(t.TempDir())
	resolved := config.ResolvedConfig{ProjectSandboxOverride: config.ProjectSandboxOverride{HasPidsLimit: true, PidsLimit: "128"}}
	if err := approveProjectSandboxWithTTY(resolved, strings.NewReader(""), io.Discard, false); err == nil {
		t.Error("project sandbox_pids_limit must go through the same approval gate as memory/cpus")
	}
}

// ── sub-agent posture: child never exceeds the parent ───────────────────

func TestChildSandboxPosture_StampsPidsLimit(t *testing.T) {
	d := &delegateTasksTool{}
	recordChildSandbox([]odek.Tool{d}, sandboxConfig{Network: "none", PidsLimit: "256"})
	_, sp := d.childPosture()
	if sp == nil || sp.PidsLimit != "256" {
		t.Fatalf("posture = %+v, want PidsLimit 256", sp)
	}
}

func TestApplyTaskSandboxPosture_PidsLimitOnlyLowers(t *testing.T) {
	cases := []struct{ name, parent, child, want string }{
		{"child higher is lowered", "100", "200", "100"},
		{"child lower is kept", "100", "50", "50"},
		{"child empty inherits parent", "100", "", "100"},
		{"parent empty leaves child", "", "300", "300"},
		{"child invalid is replaced by parent", "100", "abc", "100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := config.ResolvedConfig{SandboxPidsLimit: tc.child}
			applyTaskSandboxPosture(&r, &taskSandboxPosture{PidsLimit: tc.parent})
			if r.SandboxPidsLimit != tc.want {
				t.Errorf("child pids = %q, want %q", r.SandboxPidsLimit, tc.want)
			}
		})
	}
}

// ── effective posture surfaces ──────────────────────────────────────────

func TestSandboxPostureLine(t *testing.T) {
	got := sandboxPostureLine(sandboxConfig{PidsLimit: "512", Network: "bridge"}, "alpine:3.20@sha256:abc")
	if want := "odek: sandbox network=bridge pids=512 image=alpine:3.20@sha256:abc"; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
	got = sandboxPostureLine(sandboxConfig{}, "alpine:3")
	if want := "odek: sandbox network=none pids=4096 image=alpine:3"; got != want {
		t.Errorf("defaults line = %q, want %q", got, want)
	}
}

func TestSandboxPostureLine_SanitizesImage(t *testing.T) {
	got := sandboxPostureLine(sandboxConfig{}, "evil\x1b[31m\nodek: injected‮")
	for _, bad := range []string{"\x1b", "\n", "\r", "‮"} {
		if strings.Contains(got, bad) {
			t.Errorf("line contains raw %q: %q", bad, got)
		}
	}
	if !strings.HasPrefix(got, "odek: sandbox network=none pids=4096 image=") {
		t.Errorf("line prefix wrong: %q", got)
	}
}

func TestSandboxPostureView(t *testing.T) {
	v := sandboxPostureView(config.ResolvedConfig{SandboxNetwork: "bridge", SandboxPidsLimit: "256", SandboxImage: "alpine:3"})
	if v["network"] != "bridge" || v["pids_limit"] != 256 || v["image"] != "alpine:3" {
		t.Errorf("view = %#v", v)
	}
	d := sandboxPostureView(config.ResolvedConfig{})
	if d["network"] != "none" || d["pids_limit"] != 4096 {
		t.Errorf("defaults view = %#v", d)
	}
}

func TestConfigView_ReportsSandboxPosture(t *testing.T) {
	resolved := config.ResolvedConfig{Sandbox: true, SandboxNetwork: "bridge", SandboxPidsLimit: "256", SandboxImage: "alpine:3"}
	sb, ok := buildConfigView(resolved)["sandbox"].(map[string]any)
	if !ok {
		t.Fatal("config view has no sandbox map")
	}
	if sb["pids_limit"] != 256 || sb["network"] != "bridge" || sb["image"] != "alpine:3" {
		t.Errorf("sandbox view = %#v", sb)
	}
	if def := buildConfigView(config.ResolvedConfig{})["sandbox"].(map[string]any); def["pids_limit"] != 4096 {
		t.Errorf("default pids_limit = %#v, want 4096", def["pids_limit"])
	}

	w := httptest.NewRecorder()
	handleConfigView(resolved)(w, httptest.NewRequest("GET", "/api/config", nil))
	var body struct {
		Sandbox map[string]any `json:"sandbox"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Sandbox["pids_limit"] != float64(256) {
		t.Errorf("REST sandbox view = %#v", body.Sandbox)
	}
}
