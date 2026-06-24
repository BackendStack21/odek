package danger

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── CheckOperation ──────────────────────────────────────────────────────

// fakeApprover is a test double recording the operations it was asked to
// approve and returning a configurable error.
type fakeApprover struct {
	err      error
	gotOps   []ToolOperation
	gotCmds  []string
	gotClass []RiskClass
}

func (f *fakeApprover) PromptCommand(cls RiskClass, cmd, _ string) error {
	f.gotClass = append(f.gotClass, cls)
	f.gotCmds = append(f.gotCmds, cmd)
	return f.err
}

func (f *fakeApprover) PromptOperation(op ToolOperation) error {
	f.gotOps = append(f.gotOps, op)
	return f.err
}

func TestCheckOperation_Allow(t *testing.T) {
	cfg := &DangerousConfig{}
	// Safe defaults to allow — no approver should be consulted.
	op := ToolOperation{Name: "read_file", Resource: "x.txt", Risk: Safe}
	if err := cfg.CheckOperation(op, nil); err != nil {
		t.Errorf("CheckOperation(safe) = %v, want nil", err)
	}
}

func TestCheckOperation_Deny(t *testing.T) {
	cfg := &DangerousConfig{}
	op := ToolOperation{Name: "shell", Resource: "rm -rf /", Risk: Destructive}
	err := cfg.CheckOperation(op, nil)
	if err == nil {
		t.Fatal("CheckOperation(destructive) should deny by default")
	}
	if !strings.Contains(err.Error(), "denied by configuration") {
		t.Errorf("deny error = %q, want it to mention configuration", err)
	}
}

func TestCheckOperation_BlockedAlwaysDenies(t *testing.T) {
	// Even with a global allow (YOLO), Blocked must still deny.
	cfg := &DangerousConfig{DefaultAction: strPtr("allow")}
	op := ToolOperation{Name: "shell", Resource: "dd of=/dev/sda", Risk: Blocked}
	if err := cfg.CheckOperation(op, nil); err == nil {
		t.Error("CheckOperation(blocked) must deny even in YOLO mode")
	}
}

func TestCheckOperation_Prompt_DelegatesToApprover(t *testing.T) {
	fa := &fakeApprover{}
	cfg := &DangerousConfig{Approver: fa}
	op := ToolOperation{Name: "browser", Resource: "https://x.com", Risk: NetworkEgress}
	if err := cfg.CheckOperation(op, nil); err != nil {
		t.Errorf("CheckOperation = %v, want nil (approver approved)", err)
	}
	if len(fa.gotOps) != 1 || fa.gotOps[0].Resource != "https://x.com" {
		t.Errorf("approver did not receive the operation: %+v", fa.gotOps)
	}
}

func TestCheckOperation_Prompt_ApproverDenies(t *testing.T) {
	fa := &fakeApprover{err: os.ErrPermission}
	cfg := &DangerousConfig{Approver: fa}
	op := ToolOperation{Name: "shell", Resource: "curl x", Risk: NetworkEgress}
	if err := cfg.CheckOperation(op, nil); err == nil {
		t.Error("CheckOperation should propagate the approver's denial")
	}
}

func TestCheckOperation_Prompt_NilApproverUsesTTYWithTrust(t *testing.T) {
	// With no approver configured, CheckOperation builds a TTYApprover and
	// injects trustedClasses. A trusted class short-circuits before any TTY is
	// opened, so this exercises the fallback branch deterministically.
	cfg := &DangerousConfig{}
	op := ToolOperation{Name: "shell", Resource: "tee /etc/x", Risk: SystemWrite}
	trusted := map[RiskClass]bool{SystemWrite: true}
	if err := cfg.CheckOperation(op, trusted); err != nil {
		t.Errorf("CheckOperation with trusted class = %v, want nil", err)
	}
}

// ── Interactive prompt() via a file-backed TTY ──────────────────────────

// ttyWithInput writes content to a temp file and returns its path, usable as a
// TTYApprover.TTYPath. prompt() opens it O_RDWR and reads one line. It also
// silences os.Stderr for the test, since prompt() writes its banner there.
func ttyWithInput(t *testing.T, content string) string {
	t.Helper()
	silenceStderr(t)
	p := filepath.Join(t.TempDir(), "tty")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write fake tty: %v", err)
	}
	return p
}

// silenceStderr redirects os.Stderr to os.DevNull for the duration of the test
// so the approver's prompt banner does not clutter test output.
func silenceStderr(t *testing.T) {
	t.Helper()
	orig := os.Stderr
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return // best-effort; leave stderr as-is
	}
	os.Stderr = devnull
	t.Cleanup(func() {
		os.Stderr = orig
		devnull.Close()
	})
}

func TestPrompt_Approve(t *testing.T) {
	for _, in := range []string{"a\n", "approve\n", "A\n", "  approve  \n"} {
		a := NewTTYApprover(nil)
		a.TTYPath = ttyWithInput(t, in)
		if err := a.PromptCommand(SystemWrite, "tee /etc/x", "write"); err != nil {
			t.Errorf("PromptCommand(%q) = %v, want approve", in, err)
		}
	}
}

func TestPrompt_Deny(t *testing.T) {
	for _, in := range []string{"d\n", "no\n", "deny\n", "\n", "x\n"} {
		a := NewTTYApprover(nil)
		a.TTYPath = ttyWithInput(t, in)
		if err := a.PromptCommand(SystemWrite, "tee /etc/x", "write"); err == nil {
			t.Errorf("PromptCommand(%q) = nil, want deny", in)
		}
	}
}

func TestPrompt_TrustSession(t *testing.T) {
	a := NewTTYApprover(nil)
	a.TTYPath = ttyWithInput(t, "t\n")
	if err := a.PromptCommand(NetworkEgress, "curl x", ""); err != nil {
		t.Fatalf("trust = %v, want nil", err)
	}
	// The class is now cached: a second call must not even open the TTY.
	a.TTYPath = "/nonexistent/tty"
	a.DangerousConfig = &DangerousConfig{NonInteractive: strPtr("deny")}
	if err := a.PromptCommand(NetworkEgress, "curl y", ""); err != nil {
		t.Errorf("trusted class second call = %v, want nil (cached)", err)
	}
}

func TestPrompt_Friction_RequiresFullWord(t *testing.T) {
	a := NewTTYApprover(nil)
	a.FrictionThreshold = 1
	a.FrictionWindow = time.Minute
	a.pauseFn = func(time.Duration) {} // no real sleep in tests
	a.recordApproval(SystemWrite)      // trip the friction threshold

	// In friction mode the single-letter "a" is rejected.
	a.TTYPath = ttyWithInput(t, "a\n")
	if err := a.PromptCommand(SystemWrite, "tee /etc/x", ""); err == nil {
		t.Error("friction mode must reject single-letter 'a'")
	}

	// The full word "approve" is accepted.
	a.TTYPath = ttyWithInput(t, "approve\n")
	if err := a.PromptCommand(SystemWrite, "tee /etc/x", ""); err != nil {
		t.Errorf("friction mode full-word approve = %v, want nil", err)
	}
}

func TestPrompt_TrustDisabledForDestructive(t *testing.T) {
	// trust-session is unavailable for Destructive; a direct approve still works
	// and must not cache the class.
	a := NewTTYApprover(nil)
	a.TTYPath = ttyWithInput(t, "a\n")
	if err := a.PromptCommand(Destructive, "rm -rf /", ""); err != nil {
		t.Fatalf("approve destructive = %v, want nil", err)
	}
	a.mu.Lock()
	cached := a.TrustedClasses[Destructive]
	a.mu.Unlock()
	if cached {
		t.Error("Destructive must never be cached as a trusted class")
	}
}

func TestPrompt_ReadErrorWhenNoNewline(t *testing.T) {
	// A TTY that yields input without a trailing newline makes ReadString hit
	// EOF; prompt surfaces that as an error rather than silently approving.
	a := NewTTYApprover(nil)
	a.TTYPath = ttyWithInput(t, "a") // no newline
	if err := a.PromptCommand(SystemWrite, "tee /etc/x", ""); err == nil {
		t.Error("expected a read error when input has no newline")
	}
}

func TestRecordApproval_InitialisesNilLog(t *testing.T) {
	// A zero-value TTYApprover (no NewTTYApprover) has a nil approvalLog;
	// recordApproval must lazily initialise it.
	a := &TTYApprover{}
	a.recordApproval(SystemWrite)
	a.mu.Lock()
	n := len(a.approvalLog[SystemWrite])
	a.mu.Unlock()
	if n != 1 {
		t.Errorf("approvalLog[SystemWrite] = %d entries, want 1", n)
	}
}

func TestIsSensitiveOdekPath_NoHome(t *testing.T) {
	// With $HOME unset, the home lookup fails and the path is not sensitive.
	t.Setenv("HOME", "")
	if isSensitiveOdekPath("~/.odek/config.json") {
		t.Error("with no home, odek path should not be sensitive")
	}
}

func TestPrompt_NonInteractiveAllowWhenNoTTY(t *testing.T) {
	// No TTY + NonInteractive allow (or nil config) → approve.
	a := NewTTYApprover(&DangerousConfig{NonInteractive: strPtr("allow")})
	a.TTYPath = "/nonexistent/tty-allow"
	if err := a.PromptCommand(SystemWrite, "x", ""); err != nil {
		t.Errorf("no-tty allow = %v, want nil", err)
	}
}

// ── isOdekTrustAnchor (env-independent, via synthetic home) ─────────────

func TestIsOdekTrustAnchor(t *testing.T) {
	home := "/home/agent"
	mk := func(rel string) string { return home + "/.odek/" + rel }
	anchors := []string{
		"config.json", "secrets.env", "IDENTITY.md", "schedules.json",
		"schedule-state.json", "schedules.lock", "mcp_approvals.json",
		"mcp_tool_approvals.json", "restart.json", "telegram.lock",
		"telegram.pid", "schedule.pid", "schedule.log",
		"skills", "skills/evil/SKILL.md", "sessions/s.json",
		"audit/turn-1.json", "plans/p.md",
	}
	for _, rel := range anchors {
		if !isOdekTrustAnchor(home, mk(rel)) {
			t.Errorf("isOdekTrustAnchor(%q) = false, want true", rel)
		}
	}
	nonAnchors := []string{
		mk("memory/episodes.json"), mk("notes.md"), mk("media/a.jpg"),
		mk("skillsZZ/x"),            // prefix-but-not-dir must not match
		home + "/other/config.json", // outside ~/.odek
		"/etc/config.json",          // unrelated absolute path
	}
	for _, abs := range nonAnchors {
		if isOdekTrustAnchor(home, abs) {
			t.Errorf("isOdekTrustAnchor(%q) = true, want false", abs)
		}
	}
	// Empty home is never an anchor.
	if isOdekTrustAnchor("", "/anything/.odek/config.json") {
		t.Error("empty home must not match any anchor")
	}
}

// ── ANSI-C escape decoding ──────────────────────────────────────────────

func TestDecodeANSIC(t *testing.T) {
	tests := []struct{ in, want string }{
		{`$'\x72\x6d'`, "rm"},                // hex
		{`$'\162\155'`, "rm"},                // octal
		{`$'a\nb'`, "a\nb"},                  // \n
		{`$'a\tb'`, "a\tb"},                  // \t
		{`$'a\rb'`, "a\rb"},                  // \r
		{`$'a\\b'`, `a\b`},                   // escaped backslash
		{`$'a\'b'`, "a'b"},                   // escaped single quote
		{`$'a\"b'`, `a"b`},                   // escaped double quote
		{`$'\q'`, "q"},                       // unknown escape → literal char
		{`plain text`, "plain text"},         // no ANSI-C span untouched
		{`$'unterminated`, `$'unterminated`}, // no closing quote → literal
	}
	for _, tt := range tests {
		if got := decodeANSIC(tt.in); got != tt.want {
			t.Errorf("decodeANSIC(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ── parseBrowserIP (inet_aton-style encodings) ──────────────────────────

func TestParseBrowserIP(t *testing.T) {
	tests := []struct {
		host    string
		wantNil bool
		wantIP  string
	}{
		{"127.0.0.1", false, "127.0.0.1"},
		{"0177.0.0.1", false, "127.0.0.1"}, // octal
		{"0x7f.0.0.1", false, "127.0.0.1"}, // hex octet
		{"0x7f000001", false, "127.0.0.1"}, // single hex int
		{"2130706433", false, "127.0.0.1"}, // single decimal int
		{"127.1", false, "127.0.0.1"},      // short form a.b
		{"127.0.1", false, "127.0.0.1"},    // short form a.b.c
		{"::1", false, "::1"},              // IPv6
		{"1.2.3.4.5", true, ""},            // too many parts
		{"99999999999.1", true, ""},        // part exceeds 32 bits → nil
		{"0xZZ.0.0.1", true, ""},           // bad hex
		{"not.an.ip.addr", true, ""},       // non-numeric
		{"", true, ""},                     // empty
	}
	for _, tt := range tests {
		ip := parseBrowserIP(tt.host)
		if tt.wantNil {
			if ip != nil {
				t.Errorf("parseBrowserIP(%q) = %v, want nil", tt.host, ip)
			}
			continue
		}
		if ip == nil {
			t.Errorf("parseBrowserIP(%q) = nil, want %s", tt.host, tt.wantIP)
			continue
		}
		want := net.ParseIP(tt.wantIP)
		if !ip.Equal(want) {
			t.Errorf("parseBrowserIP(%q) = %v, want %v", tt.host, ip, want)
		}
	}
}

// ── hostnameIsInternal ──────────────────────────────────────────────────

func TestHostnameIsInternal(t *testing.T) {
	internal := []string{
		"localhost", "LOCALHOST", "localhost.localdomain",
		"localhost6", "ip6-localhost", "ip6-loopback",
		"printer.local", "169.254.169.254", "[fd00:ec2::254]",
		"metadata.google.internal", "metadata.internal",
		"db.svc.internal", "host.docker.internal",
	}
	for _, h := range internal {
		if !hostnameIsInternal(h) {
			t.Errorf("hostnameIsInternal(%q) = false, want true", h)
		}
	}
	external := []string{"example.com", "api.github.com", "google.com", "internal.com.evil.net"}
	for _, h := range external {
		if hostnameIsInternal(h) {
			t.Errorf("hostnameIsInternal(%q) = true, want false", h)
		}
	}
}

// ── Small helpers ───────────────────────────────────────────────────────

func TestIsOctalMode(t *testing.T) {
	for _, s := range []string{"0", "755", "4755", "0000", "7777"} {
		if !isOctalMode(s) {
			t.Errorf("isOctalMode(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "8", "9", "75a", "0x7", "-1"} {
		if isOctalMode(s) {
			t.Errorf("isOctalMode(%q) = true, want false", s)
		}
	}
}

func TestChmodSetsSUIDGID_Direct(t *testing.T) {
	yes := [][]string{
		{"chmod", "u+s", "f"}, {"chmod", "g+s", "f"}, {"chmod", "+s", "f"},
		{"chmod", "4755", "f"}, {"chmod", "2755", "f"}, {"chmod", "6755", "f"},
		{"chmod", "-R", "u+s", "f"}, // flag skipped, mode still inspected
	}
	for _, toks := range yes {
		if !chmodSetsSUIDGID(toks) {
			t.Errorf("chmodSetsSUIDGID(%v) = false, want true", toks)
		}
	}
	no := [][]string{
		{"chmod", "+x", "f"}, {"chmod", "755", "f"}, {"chmod", "0755", "f"},
		{"chmod", "1755", "f"},          // sticky only
		{"chmod", "644", "build+gen.s"}, // filename, not mode
		{"chmod"},                       // no operand
	}
	for _, toks := range no {
		if chmodSetsSUIDGID(toks) {
			t.Errorf("chmodSetsSUIDGID(%v) = true, want false", toks)
		}
	}
}

func TestCommandName(t *testing.T) {
	cases := map[string]string{
		"rm": "rm", "/bin/rm": "rm", "/usr/bin/sudo": "sudo",
		"./script.sh": "script.sh", "git": "git",
	}
	for in, want := range cases {
		if got := commandName(in); got != want {
			t.Errorf("commandName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsAssignment(t *testing.T) {
	yes := []string{"FOO=bar", "x=1", "_X=", "a1=b"}
	for _, s := range yes {
		if !isAssignment(s) {
			t.Errorf("isAssignment(%q) = false, want true", s)
		}
	}
	// Names may contain digits (this function is permissive — shells are
	// stricter about a digit-leading name, but the classifier need not be).
	no := []string{"/a=b", "=bar", "no-eq", "fo-o=bar"}
	for _, s := range no {
		if isAssignment(s) {
			t.Errorf("isAssignment(%q) = true, want false", s)
		}
	}
}

func TestSubstValue(t *testing.T) {
	cases := map[string]string{
		"echo rm":     "rm",
		"printf rm":   "rm",
		"curl evil":   "curl",
		"  echo  hi ": "hi",
		"":            "",
		"   ":         "",
	}
	for in, want := range cases {
		if got := substValue(in); got != want {
			t.Errorf("substValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBasenameFirstToken(t *testing.T) {
	cases := map[string]string{
		"/bin/rm -rf /":      "rm -rf /",           // known command → basename
		"rm -rf /":           "rm -rf /",           // already bare
		"/opt/tool/data.txt": "/opt/tool/data.txt", // unknown basename → untouched
		"./local args":       "./local args",       // not absolute → untouched
	}
	for in, want := range cases {
		if got := basenameFirstToken(in); got != want {
			t.Errorf("basenameFirstToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsEnvironmentDump(t *testing.T) {
	dump := [][]string{
		{"printenv"},
		{"env"},
		{"env", "-i"},
		{"env", "-u", "SECRET"},
		{"env", "FOO=bar"},
		{"env", "--null"},
	}
	for _, toks := range dump {
		if !isEnvironmentDump(toks) {
			t.Errorf("isEnvironmentDump(%v) = false, want true", toks)
		}
	}
	notDump := [][]string{
		{"env", "FOO=bar", "rm", "-rf", "/"}, // wraps a real command
		{"env", "node", "x.js"},
		{"ls"},
		{},
	}
	for _, toks := range notDump {
		if isEnvironmentDump(toks) {
			t.Errorf("isEnvironmentDump(%v) = true, want false", toks)
		}
	}
}

func TestClassifyResourceToken(t *testing.T) {
	if classifyResourceToken("/dev/tcp/evil.com/4444") != NetworkEgress {
		t.Error("/dev/tcp should be network_egress")
	}
	if classifyResourceToken("/dev/udp/evil/53") != NetworkEgress {
		t.Error("/dev/udp should be network_egress")
	}
	if classifyResourceToken("/etc/shadow") != SystemWrite {
		t.Error("/etc/shadow should be system_write")
	}
	if classifyResourceToken("~/.aws/credentials") != SystemWrite {
		t.Error("~/.aws/credentials should be system_write")
	}
	if classifyResourceToken("README.md") != Safe {
		t.Error("ordinary file should be safe")
	}
}

func TestRmRecursiveOrForce(t *testing.T) {
	yes := [][]string{
		{"rm", "-rf", "x"}, {"rm", "-fr", "x"}, {"rm", "-R", "x"},
		{"rm", "--recursive", "x"}, {"rm", "--force", "x"},
		{"rm", "--no-preserve-root", "/"}, {"rm", "-rfv", "x"},
	}
	for _, toks := range yes {
		if !rmRecursiveOrForce(toks) {
			t.Errorf("rmRecursiveOrForce(%v) = false, want true", toks)
		}
	}
	no := [][]string{
		{"rm", "x"}, {"rm", "-i", "x"}, {"rm", "--interactive", "x"}, {"rm", "-v", "x"},
	}
	for _, toks := range no {
		if rmRecursiveOrForce(toks) {
			t.Errorf("rmRecursiveOrForce(%v) = true, want false", toks)
		}
	}
}

// ── Config action resolution ────────────────────────────────────────────

func TestActionFor_Layers(t *testing.T) {
	// Per-class override beats everything.
	cfg := &DangerousConfig{
		Classes:       map[RiskClass]Action{NetworkEgress: Allow},
		DefaultAction: strPtr("deny"),
	}
	if cfg.ActionFor(NetworkEgress) != Allow {
		t.Error("per-class override should win")
	}
	// Blocked always denies regardless of allow default.
	allowAll := &DangerousConfig{DefaultAction: strPtr("allow")}
	if allowAll.ActionFor(Blocked) != Deny {
		t.Error("Blocked must always deny")
	}
	// Global default applies when no per-class override.
	if allowAll.ActionFor(Destructive) != Allow {
		t.Error("global allow should apply to destructive")
	}
	// Built-in default when nothing configured.
	empty := &DangerousConfig{}
	if empty.ActionFor(SystemWrite) != Prompt {
		t.Error("built-in default for system_write should be prompt")
	}
}

func TestActionForCommand_EmptyAndClass(t *testing.T) {
	cfg := &DangerousConfig{}
	if cfg.ActionForCommand("") != Allow {
		t.Error("empty command should allow")
	}
	if cfg.ActionForCommand("rm -rf /") != Deny {
		t.Error("rm -rf / classifies destructive → deny by default")
	}
	if cfg.ActionForCommand("ls") != Allow {
		t.Error("ls is safe → allow")
	}
}

func TestNonInteractiveAction(t *testing.T) {
	if (&DangerousConfig{}).NonInteractiveAction() != Deny {
		t.Error("default non-interactive action should be deny")
	}
	if (&DangerousConfig{NonInteractive: strPtr("allow")}).NonInteractiveAction() != Allow {
		t.Error("configured non-interactive allow should be honored")
	}
}

// ── ClassifyPath / sensitive-odek-path with a synthetic $HOME ───────────
// The sandbox's real $HOME is /root, which short-circuits on the /root system
// prefix before the home-relative branches run. Pointing $HOME at a synthetic
// non-/tmp path (no disk access — ClassifyPath only does string prefixing)
// exercises those branches deterministically in any environment.

func TestClassifyPath_SyntheticHome(t *testing.T) {
	t.Setenv("HOME", "/home/agent")
	tests := []struct {
		path string
		want RiskClass
	}{
		{"/home/agent/.ssh/id_rsa", SystemWrite},
		{"/home/agent/.aws/credentials", SystemWrite},
		{"/home/agent/.gnupg/secring", SystemWrite},
		{"/home/agent/.odek/config.json", SystemWrite},
		{"/home/agent/.odek/skills/x/SKILL.md", SystemWrite},
		{"/home/agent/.bashrc", SystemWrite},
		{"/home/agent/.zshrc", SystemWrite},
		{"/home/agent/.vimrc", LocalWrite},          // dotfile, not an rc file
		{"/home/agent/project/main.go", LocalWrite}, // ordinary workspace file
		{"/home/agent/.odek/notes.md", LocalWrite},  // non-anchor odek state
		{"/boot/grub/grub.cfg", Destructive},
		{"/proc/sysrq-trigger", Destructive},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := ClassifyPath(tt.path); got != tt.want {
				t.Errorf("ClassifyPath(%q) = %s, want %s", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsSensitiveOdekPath_SyntheticHome(t *testing.T) {
	t.Setenv("HOME", "/home/agent")
	if !isSensitiveOdekPath("~/.odek/secrets.env") {
		t.Error("~/.odek/secrets.env should be a sensitive odek path")
	}
	if !isSensitiveOdekPath("/home/agent/.odek/IDENTITY.md") {
		t.Error("absolute odek anchor should be sensitive")
	}
	if isSensitiveOdekPath("~/.odek/notes.md") {
		t.Error("non-anchor odek state should not be sensitive")
	}
	if isSensitiveOdekPath("README.md") {
		t.Error("ordinary file should not be sensitive")
	}
}

// ── Remaining small branches ────────────────────────────────────────────

func TestDecodeANSIC_EscapeEdgeCases(t *testing.T) {
	tests := []struct{ in, want string }{
		{`$'\x'`, `x`},     // incomplete \x → literal x
		{`$'\xZZ'`, `xZZ`}, // bad hex digits → literal
		{`$'\8'`, `8`},     // 8 is not octal → literal
		{`$'\a\b'`, `ab`},  // unrecognised escapes → literal chars
	}
	for _, tt := range tests {
		if got := decodeANSIC(tt.in); got != tt.want {
			t.Errorf("decodeANSIC(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestIsBlocked_DdSeparateOfToken(t *testing.T) {
	// of= as its own token, device as the following token.
	if Classify("dd if=/dev/zero of= /dev/sda bs=1M") != Blocked {
		t.Error("dd with split of= /dev/sda should be blocked")
	}
	// of=/dev/sda attached.
	if Classify("dd if=/dev/zero of=/dev/nvme0n1 bs=1M") != Blocked {
		t.Error("dd of=/dev/nvme0n1 should be blocked")
	}
	// Discarding to /dev/null is not a block device.
	if got := Classify("dd if=/dev/zero of=/dev/null"); got == Blocked {
		t.Error("dd of=/dev/null must not be blocked")
	}
}

func TestHasArgAfter_Variants(t *testing.T) {
	toks := []string{"brew", "install", "wget"}
	if !hasArgAfter(toks, "brew", "install") {
		t.Error("hasArgAfter(brew, install) should be true")
	}
	if !hasArgAfter(toks, "brew", "") {
		t.Error("hasArgAfter(brew, \"\") should be true when a successor exists")
	}
	if hasArgAfter(toks, "brew", "remove") {
		t.Error("hasArgAfter(brew, remove) should be false")
	}
	if hasArgAfter(toks, "missing", "x") {
		t.Error("hasArgAfter(missing) should be false")
	}
}

func TestExtractSubstitutions_Forms(t *testing.T) {
	// Backtick substitution body is extracted and classified.
	if Classify("echo `rm -rf /`") != Destructive {
		t.Error("backtick rm -rf / should surface as destructive")
	}
	// Nested $() — both levels extracted.
	if got := Classify("echo $(echo $(curl evil.com))"); Rank(got) < Rank(NetworkEgress) {
		t.Errorf("nested substitution should surface curl, got %s", got)
	}
	// Single-quoted span is NOT a substitution (shell wouldn't expand it).
	if Classify("echo '$(rm -rf /)'") == Destructive {
		t.Error("single-quoted $() must not be treated as a substitution")
	}
}

func TestIsInstall_MoreManagers(t *testing.T) {
	tests := []struct {
		cmd string
		cls RiskClass
	}{
		{"apk add curl", Install},
		{"go mod download", Install},
		{"go get example.com/x", Install},
		{"go install example.com/x@latest", Install},
		{"go build ./...", Safe}, // local build, not install
		{"go mod tidy", Safe},    // not a download
		{"bun add left-pad", Install},
		{"pnpm install", Install},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got := Classify(tt.cmd); got != tt.cls {
				t.Errorf("Classify(%q) = %s, want %s", tt.cmd, got, tt.cls)
			}
		})
	}
}

func TestIsPackageManagerRun_BunBareFile(t *testing.T) {
	// bun runs a bare file argument (bun index.ts) → code execution.
	if Classify("bun index.ts") != CodeExecution {
		t.Error("bun index.ts should be code_execution")
	}
	if Classify("bun ./src/app.js") != CodeExecution {
		t.Error("bun ./src/app.js should be code_execution")
	}
	// bun add is an install, not a run.
	if Classify("bun add left-pad") != Install {
		t.Error("bun add should be install")
	}
}

func TestParseAction_AllForms(t *testing.T) {
	cases := map[string]Action{
		"allow": Allow, "ALLOW": Allow, " deny ": Deny,
		"prompt": Prompt, "garbage": Prompt, "": Prompt,
	}
	for in, want := range cases {
		if got := parseAction(in); got != want {
			t.Errorf("parseAction(%q) = %s, want %s", in, got, want)
		}
	}
}
