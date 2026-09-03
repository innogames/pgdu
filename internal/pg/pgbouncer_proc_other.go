//go:build !linux

package pg

import (
	"os"
	"path/filepath"
)

// scanPgBouncerProcs has no /proc to read on non-Linux hosts.
func scanPgBouncerProcs() []pgbProc { return nil }

// statSocket reports whether path exists and is a unix socket.
func statSocket(path string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return fi.Mode()&os.ModeSocket != 0, nil
}

func pgbIniGlob() []string {
	m, _ := filepath.Glob("/etc/pgbouncer/*.ini")
	return m
}
