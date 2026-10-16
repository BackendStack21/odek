package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
)

// initScratch runs `odek init --global` and `odek init` in a fresh HOME and
// project directory, so tests observe exactly what a new user gets.
func initScratch(t *testing.T) (home, proj string) {
	t.Helper()
	home, proj = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(proj)
	if err := initConfig([]string{"--global"}); err != nil {
		t.Fatalf("init --global: %v", err)
	}
	if err := initConfig(nil); err != nil {
		t.Fatalf("init: %v", err)
	}
	return home, proj
}

// TestRED_FreshLocalInitNeedsNoSandboxApproval: a freshly generated
// ./odek.json must not count as a project sandbox override. It used to pin
// "sandbox_network": "none", which made every non-interactive run in that
// project fail with "requires explicit approval".
func TestRED_FreshLocalInitNeedsNoSandboxApproval(t *testing.T) {
	initScratch(t)
	t.Setenv("ODEK_APPROVE_PROJECT_SANDBOX", "")
	resolved := config.LoadConfig(config.CLIFlags{})
	if o := resolved.ProjectSandboxOverride; o.HasEnv || o.HasImage || o.HasNetwork || o.HasVolumes || o.HasUser || o.HasMemory || o.HasCPUs {
		t.Errorf("fresh odek init produced a project sandbox override: %+v", o)
	}
	if err := approveProjectSandboxWithTTY(resolved, nil, io.Discard, false); err != nil {
		t.Errorf("fresh odek init needs sandbox approval without a TTY: %v", err)
	}
}

// TestRED_FreshLocalInitInheritsGlobalValues: the local template documents
// knobs in inherit-neutral form only, so adding it must not change the
// resolved planning or skills settings (zeros used to clamp planning down to
// its minimum, and pinned skills values overrode the operator's).
func TestRED_FreshLocalInitInheritsGlobalValues(t *testing.T) {
	initScratch(t)
	withLocal := config.LoadConfig(config.CLIFlags{})

	t.Chdir(t.TempDir()) // same global config, no ./odek.json
	globalOnly := config.LoadConfig(config.CLIFlags{})

	if withLocal.Planning.MaxSteps != globalOnly.Planning.MaxSteps ||
		withLocal.Planning.MaxRenderChars != globalOnly.Planning.MaxRenderChars {
		t.Errorf("local template changed planning: steps %d→%d, render %d→%d",
			globalOnly.Planning.MaxSteps, withLocal.Planning.MaxSteps,
			globalOnly.Planning.MaxRenderChars, withLocal.Planning.MaxRenderChars)
	}
	if withLocal.Skills.MaxAutoLoad != globalOnly.Skills.MaxAutoLoad ||
		withLocal.Skills.MaxLazySlots != globalOnly.Skills.MaxLazySlots {
		t.Errorf("local template changed skills: auto_load %d→%d, lazy %d→%d",
			globalOnly.Skills.MaxAutoLoad, withLocal.Skills.MaxAutoLoad,
			globalOnly.Skills.MaxLazySlots, withLocal.Skills.MaxLazySlots)
	}
}

// TestRED_FreshGlobalInitNeedsNoExternalDependencies: what `odek init
// --global` writes must resolve to a setup that works with the binary and
// one API key — no PIGuard sidecar, SearXNG, whisper.cpp, local vision
// models, MCP servers or Telegram bot.
func TestRED_FreshGlobalInitNeedsNoExternalDependencies(t *testing.T) {
	initScratch(t)
	t.Chdir(t.TempDir())
	r := config.LoadConfig(config.CLIFlags{})
	if r.Guard.Provider != "local" {
		t.Errorf("guard.provider = %q, want local (no sidecar)", r.Guard.Provider)
	}
	if r.WebSearch.BaseURL != "" {
		t.Errorf("web_search.base_url = %q, want empty (no SearXNG)", r.WebSearch.BaseURL)
	}
	if r.Transcription.AutoTranscribe {
		t.Error("transcription.auto_transcribe is on (needs whisper.cpp)")
	}
	if r.Vision.AutoDescribeEnabled() {
		t.Error("vision.auto_describe is on (needs MiniCPM-V or a vision model)")
	}
	if len(r.MCPServers) != 0 {
		t.Errorf("mcp_servers = %v, want none", r.MCPServers)
	}
	if r.Telegram.Token != "" {
		t.Error("telegram.bot_token is set")
	}
}

// TestRED_GlobalTemplateGuardHasNoSidecarFields: the template enables the
// zero-dependency local guard, so it must not carry PIGuard sidecar
// settings — they are inert under "local", and the pinned 5s timeout would
// silently override the deliberate 1s code default for anyone who later
// switches to piguard. docs/CONFIG.md documents the sidecar fields.
func TestRED_GlobalTemplateGuardHasNoSidecarFields(t *testing.T) {
	var fc struct {
		Guard map[string]any `json:"guard"`
	}
	if err := json.Unmarshal([]byte(globalConfigTemplate), &fc); err != nil {
		t.Fatal(err)
	}
	if fc.Guard["provider"] != "local" {
		t.Errorf("guard.provider = %v, want local", fc.Guard["provider"])
	}
	for key := range fc.Guard {
		if key != "provider" && key != "scan" {
			t.Errorf("guard.%s is a PIGuard sidecar field; leave it out of the template", key)
		}
	}
}

// TestRED_GlobalTemplateRequiresHTTPSForSkillImports: a fresh operator
// config refuses plain-HTTP skill imports, which can be tampered with in
// transit. Projects may tighten this but never relax it.
func TestRED_GlobalTemplateRequiresHTTPSForSkillImports(t *testing.T) {
	initScratch(t)
	t.Chdir(t.TempDir())
	if !config.LoadConfig(config.CLIFlags{}).Skills.Import.RequireHTTPS {
		t.Error("odek init --global leaves skills.import.require_https off")
	}
}

// TestRED_GlobalInitKeepsOdekHomePrivate: ~/.odek holds sessions, memory,
// audit logs and approval stores, so init must create it 0700, and
// --force must not carry group/other bits of an existing config over.
func TestRED_GlobalInitKeepsOdekHomePrivate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := initConfig([]string{"--global"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(home, ".odek"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("~/.odek created with mode %o, want 700", perm)
	}

	cfg := config.GlobalConfigPath()
	if err := os.Chmod(cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := initConfig([]string{"--global", "--force"}); err != nil {
		t.Fatal(err)
	}
	fi, err = os.Stat(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("--force kept a group/other-readable config (mode %o)", perm)
	}
}

// TestRED_GlobalInitScaffoldsPrivateSecretsFile: init --global tells a new
// user where the API key goes and creates a private ~/.odek/secrets.env
// stub (nothing set, only a commented placeholder). An existing
// secrets.env is never touched, even with --force.
func TestRED_GlobalInitScaffoldsPrivateSecretsFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	out := opsHuntCaptureStdout(t, func() {
		if err := initConfig([]string{"--global"}); err != nil {
			t.Error(err)
		}
	})
	secrets := filepath.Join(home, ".odek", "secrets.env")
	fi, err := os.Stat(secrets)
	if err != nil {
		t.Fatalf("secrets.env not created: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("secrets.env mode %o, want 600", perm)
	}
	body, _ := os.ReadFile(secrets)
	if !strings.Contains(string(body), "# DEEPSEEK_API_KEY=") {
		t.Errorf("secrets.env lacks the commented DEEPSEEK_API_KEY placeholder:\n%s", body)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "#") {
			t.Errorf("secrets.env stub sets a value: %q", l)
		}
	}
	for _, want := range []string{"secrets.env", "DEEPSEEK_API_KEY"} {
		if !strings.Contains(out, want) {
			t.Errorf("init --global output does not mention %s:\n%s", want, out)
		}
	}

	if err := os.WriteFile(secrets, []byte("DEEPSEEK_API_KEY=keep-me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opsHuntCaptureStdout(t, func() { _ = initConfig([]string{"--global", "--force"}) })
	if got, _ := os.ReadFile(secrets); string(got) != "DEEPSEEK_API_KEY=keep-me\n" {
		t.Errorf("init --global --force rewrote an existing secrets.env: %q", got)
	}
}

// TestRED_LocalInitCreatesNoSecretsFile: project init never writes into
// ~/.odek.
func TestRED_LocalInitCreatesNoSecretsFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	opsHuntCaptureStdout(t, func() { _ = initConfig(nil) })
	if _, err := os.Lstat(filepath.Join(home, ".odek")); err == nil {
		t.Error("odek init (local) created ~/.odek")
	}
}

// projectIgnoredSamples holds one project-config snippet per field that
// `odek init` says the loader ignores in ./odek.json.
var projectIgnoredSamples = map[string]string{
	"provider":                        `{"provider":"openai"}`,
	"providers":                       `{"providers":{"openai":{"api_key":"x"}}}`,
	"api_key":                         `{"api_key":"x"}`,
	"base_url":                        `{"base_url":"http://127.0.0.1:1"}`,
	"llm":                             `{"llm":{"request_timeout_seconds":5}}`,
	"system":                          `{"system":"x"}`,
	"dangerous":                       `{"dangerous":{"non_interactive":"allow"}}`,
	"memory":                          `{"memory":{"enabled":false}}`,
	"sessions":                        `{"sessions":{"embedding":{"provider":"rp"}}}`,
	"embedding":                       `{"embedding":{"provider":"rp"}}`,
	"guard":                           `{"guard":{"provider":"local"}}`,
	"maintenance":                     `{"maintenance":{"enabled":false}}`,
	"logging":                         `{"logging":{"enabled":false}}`,
	"telegram":                        `{"telegram":{"bot_token":"x"}}`,
	"web_search":                      `{"web_search":{"base_url":"http://127.0.0.1:1"}}`,
	"transcription":                   `{"transcription":{"model":"tiny"}}`,
	"vision":                          `{"vision":{"backend":"local"}}`,
	"tts":                             `{"tts":{"backend":"provider"}}`,
	"stt":                             `{"stt":{"backend":"local"}}`,
	"trusted_proxies":                 `{"trusted_proxies":["127.0.0.1"]}`,
	"schedules":                       `{"schedules":{"enabled":false}}`,
	"subagent":                        `{"subagent":{"max_depth":1}}`,
	"verify":                          `{"verify":{"enabled":true}}`,
	"profiles":                        `{"profiles":{"p":{}}}`,
	"tools.enabled":                   `{"tools":{"enabled":["shell"]}}`,
	"skills.dirs":                     `{"skills":{"dirs":["/nonexistent"]}}`,
	"skills.embedding":                `{"skills":{"embedding":{"provider":"rp"}}}`,
	"mcp_servers.<name>.auto_approve": `{"mcp_servers":{"s":{"command":"true","auto_approve":true}}}`,
}

// TestRED_LocalInitSummaryMatchesLoader: every field the local init
// summary lists as ignored is really ignored (with a warning) by the
// loader, so the printed guidance cannot drift from the code again.
func TestRED_LocalInitSummaryMatchesLoader(t *testing.T) {
	for _, field := range projectIgnoredFields {
		sample, ok := projectIgnoredSamples[field]
		if !ok {
			t.Errorf("no sample for listed field %q", field)
			continue
		}
		t.Run(field, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("HOME", t.TempDir())
			t.Chdir(dir)
			if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(sample), 0o600); err != nil {
				t.Fatal(err)
			}
			stderr := captureStderrDuring(t, func() { config.LoadConfig(config.CLIFlags{}) })
			want := "ignoring " + strings.ReplaceAll(field, "<name>", "s")
			if !strings.Contains(stderr, want) {
				t.Errorf("loader did not warn %q for %s; stderr:\n%s", want, sample, stderr)
			}
		})
	}
	for field := range projectIgnoredSamples {
		if !slices.Contains(projectIgnoredFields, field) {
			t.Errorf("loader ignores %q from project config but odek init does not list it", field)
		}
	}
}

// TestRED_CLIErrorsCarryOnePrefix: errors that already start with "odek:"
// (the public API's convention) are printed with a single prefix.
func TestRED_CLIErrorsCarryOnePrefix(t *testing.T) {
	stderr := captureStderrDuring(t, func() { cliExit(errors.New("odek: llm: boom")) })
	if strings.Contains(stderr, "odek: odek:") {
		t.Errorf("doubled prefix: %q", stderr)
	}
	if !strings.Contains(stderr, "odek: llm: boom") {
		t.Errorf("error text lost: %q", stderr)
	}
}

// TestRED_RunReportsMissingKeyBeforeSandbox: with no API key, `odek run`
// fails with the key error and never starts (or warns about) the sandbox.
func TestRED_RunReportsMissingKeyBeforeSandbox(t *testing.T) {
	initScratch(t)
	for _, k := range []string{"ODEK_API_KEY", "DEEPSEEK_API_KEY", "OPENAI_API_KEY", "ODEK_NO_SANDBOX", "ODEK_REQUIRE_SANDBOX"} {
		t.Setenv(k, "")
	}
	var err error
	stderr := captureStderrDuring(t, func() { err = run([]string{"hello"}) })
	if err == nil || !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("run error = %v, want the missing-key error", err)
	}
	if strings.Contains(stderr, "sandbox") {
		t.Errorf("sandbox output before the missing-key error:\n%s", stderr)
	}
}

// TestRED_FreshGlobalInitKeepsBuiltinTools: the template documented
// tools.enabled as an empty list, but a present (non-nil) list is a strict
// allowlist, so every fresh config silently disabled every built-in tool.
func TestRED_FreshGlobalInitKeepsBuiltinTools(t *testing.T) {
	initScratch(t)
	t.Chdir(t.TempDir())
	r := config.LoadConfig(config.CLIFlags{})
	if r.Tools.Enabled != nil {
		t.Errorf("tools.enabled = %#v, want unset (nil): a present list is an allowlist", r.Tools.Enabled)
	}
	all := builtinTools(r.Dangerous, nil, nil, 1, "", toolConfigFromResolved(r), nil)
	got := filterBuiltinTools(all, r.Tools, nil)
	if len(got) != len(all) {
		t.Errorf("fresh config exposes %d of %d built-in tools", len(got), len(all))
	}
}

// TestRED_SecretsStubWithExistingConfig: a user who already has a global
// config but no secrets.env still gets the stub from `odek init --global`
// without having to --force-overwrite their config.
func TestRED_SecretsStubWithExistingConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := filepath.Join(home, ".odek", "config.json")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(`{"model":"mine"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	opsHuntCaptureStdout(t, func() { _ = initConfig([]string{"--global"}) })
	if got, _ := os.ReadFile(cfg); string(got) != `{"model":"mine"}` {
		t.Errorf("existing config rewritten without --force: %s", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".odek", "secrets.env")); err != nil {
		t.Errorf("secrets.env not scaffolded next to an existing config: %v", err)
	}
}

// TestRED_SecretsStubRefusesPlantedLink: a symlink (even dangling) at the
// secrets.env path is never written through or replaced.
func TestRED_SecretsStubRefusesPlantedLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	link := filepath.Join(dir, "secrets.env")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	created, err := scaffoldSecretsEnv(link)
	if created || err != nil {
		t.Errorf("scaffoldSecretsEnv over a dangling link = (%v, %v), want (false, nil)", created, err)
	}
	if _, err := os.Lstat(target); err == nil {
		t.Error("stub was written through the planted link")
	}
}

// TestRED_SecretsStubCleansUpOnWriteError: a failed write leaves no
// half-written stub behind and is not reported as created.
func TestRED_SecretsStubCleansUpOnWriteError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.env")
	orig := writeSecretsStub
	writeSecretsStub = func(*os.File) error { return errors.New("disk full") }
	defer func() { writeSecretsStub = orig }()
	created, err := scaffoldSecretsEnv(path)
	if created || err == nil {
		t.Errorf("scaffoldSecretsEnv = (%v, %v), want (false, error)", created, err)
	}
	if _, err := os.Lstat(path); err == nil {
		t.Error("partial secrets.env left behind after a write error")
	}
}
