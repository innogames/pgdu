package pg

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// snapshotExt is the on-disk suffix for snapshots. Files are gzip-compressed
// JSON: the captured Stats slice is highly repetitive (the same query-text
// shapes and column names recur on every row), so gzip typically shrinks a
// snapshot to a fraction of its JSON size. The double extension keeps the
// inner format visible. Pre-compression plain ".json" files are ignored.
const snapshotExt = ".json.gz"

// snapshotVersion is the on-disk schema version. Bump it when the Snapshot
// layout changes incompatibly; LoadSnapshot/ListSnapshots tolerate older files
// by ignoring unknown fields, and skip files with a newer version they can't read.
const snapshotVersion = 1

// Snapshot is a persisted, raw (cumulative-since-reset) pg_stat_statements
// capture plus the metadata needed to diff it safely later. Stats holds the same
// rows StatementSnapshot returns, so loading one as a baseline and diffing live
// counters against it reproduces the in-memory window across pgdu restarts. All
// fields are exported, so encoding/json serializes them without struct tags.
type Snapshot struct {
	Version       int
	Target        string // connection target (host:port / socket) — server-identity guard
	Database      string
	CapturedAt    time.Time
	StatsReset    time.Time // pg_stat_statements_info.stats_reset at capture (zero = unknown)
	TrackPlanning bool
	// QueryCount is len(Stats), persisted *before* Stats so ListSnapshots can read
	// it from the header and stop without decompressing the (large) Stats array.
	QueryCount int
	Stats      []QueryStat
}

// SnapshotMeta is the lightweight listing form of a Snapshot: everything the
// browser needs to render a row, without the (potentially large) Stats slice.
// ListSnapshots decodes files into this, so listing a directory of snapshots
// doesn't hold every captured query in memory at once.
type SnapshotMeta struct {
	Path          string
	Version       int
	Target        string
	Database      string
	CapturedAt    time.Time
	StatsReset    time.Time
	TrackPlanning bool
	QueryCount    int
}

// BaselineMap keys the snapshot's rows by queryid, matching the in-memory
// baseline shape the diff functions consume.
func (s *Snapshot) BaselineMap() map[int64]QueryStat {
	m := make(map[int64]QueryStat, len(s.Stats))
	for _, q := range s.Stats {
		m[q.QueryID] = q
	}
	return m
}

// CaptureSnapshot reads the current pg_stat_statements counters for db and wraps
// them with the metadata needed for a later diff (capture time, stats_reset,
// track_planning, connection target). The stats_reset read is best-effort: a
// failure leaves it zero rather than failing the whole capture.
func (c *Client) CaptureSnapshot(ctx context.Context, db string) (*Snapshot, error) {
	stats, err := c.StatementSnapshot(ctx, db)
	if err != nil {
		return nil, err
	}
	if db == "" {
		db = c.DefaultDB()
	}
	reset, _ := c.StatementsInfo(ctx, db)
	tp, _ := c.TrackPlanning(ctx, db)
	return &Snapshot{
		Version:       snapshotVersion,
		Target:        c.Target(),
		Database:      db,
		CapturedAt:    time.Now(),
		StatsReset:    reset,
		TrackPlanning: tp,
		Stats:         stats,
	}, nil
}

// SaveSnapshot writes s to dir as gzip-compressed pretty JSON and returns the
// file path. The filename is <sanitized-db>-<UTC timestamp>.json.gz so
// snapshots sort and read chronologically. The directory is created if missing.
//
// The directory and file are made world readable/writable so snapshots are
// shared across users on the same host: any user can list, load and delete any
// other user's snapshots. The directory is created mode 0o777 *without* the
// sticky bit (which a shared /tmp normally carries) so non-owners can unlink
// files in it; the explicit Chmod defeats a restrictive umask. Both Chmods are
// best-effort — they only succeed for whoever owns the path, but the first
// writer sets the permissions correctly for everyone that follows.
func SaveSnapshot(dir string, s *Snapshot) (string, error) {
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", fmt.Errorf("create snapshot dir %q: %w", dir, err)
	}
	_ = os.Chmod(dir, 0o777)
	name := fmt.Sprintf("%s-%s%s", sanitizeDB(s.Database), s.CapturedAt.UTC().Format("20060102T150405Z"), snapshotExt)
	path := filepath.Join(dir, name)
	// Stamp the row count so ListSnapshots can read it from the header (it is
	// serialized before Stats) without decompressing the array. SaveSnapshot is
	// the only writer, so this keeps the on-disk count authoritative regardless
	// of how the caller built the Snapshot.
	s.QueryCount = len(s.Stats)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode snapshot: %w", err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		return "", fmt.Errorf("compress snapshot: %w", err)
	}
	if err := gz.Close(); err != nil {
		return "", fmt.Errorf("compress snapshot: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o666); err != nil {
		return "", fmt.Errorf("write snapshot %q: %w", path, err)
	}
	_ = os.Chmod(path, 0o666)
	return path, nil
}

// readSnapshotFile reads a gzip-compressed snapshot file and returns the
// decompressed JSON bytes.
func readSnapshotFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer func() { _ = gz.Close() }()
	return io.ReadAll(gz)
}

// metaCache memoizes parsed SnapshotMeta keyed by path. Snapshot files are
// immutable once written (SaveSnapshot uses a unique timestamped name and never
// rewrites), so a (modTime,size) match means the cached metadata is still valid.
// This makes re-opening the browser or re-listing after a delete ~free: only
// new/changed files are read from disk.
var (
	metaCacheMu sync.Mutex
	metaCache   = map[string]cachedMeta{}
)

type cachedMeta struct {
	modTime time.Time
	size    int64
	meta    SnapshotMeta
}

// ListSnapshots returns the metadata of every readable *.json.gz snapshot in
// dir, newest capture first. A missing directory is not an error (no snapshots
// yet); unreadable or undecodable files are skipped so one bad file doesn't hide
// the rest. Pre-compression plain *.json files are ignored.
//
// Metadata is read by streaming only the leading header fields and stopping at
// the Stats array, so listing never decompresses the bulk of a file. Results are
// cached per path (validated by mod time + size), so repeat listings cost a stat
// per file rather than a full read.
func ListSnapshots(dir string) ([]SnapshotMeta, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read snapshot dir %q: %w", dir, err)
	}

	metaCacheMu.Lock()
	defer metaCacheMu.Unlock()

	var out []SnapshotMeta
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), snapshotExt) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		seen[path] = struct{}{}
		if c, ok := metaCache[path]; ok && c.modTime.Equal(info.ModTime()) && c.size == info.Size() {
			out = append(out, c.meta)
			continue
		}
		meta, err := readSnapshotMeta(path)
		if err != nil {
			continue
		}
		metaCache[path] = cachedMeta{modTime: info.ModTime(), size: info.Size(), meta: meta}
		out = append(out, meta)
	}
	// Drop cache entries for files in this dir that disappeared (deleted
	// snapshots) so the cache tracks the directory rather than growing without
	// bound. Entries from other directories are left untouched.
	for path := range metaCache {
		if _, ok := seen[path]; !ok && filepath.Dir(path) == filepath.Clean(dir) {
			delete(metaCache, path)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].CapturedAt.After(out[j].CapturedAt) })
	return out, nil
}

// readSnapshotMeta extracts a SnapshotMeta from a snapshot file without decoding
// its Stats array. It streams the JSON header field by field; the moment it
// reaches the "Stats" key it stops, so for files carrying QueryCount (written
// before Stats) the large array is never decompressed. Files predating
// QueryCount fall back to counting array elements via the token stream, which
// still avoids unmarshalling each row into a struct.
func readSnapshotMeta(path string) (SnapshotMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return SnapshotMeta{}, err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return SnapshotMeta{}, err
	}
	defer func() { _ = gz.Close() }()

	dec := json.NewDecoder(gz)
	if tok, err := dec.Token(); err != nil {
		return SnapshotMeta{}, err
	} else if d, ok := tok.(json.Delim); !ok || d != '{' {
		return SnapshotMeta{}, fmt.Errorf("snapshot %q: expected JSON object", path)
	}

	meta := SnapshotMeta{Path: path}
	haveCount := false
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return SnapshotMeta{}, err
		}
		key, _ := keyTok.(string)
		var dst any
		switch key {
		case "Version":
			dst = &meta.Version
		case "Target":
			dst = &meta.Target
		case "Database":
			dst = &meta.Database
		case "CapturedAt":
			dst = &meta.CapturedAt
		case "StatsReset":
			dst = &meta.StatsReset
		case "TrackPlanning":
			dst = &meta.TrackPlanning
		case "QueryCount":
			dst = &meta.QueryCount
			haveCount = true
		case "Stats":
			if haveCount {
				return meta, nil // header carried the count; skip the array entirely
			}
			n, err := countArrayElements(dec)
			if err != nil {
				return SnapshotMeta{}, err
			}
			meta.QueryCount = n
			return meta, nil
		default:
			if err := skipValue(dec); err != nil { // tolerate unknown fields
				return SnapshotMeta{}, err
			}
			continue
		}
		if err := dec.Decode(dst); err != nil {
			return SnapshotMeta{}, err
		}
	}
	return meta, nil
}

// countArrayElements consumes a JSON array from dec (positioned just before the
// opening '[') and returns its element count without materialising the values.
func countArrayElements(dec *json.Decoder) (int, error) {
	tok, err := dec.Token()
	if err != nil {
		return 0, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return 0, nil // null or non-array: treat as empty
	}
	n := 0
	for dec.More() {
		if err := skipValue(dec); err != nil {
			return 0, err
		}
		n++
	}
	if _, err := dec.Token(); err != nil { // closing ']'
		return 0, err
	}
	return n, nil
}

// skipValue consumes exactly one JSON value from dec — scalar, object or array,
// recursing through nested containers — without allocating a destination for it.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil // scalar already consumed
	}
	switch d {
	case '{':
		for dec.More() {
			if _, err := dec.Token(); err != nil { // key
				return err
			}
			if err := skipValue(dec); err != nil { // value
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := skipValue(dec); err != nil {
				return err
			}
		}
	}
	if _, err := dec.Token(); err != nil { // closing '}' or ']'
		return err
	}
	return nil
}

// LoadSnapshot reads and decodes a single gzip-compressed snapshot file.
func LoadSnapshot(path string) (*Snapshot, error) {
	data, err := readSnapshotFile(path)
	if err != nil {
		return nil, fmt.Errorf("read snapshot %q: %w", path, err)
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("decode snapshot %q: %w", path, err)
	}
	return &s, nil
}

// DeleteSnapshot removes a snapshot file.
func DeleteSnapshot(path string) error {
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete snapshot %q: %w", path, err)
	}
	return nil
}

// sanitizeDB makes a database name safe for a filename: anything that isn't a
// letter, digit, dash or underscore becomes an underscore. Empty names (the
// libpq "same as user" default) become "db" so the file still has a stem.
func sanitizeDB(db string) string {
	if db == "" {
		return "db"
	}
	var b strings.Builder
	for _, r := range db {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
