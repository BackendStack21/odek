package danger

import "testing"

// False-positive pins from the 2026-09 danger-module review: everyday
// commands that used to over-classify (prompt/deny) without accessing a
// dangerous resource.

func TestClassify_FP_CredentialSubstringNotPath(t *testing.T) {
	safe := []string{
		"echo id_rsa",
		"echo my_id_rsa_backup",
		"grep id_rsa README.md",
		`echo "see ~/.ssh docs"`,
		`echo "check /etc/shadow docs"`,
		`echo "set /environ var"`,
		"echo /etc/passwd",
		"printf /usr/bin/foo",
	}
	for _, cmd := range safe {
		if got := Classify(cmd); got != Safe {
			t.Errorf("Classify(%q) = %s, want safe (display/search, not a path access)", cmd, got)
		}
	}
	// Real credential paths still escalate.
	for _, cmd := range []string{
		"cat ~/.ssh/id_rsa",
		"cat /etc/shadow",
		"cat /proc/self/environ",
	} {
		if got := Classify(cmd); Rank(got) < Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want >= system_write", cmd, got)
		}
	}
}

func TestClassify_FP_DevNullDiscard(t *testing.T) {
	benign := []string{
		"echo x > /dev/null",
		"ls 2> /dev/null",
		"cat file > /dev/null",
		"tee /dev/null",
		"dd of=/dev/null",
		"dd if=/dev/zero of=/dev/null bs=1M count=1",
		"dd of=/dev/stdout",
		"echo x > /dev/stderr",
	}
	for _, cmd := range benign {
		if got := Classify(cmd); Rank(got) >= Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want < system_write (char-device discard)", cmd, got)
		}
	}
	if got := Classify("dd if=/dev/zero of=/dev/sda bs=4M"); got != Blocked {
		t.Errorf("Classify(dd of=/dev/sda) = %s, want blocked", got)
	}
}

func TestClassify_FP_ProjectExecAndSafeTools(t *testing.T) {
	cases := []struct {
		cmd string
		cls RiskClass
	}{
		{"make", CodeExecution},
		{"make test", CodeExecution},
		{"make --version", Safe},
		{"gmake -h", Safe},
		{"pytest", CodeExecution},
		{"pytest -q", CodeExecution},
		{"pytest --version", Safe},
		{"base64 file", Safe},
		{"base64 -d file", Safe},
		{"crontab -l", Safe},
		{"crontab --list", Safe},
		{"crontab file", Persistence},
		{"curl --help", Safe},
		{"curl --version", Safe},
		{"wget --version", Safe},
		{"curl http://example.com", NetworkEgress},
	}
	for _, tc := range cases {
		if got := Classify(tc.cmd); got != tc.cls {
			t.Errorf("Classify(%q) = %s, want %s", tc.cmd, got, tc.cls)
		}
	}
	// Redirected base64 to a system path still escalates.
	if got := Classify("base64 file > /etc/x"); got != SystemWrite {
		t.Errorf("Classify(base64 > /etc/x) = %s, want system_write", got)
	}
}

func TestClassify_FP_AwkPlainPrint(t *testing.T) {
	if got := Classify("awk '{print $1}' file"); got != Safe {
		t.Errorf("Classify(awk print) = %s, want safe", got)
	}
	if got := Classify(`awk 'BEGIN{system("id")}'`); got != CodeExecution {
		t.Errorf("Classify(awk system) = %s, want code_execution", got)
	}
	if got := Classify("awk -f script.awk"); got != CodeExecution {
		t.Errorf("Classify(awk -f) = %s, want code_execution", got)
	}
}
