package pg

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// pgbIni is the [pgbouncer] section of a pgbouncer.ini, keys lower-cased.
// Only that section matters for discovery; [databases]/[users]/[peers] carry
// connection strings we never need (and must not mis-parse as key = value).
type pgbIni map[string]string

// pgbIniIncludeDepth bounds %include recursion so a self-including file cannot
// spin discovery forever.
const pgbIniIncludeDepth = 8

// parsePgBouncerIni reads path and every %include it names. A later value for
// the same key wins, mirroring pgbouncer's own last-one-wins semantics.
func parsePgBouncerIni(path string) (pgbIni, error) {
	ini := pgbIni{}
	if err := ini.load(path, 0); err != nil {
		return nil, err
	}
	return ini, nil
}

func (ini pgbIni) load(path string, depth int) error {
	if depth > pgbIniIncludeDepth {
		return fmt.Errorf("%%include nesting too deep at %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	// pgbouncer applies an %include before the following lines, so a key set
	// after the include overrides the included value — sequential processing
	// with a plain map gives exactly that.
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == ';' || line[0] == '#' {
			continue
		}
		if strings.HasPrefix(line, "%include") {
			inc := strings.TrimSpace(strings.TrimPrefix(line, "%include"))
			inc = unquoteIni(inc)
			if inc == "" {
				continue
			}
			if !filepath.IsAbs(inc) {
				inc = filepath.Join(filepath.Dir(path), inc)
			}
			if err := ini.load(inc, depth+1); err != nil {
				return err
			}
			continue
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				continue
			}
			section = strings.ToLower(strings.TrimSpace(line[1:end]))
			continue
		}
		if section != "pgbouncer" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		ini[strings.ToLower(strings.TrimSpace(k))] = unquoteIni(strings.TrimSpace(v))
	}
	return sc.Err()
}

// unquoteIni strips one pair of matching single or double quotes.
func unquoteIni(v string) string {
	if len(v) >= 2 {
		if q := v[0]; (q == '\'' || q == '"') && v[len(v)-1] == q {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// list splits a comma-separated user list, dropping blanks.
func (ini pgbIni) list(key string) []string {
	var out []string
	for _, s := range strings.Split(ini[key], ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (ini pgbIni) intOr(key string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(ini[key])); err == nil {
		return n
	}
	return def
}

// instanceFromIni fills the ini-derived fields of inst. Unreadable/unparsable
// files leave inst untouched apart from IniErr, so an instance seen in /proc
// still lists with its PID and a hint about the missing config.
func instanceFromIni(inst *PgBouncerInstance, path string) {
	inst.IniPath = path
	if inst.Name == "" {
		inst.Name = strings.TrimSuffix(filepath.Base(path), ".ini")
	}
	ini, err := parsePgBouncerIni(path)
	if err != nil {
		inst.IniErr = err
		return
	}
	inst.SocketDir = ini["unix_socket_dir"]
	inst.ListenAddr = ini["listen_addr"]
	inst.ListenPort = ini.intOr("listen_port", 0)
	inst.Logfile = ini["logfile"]
	inst.Pidfile = ini["pidfile"]
	inst.PoolMode = ini["pool_mode"]
	inst.AuthType = ini["auth_type"]
	inst.AuthFile = ini["auth_file"]
	inst.AdminUsers = ini.list("admin_users")
	inst.StatsUsers = ini.list("stats_users")
	if inst.SocketDir == "" && inst.ListenAddr == "" {
		// pgbouncer's compiled-in default socket dir; only meaningful when the
		// ini set neither, which practically never happens on Debian but keeps
		// Target() from falling through to TCP for a socket-only setup.
		inst.SocketDir = "/tmp"
	}
}
