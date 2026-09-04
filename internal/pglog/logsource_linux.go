//go:build linux

package pglog

import (
	"os"
	"syscall"
)

// fileInode identifies a file across renames so a refresh can tell "the log
// grew" from "the log was rotated and a new one started".
func fileInode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}
