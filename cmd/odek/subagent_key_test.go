package main

import (
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestWriteKeyToUnlinkedFile_RoundtripsThroughFD writes a key with
// writeKeyToUnlinkedFile, reads it back from the *os.File, and checks
// the unlink behaviour.
func TestWriteKeyToUnlinkedFile_RoundtripsThroughFD(t *testing.T) {
	const want = "sk-test-1234567890"
	f, err := writeKeyToUnlinkedFile(want)
	if err != nil {
		t.Fatalf("writeKeyToUnlinkedFile: %v", err)
	}
	defer f.Close()

	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != want {
		t.Errorf("read = %q, want %q", string(got), want)
	}

	// On POSIX, the file must already be unlinked — the inode lives on
	// only because we hold the FD. Verify by stat'ing the path.
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(f.Name()); !os.IsNotExist(err) {
			t.Errorf("expected file to be unlinked on disk, stat err = %v", err)
		}
	}
}

// TestReadKeyFromInheritedFD_NoEnvVarReturnsEmpty verifies that the
// read helper is a no-op when the parent did not opt into the handoff
// (no ODEK_API_KEY_FD env var). This is the property that lets us call
// it unconditionally at sub-agent startup without breaking processes
// that have FD 3 wired to something else (test harness, debugger).
func TestReadKeyFromInheritedFD_NoEnvVarReturnsEmpty(t *testing.T) {
	t.Setenv(keyFDEnvVar, "")
	if got := readKeyFromInheritedFD(); got != "" {
		t.Errorf("expected \"\" when env var unset, got %q", got)
	}
}

// TestSubagentChild_ReceivesKeyViaFDNotEnv launches a tiny child
// process via exec, passes a key on FD 3, and verifies (a) the child
// can read the key and (b) the key does NOT appear in the child's
// environment. Uses a small in-test go program built on the fly so we
// do not depend on the full odek binary.
func TestSubagentChild_ReceivesKeyViaFDNotEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ExtraFiles semantics differ on Windows")
	}
	const want = "sk-handoff-key-9876"

	// The child reads up to 4096 bytes from FD 3 and prints them
	// followed by a newline and the literal env string ODEK_API_KEY=…
	// (or 'NO_ENV_KEY' if absent). We use /bin/sh so we don't need a
	// pre-built helper binary.
	script := `
read_key=$(cat <&${ODEK_API_KEY_FD} 2>/dev/null || true)
echo "key=${read_key}"
if env | grep -E '^(ODEK|DEEPSEEK|OPENAI)_API_KEY=' >/dev/null 2>&1; then
  echo "env_leak=true"
else
  echo "env_leak=false"
fi
`
	keyFile, err := writeKeyToUnlinkedFile(want)
	if err != nil {
		t.Fatalf("writeKeyToUnlinkedFile: %v", err)
	}
	defer keyFile.Close()

	cmd := exec.Command("/bin/sh", "-c", script)
	// Strip any inherited key env vars so the test cannot pass by
	// accident on a developer machine that has them set.
	cmd.Env = []string{"PATH=/usr/bin:/bin", keyFDEnvVar + "=3"}
	cmd.ExtraFiles = []*os.File{keyFile}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run child: %v\nstderr/out: %s", err, out)
	}

	s := string(out)
	if !strings.Contains(s, "key="+want) {
		t.Errorf("child did not receive key on FD 3.\noutput: %s", s)
	}
	if !strings.Contains(s, "env_leak=false") {
		t.Errorf("API key leaked into child environment.\noutput: %s", s)
	}
}
