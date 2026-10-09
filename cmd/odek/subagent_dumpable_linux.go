//go:build linux

package main

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// hardenSubagentProcess marks the sub-agent child non-dumpable. On Linux a
// process running as the same uid can otherwise open /proc/<child>/fd/N and
// reopen the child's stdout pipe, its result-frame descriptor or its key
// descriptor: a background command left behind by the child could read the
// authenticated result frame before the parent does and write a forged one
// in its place. A non-dumpable process's /proc/<pid>/fd, environ and mem are
// owned by root, so same-uid processes can no longer open them. The flag is
// reset on execve, so commands the child runs are unaffected.
//
// The trade-off: no core dumps of sub-agents, and same-uid debuggers and
// tracers (gdb, strace -p) cannot attach to them.
func hardenSubagentProcess() error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(PR_SET_DUMPABLE, 0): %w", err)
	}
	return nil
}
