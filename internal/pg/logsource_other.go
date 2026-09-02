//go:build !linux

package pg

import "os"

// fileInode is unavailable here; rotation detection falls back to "the file
// shrank", which catches the common copytruncate case.
func fileInode(os.FileInfo) uint64 { return 0 }
