package pgbouncer

import (
	"path/filepath"
	"sort"
	"strings"

	"pgdu/internal/procfs"
)

// defaultIni is what pgbouncer reads when started without a config path —
// which it never really is, but the argv parser needs a fallback.
const defaultIni = "/etc/pgbouncer/pgbouncer.ini"

// Discover lists every pgbouncer instance we can find, best-effort
// and deduped by Key(). Sources, in priority order (the first sighting owns the
// Reason; later ones only fill in blanks):
//
//  1. running processes from /proc, each parsed from the ini on its command line;
//  2. /etc/pgbouncer/*.ini for configured-but-stopped instances;
//  3. pgdu's own connection when viaPooler says it evidently goes through a
//     pooler (pg.Client judges that from the pool's socket addresses).
//
// Nothing here fails: an unreadable ini leaves IniErr set, a missing /proc
// yields nothing from that source, and so on.
func (c *Client) Discover(viaPooler bool) []Instance {
	var found []Instance

	for _, p := range procfs.ListByComm("pgbouncer") {
		ini := iniFromArgv(p.Argv)
		if !filepath.IsAbs(ini) && p.Cwd != "" {
			ini = filepath.Join(p.Cwd, ini)
		}
		inst := Instance{PID: p.PID, Reason: "/proc"}
		instanceFromIni(&inst, ini)
		found = append(found, inst)
	}

	for _, ini := range iniGlob() {
		inst := Instance{Reason: "/etc/pgbouncer"}
		instanceFromIni(&inst, ini)
		found = append(found, inst)
	}

	if viaPooler {
		found = append(found, Instance{
			Name:   c.cfg.Target(),
			Reason: "connection target",
			DSN:    c.cfg.BuildDSN("pgbouncer"),
		})
	}

	return dedupe(found)
}

// dedupe merges sightings of the same instance (same Key) and sorts
// the result by name. The first sighting wins for Reason and for every field
// it set; later sightings only fill zero fields — so /proc's PID lands on the
// ini-derived record and the first source to name an instance keeps its Reason.
func dedupe(found []Instance) []Instance {
	byKey := map[string]int{}
	var out []Instance
	for _, inst := range found {
		k := inst.Key()
		i, seen := byKey[k]
		if !seen {
			byKey[k] = len(out)
			out = append(out, inst)
			continue
		}
		cur := &out[i]
		if cur.PID == 0 {
			cur.PID = inst.PID
		}
		if cur.IniPath == "" {
			cur.IniPath = inst.IniPath
			cur.IniErr = inst.IniErr
		}
		if cur.Name == "" {
			cur.Name = inst.Name
		}
		if cur.Logfile == "" {
			cur.Logfile = inst.Logfile
		}
		if cur.SocketDir == "" && cur.DSN == "" {
			cur.SocketDir = inst.SocketDir
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// iniFromArgv extracts the config path from a pgbouncer command line:
// the first non-option argument, skipping the value-taking -u/--user. Missing
// → pgbouncer's default path.
func iniFromArgv(argv []string) string {
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "-u" || a == "--user":
			i++
		case strings.HasPrefix(a, "-"):
			// -d -R -q -v -V -h and --long forms carry no ini.
		default:
			return a
		}
	}
	return defaultIni
}

// iniGlob lists the packaged config directory for instances that are
// configured but not running (or whose /proc entries we could not read).
func iniGlob() []string {
	m, _ := filepath.Glob("/etc/pgbouncer/*.ini")
	return m
}
