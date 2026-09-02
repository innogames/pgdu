package pg

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotIncremental is returned by ReadFrom on sources that cannot seek
// (gzip); the caller falls back to a full ReadTail.
var ErrNotIncremental = errors.New("log source cannot be read incrementally")

// maxWholeFile caps "whole file" reads so a runaway log cannot exhaust memory.
const maxWholeFile = 2 << 30

// serverChunk is the pg_read_binary_file transfer size. Large enough to keep
// the round-trip count low, small enough to stay well under pgx's per-message
// comfort zone.
const serverChunk = 8 << 20

// ── local plain file ─────────────────────────────────────────────────────────

type localFileSource struct {
	info LogSourceInfo
}

// OpenLocalLog opens a log on this host, picking the gzip reader by suffix.
func OpenLocalLog(path string) LogSource {
	info := LogSourceInfo{Kind: "local", Path: path, Size: -1, Rotated: isRotatedName(path)}
	if fi, err := os.Stat(path); err == nil {
		info.Size = fi.Size()
		info.ModTime = fi.ModTime()
	}
	if strings.HasSuffix(path, ".gz") {
		info.Kind = "gz"
		return &gzFileSource{info: info}
	}
	return &localFileSource{info: info}
}

func (s *localFileSource) Info() LogSourceInfo { return s.info }

func (s *localFileSource) ReadTail(_ context.Context, n int64) ([]byte, LogWindow, error) {
	f, err := os.Open(s.info.Path)
	if err != nil {
		return nil, LogWindow{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, LogWindow{}, err
	}
	size := fi.Size()
	s.info.Size, s.info.ModTime = size, fi.ModTime()
	win := LogWindow{Requested: n, FileSize: size}
	start := int64(0)
	if n > 0 && size > n {
		start = size - n
		win.Truncated = true
	} else if n <= 0 && size > maxWholeFile {
		start = size - maxWholeFile
		win.Truncated = true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, win, err
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, win, err
	}
	buf, dropped := alignHead(buf, start > 0)
	win.Start = start + int64(dropped)
	win.DroppedHead = int64(dropped)
	win.Bytes = int64(len(buf))
	return buf, win, nil
}

func (s *localFileSource) ReadFrom(_ context.Context, off int64) ([]byte, error) {
	f, err := os.Open(s.info.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

func (s *localFileSource) Cursor(_ context.Context) *LogCursor {
	fi, err := os.Stat(s.info.Path)
	if err != nil {
		return nil
	}
	return &LogCursor{Inode: fileInode(fi), Size: fi.Size()}
}

// ── local gzip file ──────────────────────────────────────────────────────────

type gzFileSource struct {
	info LogSourceInfo
}

func (s *gzFileSource) Info() LogSourceInfo { return s.info }

func (s *gzFileSource) ReadTail(ctx context.Context, n int64) ([]byte, LogWindow, error) {
	f, err := os.Open(s.info.Path)
	if err != nil {
		return nil, LogWindow{}, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, LogWindow{}, fmt.Errorf("gunzip %s: %w", s.info.Path, err)
	}
	defer zr.Close()
	return tailOfStream(ctx, zr, n)
}

func (s *gzFileSource) ReadFrom(context.Context, int64) ([]byte, error) { return nil, ErrNotIncremental }
func (s *gzFileSource) Cursor(context.Context) *LogCursor              { return nil }

// tailOfStream drains r keeping only its last n bytes (n <= 0: everything up
// to maxWholeFile), using two buffers of n so peak memory stays under 2n.
func tailOfStream(ctx context.Context, r io.Reader, n int64) ([]byte, LogWindow, error) {
	limit := n
	if limit <= 0 {
		limit = maxWholeFile
	}
	tk := &tailKeeper{n: limit}
	rd := &ctxReader{ctx: ctx, r: r}
	if _, err := io.Copy(tk, rd); err != nil {
		return nil, LogWindow{}, err
	}
	buf := tk.Bytes()
	win := LogWindow{Requested: n, FileSize: tk.total, Truncated: tk.total > int64(len(buf))}
	buf, dropped := alignHead(buf, win.Truncated)
	win.Start = tk.total - int64(len(buf))
	win.DroppedHead = int64(dropped)
	win.Bytes = int64(len(buf))
	return buf, win, nil
}

// tailKeeper is an io.Writer that remembers the last n bytes written. It
// swaps two n-sized buffers instead of shifting, so each byte is copied at
// most twice.
type tailKeeper struct {
	n     int64
	prev  []byte
	cur   []byte
	total int64
}

func (t *tailKeeper) Write(p []byte) (int, error) {
	written := len(p)
	t.total += int64(written)
	for len(p) > 0 {
		room := int(t.n) - len(t.cur)
		if room == 0 {
			t.prev, t.cur = t.cur, t.prev[:0]
			room = int(t.n)
		}
		k := min(room, len(p))
		t.cur = append(t.cur, p[:k]...)
		p = p[k:]
	}
	return written, nil
}

// Bytes returns the retained tail as one contiguous slice.
func (t *tailKeeper) Bytes() []byte {
	if len(t.prev) == 0 {
		return t.cur
	}
	need := int(t.n) - len(t.cur)
	if need > len(t.prev) {
		need = len(t.prev)
	}
	out := make([]byte, 0, need+len(t.cur))
	out = append(out, t.prev[len(t.prev)-need:]...)
	out = append(out, t.cur...)
	return out
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// alignHead drops the partial first line of a window that did not start at
// offset 0, returning the aligned buffer and how many bytes went.
func alignHead(buf []byte, cut bool) ([]byte, int) {
	if !cut {
		return buf, 0
	}
	i := bytes.IndexByte(buf, '\n')
	if i < 0 {
		return buf[:0], len(buf)
	}
	return buf[i+1:], i + 1
}

// skipContinuation drops leading lines that are not primary lines under m —
// continuation text whose primary entry lies before the window start.
func skipContinuation(buf []byte, m *prefixMatcher) ([]byte, int) {
	dropped := 0
	for len(buf) > 0 {
		i := bytes.IndexByte(buf, '\n')
		line := buf
		if i >= 0 {
			line = buf[:i]
		}
		if f, ok := m.Match(line); ok {
			if _, isAttach := attachmentField(f.tag); !isAttach {
				break
			}
		}
		if i < 0 {
			dropped += len(buf)
			return buf[:0], dropped
		}
		buf = buf[i+1:]
		dropped += i + 1
	}
	return buf, dropped
}

// ── server-side file (pg_read_binary_file) ───────────────────────────────────

type serverFileSource struct {
	pool *pgxpool.Pool
	info LogSourceInfo
}

func (s *serverFileSource) Info() LogSourceInfo { return s.info }

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

func (s *serverFileSource) ReadTail(ctx context.Context, n int64) ([]byte, LogWindow, error) {
	size, err := s.statSize(ctx)
	if err != nil {
		return nil, LogWindow{}, err
	}
	if strings.HasSuffix(s.info.Path, ".gz") {
		raw, err := s.readRange(ctx, 0, size)
		if err != nil {
			return nil, LogWindow{}, err
		}
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, LogWindow{}, fmt.Errorf("gunzip %s: %w", s.info.Path, err)
		}
		return tailOfStream(ctx, zr, n)
	}
	win := LogWindow{Requested: n, FileSize: size}
	start := int64(0)
	if n > 0 && size > n {
		start = size - n
		win.Truncated = true
	}
	buf, err := s.readRange(ctx, start, size)
	if err != nil {
		return nil, win, err
	}
	buf, dropped := alignHead(buf, start > 0)
	win.Start = start + int64(dropped)
	win.DroppedHead = int64(dropped)
	win.Bytes = int64(len(buf))
	return buf, win, nil
}

func (s *serverFileSource) ReadFrom(ctx context.Context, off int64) ([]byte, error) {
	if strings.HasSuffix(s.info.Path, ".gz") {
		return nil, ErrNotIncremental
	}
	size, err := s.statSize(ctx)
	if err != nil {
		return nil, err
	}
	return s.readRange(ctx, off, size)
}

func (s *serverFileSource) Cursor(ctx context.Context) *LogCursor {
	if strings.HasSuffix(s.info.Path, ".gz") {
		return nil
	}
	size, err := s.statSize(ctx)
	if err != nil {
		return nil
	}
	return &LogCursor{Size: size}
}

// ── discovery ────────────────────────────────────────────────────────────────

// localLogGlob is where Debian/Ubuntu's postgresql-common redirects stderr
// when logging_collector is off — the case pg_current_logfile() cannot see.
const localLogGlob = "/var/log/postgresql/postgresql-*.log*"

var rotatedRe = regexp.MustCompile(`\.log\.(\d+)(\.gz)?$`)

func isRotatedName(path string) bool { return rotatedRe.MatchString(path) }

// rotationIndex orders rotated files: the live log is 0, ".log.1" is 1, ….
func rotationIndex(path string) int {
	m := rotatedRe.FindStringSubmatch(path)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

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
		src := OpenLocalLog(path)
		info := src.Info()
		info.Current = current
		add(LogCandidate{Info: info, Reason: reason, Open: func() LogSource { return src }})
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
			if !isRotatedName(p) {
				live = append(live, p)
			}
		}
		for _, p := range matches {
			current := p == currentPath || (currentPath == "" && !isRotatedName(p) && len(live) == 1)
			local(p, "/var/log/postgresql", current)
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
	info := LogSourceInfo{Kind: "server", Path: path, Size: size, ModTime: mod, Rotated: isRotatedName(path), Current: current}
	return LogCandidate{Info: info, Reason: reason, Open: func() LogSource {
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
		ra, rb := rotationIndex(a.Info.Path), rotationIndex(b.Info.Path)
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
