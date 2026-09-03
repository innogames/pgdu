//go:build linux

package pg

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// scanPgBouncerProcs lists running pgbouncer processes from /proc. Anything we
// cannot read (other users' cwd links, vanished pids) is skipped silently —
// discovery is best-effort and a partial list beats an error.
func scanPgBouncerProcs() []pgbProc {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []pgbProc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		base := "/proc/" + e.Name()
		comm, err := os.ReadFile(base + "/comm")
		if err != nil || strings.TrimSpace(string(comm)) != "pgbouncer" {
			continue
		}
		raw, err := os.ReadFile(base + "/cmdline")
		if err != nil || len(raw) == 0 {
			continue
		}
		argv := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		// A -R takeover child runs with the same argv and ini; discovery dedupes
		// it against the parent by Key, so no ppid bookkeeping is needed here.
		cwd, _ := os.Readlink(base + "/cwd")
		out = append(out, pgbProc{PID: pid, Argv: argv, Cwd: cwd})
	}
	return out
}

// statSocket reports whether path exists and is a unix socket.
func statSocket(path string) (bool, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return false, err
	}
	return st.Mode&syscall.S_IFMT == syscall.S_IFSOCK, nil
}

// pgbIniGlob lists the packaged config directory for instances that are
// configured but not running (or whose /proc entries we could not read).
func pgbIniGlob() []string {
	m, _ := filepath.Glob("/etc/pgbouncer/*.ini")
	return m
}
