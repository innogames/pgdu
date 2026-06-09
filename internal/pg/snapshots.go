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
	Stats         []QueryStat
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
func SaveSnapshot(dir string, s *Snapshot) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create snapshot dir %q: %w", dir, err)
	}
	name := fmt.Sprintf("%s-%s%s", sanitizeDB(s.Database), s.CapturedAt.UTC().Format("20060102T150405Z"), snapshotExt)
	path := filepath.Join(dir, name)
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
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return "", fmt.Errorf("write snapshot %q: %w", path, err)
	}
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

// ListSnapshots returns the metadata of every readable *.json.gz snapshot in
// dir, newest capture first. A missing directory is not an error (no snapshots
// yet); unreadable or undecodable files are skipped so one bad file doesn't hide
// the rest. Pre-compression plain *.json files are ignored.
func ListSnapshots(dir string) ([]SnapshotMeta, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read snapshot dir %q: %w", dir, err)
	}
	var out []SnapshotMeta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), snapshotExt) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := readSnapshotFile(path)
		if err != nil {
			continue
		}
		// Decode into a header struct that mirrors Snapshot but counts (and then
		// drops) the Stats array — json reads the whole file but retains only the
		// row count, keeping listing cheap on memory.
		var hdr struct {
			Version       int
			Target        string
			Database      string
			CapturedAt    time.Time
			StatsReset    time.Time
			TrackPlanning bool
			Stats         []json.RawMessage
		}
		if err := json.Unmarshal(data, &hdr); err != nil {
			continue
		}
		out = append(out, SnapshotMeta{
			Path:          path,
			Version:       hdr.Version,
			Target:        hdr.Target,
			Database:      hdr.Database,
			CapturedAt:    hdr.CapturedAt,
			StatsReset:    hdr.StatsReset,
			TrackPlanning: hdr.TrackPlanning,
			QueryCount:    len(hdr.Stats),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CapturedAt.After(out[j].CapturedAt) })
	return out, nil
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
