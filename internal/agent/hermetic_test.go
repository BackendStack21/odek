package agent

import (
	"fmt"
	"os"
	"testing"
)

// Library constructor tests must never load the operator's persisted memory.
// Individual tests can still provide an explicit memory directory or home.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "odek-library-tests-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "isolate library tests:", err)
		os.Exit(1)
	}
	if err := os.Setenv("HOME", dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("USERPROFILE", dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
