package danger

import (
	"os"
	"path/filepath"
	"testing"
)

// `bash $PWD/evil.sh` must hit the unread-script gate exactly like
// `bash ./evil.sh`: the classifier runs in the same environment the shell
// would resolve $VAR from. expandShellTokenPath only handled ~/$HOME, so
// $VAR script paths never resolved, the stat failed, and the command
// classified as plain CodeExecution — trust-shortcut eligible — instead of
// UnreadExec (never trust-shortcuttable).
func TestUnreadScriptTargets_ExpandsEnvVarPaths(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "evil.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PWD", dir)

	targets := UnreadScriptTargets("bash $PWD/evil.sh")
	if len(targets) != 1 {
		t.Fatalf("UnreadScriptTargets(\"bash $PWD/evil.sh\") = %v, want the resolved script path", targets)
	}
	if targets[0] != script {
		t.Errorf("target = %q, want %q", targets[0], script)
	}

	// ${VAR} form too.
	targets = UnreadScriptTargets("bash ${PWD}/evil.sh")
	if len(targets) != 1 || targets[0] != script {
		t.Errorf("${PWD} form: targets = %v, want [%q]", targets, script)
	}
}
