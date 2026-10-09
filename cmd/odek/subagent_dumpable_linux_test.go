//go:build linux

package main

import (
	"testing"

	"golang.org/x/sys/unix"
)

// The sub-agent child makes itself non-dumpable so same-uid processes cannot
// reopen its descriptors through /proc/<pid>/fd.
func TestRED_SubagentProcessNonDumpable(t *testing.T) {
	t.Cleanup(func() { _ = unix.Prctl(unix.PR_SET_DUMPABLE, 1, 0, 0, 0) })
	if err := hardenSubagentProcess(); err != nil {
		t.Fatal(err)
	}
	got, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("PR_GET_DUMPABLE = %d after hardening, want 0", got)
	}
}
