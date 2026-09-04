package pg

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pgdu/internal/pglog"
)

// LogSettings returns the logging GUCs (see logSettingsKeys), best-effort.
func (c *Client) LogSettings(ctx context.Context) map[string]string {
	pool, err := c.PoolFor(ctx, c.DefaultDB())
	if err != nil {
		return map[string]string{}
	}
	return settingsMap(ctx, pool, sqlMaintSettings, logSettingsKeys)
}

// LogCandidate is one file the picker offers.
type LogCandidate struct {
	Info   pglog.SourceInfo
	Reason string // how it was found: "--log-file", "pg_current_logfile", "/var/log/postgresql", "pg_ls_logdir"
	Open   func() pglog.Source
}

// serverChunk is the pg_read_binary_file transfer size. Large enough to keep
// the round-trip count low, small enough to stay well under pgx's per-message
// comfort zone.
const serverChunk = 8 << 20

// ── server-side file (pg_read_binary_file) ───────────────────────────────────

type serverFileSource struct {
	pool *pgxpool.Pool
	info pglog.SourceInfo
}

func (s *serverFileSource) Info() pglog.SourceInfo { return s.info }

func (s *serverFileSource) statSize(ctx context.Context) (int64, error) {
	var size *int64
	var mod *time.Time
	if err := s.pool.QueryRow(ctx, sqlLogStatFile, s.info.Path).Scan(&size, &mod); err != nil {
		return -1, fmt.Errorf("pg_stat_file(%s): %w", s.info.Path, err)
	}
	if size == nil {
		return -1, fmt.Errorf("%s: no such file on the server", s.info.Path)
	}
	if mod != nil {
		s.info.ModTime = *mod
	}
	s.info.Size = *size
	return *size, nil
}

func (s *serverFileSource) readRange(ctx context.Context, off, end int64) ([]byte, error) {
	out := make([]byte, 0, end-off)
	for off < end {
		n := min(int64(serverChunk), end-off)
		var chunk []byte
		if err := s.pool.QueryRow(ctx, sqlLogReadFile, s.info.Path, off, n).Scan(&chunk); err != nil {
			return nil, fmt.Errorf("pg_read_binary_file(%s): %w", s.info.Path, err)
		}
		if len(chunk) == 0 {
			break
		}
		out = append(out, chunk...)
		off += int64(len(chunk))
	}
	return out, nil
}

func (s *serverFileSource) ReadTail(ctx context.Context, n int64) ([]byte, pglog.Window, error) {
	size, err := s.statSize(ctx)
	if err != nil {
		return nil, pglog.Window{}, err
	}
	if strings.HasSuffix(s.info.Path, ".gz") {
		raw, err := s.readRange(ctx, 0, size)
		if err != nil {
			return nil, pglog.Window{}, err
		}
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, pglog.Window{}, fmt.Errorf("gunzip %s: %w", s.info.Path, err)
		}
		return pglog.TailOfStream(ctx, zr, n)
	}
	win := pglog.Window{Requested: n, FileSize: size}
	start := int64(0)
	if n > 0 && size > n {
		start = size - n
		win.Truncated = true
	}
	buf, err := s.readRange(ctx, start, size)
	if err != nil {
		return nil, win, err
	}
	buf, dropped := pglog.AlignHead(buf, start > 0)
	win.Start = start + int64(dropped)
	win.DroppedHead = int64(dropped)
	win.Bytes = int64(len(buf))
	return buf, win, nil
}

func (s *serverFileSource) ReadFrom(ctx context.Context, off int64) ([]byte, error) {
	if strings.HasSuffix(s.info.Path, ".gz") {
		return nil, pglog.ErrNotIncremental
	}
	size, err := s.statSize(ctx)
	if err != nil {
		return nil, err
	}
	return s.readRange(ctx, off, size)
}

func (s *serverFileSource) Cursor(ctx context.Context) *pglog.Cursor {
	if strings.HasSuffix(s.info.Path, ".gz") {
		return nil
	}
	size, err := s.statSize(ctx)
	if err != nil {
		return nil
	}
	return &pglog.Cursor{Size: size}
}

// ── discovery ────────────────────────────────────────────────────────────────

// localLogGlob is where Debian/Ubuntu's postgresql-common redirects stderr
// when logging_collector is off — the case pg_current_logfile() cannot see.
const localLogGlob = "/var/log/postgresql/postgresql-*.log*"

// pgbLogGlobs are where pgbouncer's logfile usually lands (Debian puts it next
// to the server logs; the upstream sample uses its own directory). The
// pgbouncer tool opens an instance's configured logfile directly, so this only
// matters for the picker.
var pgbLogGlobs = []string{"/var/log/postgresql/pgbouncer*.log*", "/var/log/pgbouncer/*.log*"}

// DiscoverLogs lists every log file the analyzer can offer, best-effort:
// the explicit --log-file, pg_current_logfile() (local when the path exists
// here, else server-side), the Debian /var/log/postgresql glob, and — when
// privileges allow — the server's log directory. Nothing here fails; a source
// that cannot be listed is simply absent.
func (c *Client) DiscoverLogs(ctx context.Context, explicit string) []LogCandidate {
	var out []LogCandidate
	seen := map[string]bool{}
	add := func(cand LogCandidate) {
		k := cand.Info.Kind + "|" + cand.Info.Path
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, cand)
	}
	local := func(path, reason string, current bool) {
		src := pglog.OpenLocal(path)
		info := src.Info()
		info.Current = current
		add(LogCandidate{Info: info, Reason: reason, Open: func() pglog.Source { return src }})
	}

	if explicit != "" {
		local(explicit, "--log-file", true)
	}

	pool, perr := c.PoolFor(ctx, c.DefaultDB())
	settings := map[string]string{}
	if perr == nil {
		settings = settingsMap(ctx, pool, sqlMaintSettings, logSettingsKeys)
	}
	dataDir := settings["data_directory"]

	currentPath := ""
	if perr == nil {
		var cur string
		if err := pool.QueryRow(ctx, sqlLogCurrentLogfile).Scan(&cur); err == nil && cur != "" {
			if !filepath.IsAbs(cur) && dataDir != "" {
				cur = filepath.Join(dataDir, cur)
			}
			currentPath = cur
			if _, err := os.Stat(cur); err == nil {
				local(cur, "pg_current_logfile", true)
			} else {
				add(c.serverCandidate(pool, cur, -1, time.Time{}, "pg_current_logfile", true))
			}
		}
	}

	if matches, _ := filepath.Glob(localLogGlob); len(matches) > 0 {
		var live []string
		for _, p := range matches {
			if !pglog.IsRotatedName(p) {
				live = append(live, p)
			}
		}
		for _, p := range matches {
			current := p == currentPath || (currentPath == "" && !pglog.IsRotatedName(p) && len(live) == 1)
			local(p, "/var/log/postgresql", current)
		}
	}
	for _, g := range pgbLogGlobs {
		matches, _ := filepath.Glob(g)
		for _, p := range matches {
			local(p, "pgbouncer", false)
		}
	}

	if perr == nil && settings["logging_collector"] == "on" {
		logDir := settings["log_directory"]
		if !filepath.IsAbs(logDir) && dataDir != "" {
			logDir = filepath.Join(dataDir, logDir)
		}
		for _, f := range collectBestEffort(ctx, pool, sqlLogLsLogdir, nil, scanServerFile) {
			path := f.name
			if logDir != "" {
				path = filepath.Join(logDir, f.name)
			}
			if _, err := os.Stat(path); err == nil {
				local(path, "pg_ls_logdir", path == currentPath)
				continue
			}
			add(c.serverCandidate(pool, path, f.size, f.mod, "pg_ls_logdir", path == currentPath))
		}
	}

	if perr == nil && len(out) == 0 {
		var super bool
		if err := pool.QueryRow(ctx, sqlLogIsSuper).Scan(&super); err == nil && super {
			const dir = "/var/log/postgresql"
			for _, f := range collectBestEffort(ctx, pool, sqlLogLsDir, []any{dir}, scanServerFile) {
				add(c.serverCandidate(pool, filepath.Join(dir, f.name), f.size, f.mod, "pg_ls_dir", false))
			}
		}
	}

	sortCandidates(out)
	return out
}

type serverFile struct {
	name string
	size int64
	mod  time.Time
}

func scanServerFile(rows pgx.Rows) (serverFile, bool) {
	var f serverFile
	var size *int64
	var mod *time.Time
	if rows.Scan(&f.name, &size, &mod) != nil {
		return f, false
	}
	if size != nil {
		f.size = *size
	}
	if mod != nil {
		f.mod = *mod
	}
	return f, true
}

func (c *Client) serverCandidate(pool *pgxpool.Pool, path string, size int64, mod time.Time, reason string, current bool) LogCandidate {
	info := pglog.SourceInfo{Kind: "server", Path: path, Size: size, Lines: -1, ModTime: mod, Rotated: pglog.IsRotatedName(path), Current: current}
	return LogCandidate{Info: info, Reason: reason, Open: func() pglog.Source {
		return &serverFileSource{pool: pool, info: info}
	}}
}

// sortCandidates puts the live log first, then rotated files newest first;
// explicit/local files sort before server-side copies of the same name.
func sortCandidates(c []LogCandidate) {
	kindRank := func(k string) int {
		switch k {
		case "local":
			return 0
		case "gz":
			return 1
		}
		return 2
	}
	sort.SliceStable(c, func(i, j int) bool {
		a, b := c[i], c[j]
		if a.Info.Current != b.Info.Current {
			return a.Info.Current
		}
		ra, rb := pglog.RotationIndex(a.Info.Path), pglog.RotationIndex(b.Info.Path)
		if ra != rb {
			return ra < rb
		}
		if !a.Info.ModTime.Equal(b.Info.ModTime) {
			return a.Info.ModTime.After(b.Info.ModTime)
		}
		if ka, kb := kindRank(a.Info.Kind), kindRank(b.Info.Kind); ka != kb {
			return ka < kb
		}
		return a.Info.Path < b.Info.Path
	})
}
