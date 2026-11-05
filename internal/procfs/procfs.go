// Package procfs is pgdu's one place for Linux-only host reads: process
// discovery and per-PID resource samples from /proc, plus the inode a stat(2)
// reports. Every function degrades to "nothing known" on other platforms so
// callers never need their own build tags.
package procfs

import "time"

// Process is one running process as read from /proc/<pid>.
type Process struct {
	PID  int
	Argv []string
	Cwd  string // "" when the cwd link was unreadable (another user's process)
}

// PIDStats is one resource sample for a single PID. ReadBytes and WriteBytes
// are -1 when /proc/<pid>/io was unreadable (it needs the same UID or
// CAP_SYS_PTRACE).
type PIDStats struct {
	PID        int32
	RSSBytes   int64  // VmRSS in bytes (from /proc/<pid>/status)
	CPUTicks   uint64 // utime+stime in USER_HZ ticks (from /proc/<pid>/stat)
	ReadBytes  int64  // cumulative storage read_bytes (from /proc/<pid>/io); -1 = unreadable
	WriteBytes int64  // cumulative storage write_bytes; -1 = unreadable
	At         time.Time
}

// FS is a filesystem's capacity as statfs(2) reports it, in bytes. Avail is
// what an unprivileged process may still write (f_bavail), which is what a
// CREATE INDEX actually has to work with — it excludes the root-reserved
// blocks Free (f_bfree) counts.
type FS struct {
	Total int64
	Free  int64
	Avail int64
}

// Used is the part of the filesystem already occupied, including the
// root-reserved blocks (which nobody but root can hand back).
func (f FS) Used() int64 { return max(f.Total-f.Free, 0) }
