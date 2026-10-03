//go:build !unix

package session

import "os"

// fileInode is 0 on platforms without an inode concept; the cache stamp
// then relies on mtime+size alone.
func fileInode(os.FileInfo) uint64 { return 0 }
