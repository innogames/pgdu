//go:build !linux

package procfs

import "os"

// ListByComm has no /proc to read on non-Linux hosts.
func ListByComm(string) []Process { return nil }

// Sample yields no samples without /proc; the caller shows its proc columns
// as unavailable.
func Sample([]int32) []PIDStats { return nil }

// Inode is unavailable here; rotation detection falls back to "the file
// shrank", which catches the common copytruncate case.
func Inode(os.FileInfo) uint64 { return 0 }
