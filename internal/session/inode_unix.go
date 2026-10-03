//go:build unix

package session

import (
	"os"
	"syscall"
)

// fileInode returns the file's inode number, used as part of the index
// cache stamp. The atomic-rename write pattern always mints a fresh inode,
// so an external rewrite of index.json is detected even when mtime
// granularity and file size coincide.
func fileInode(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}
