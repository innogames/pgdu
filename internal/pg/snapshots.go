package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	// FileSize is the on-disk (compressed) size of the snapshot file, filled by
	// ListSnapshots from the directory entry rather than from the file's
	// contents — readSnapshotMeta never sees it.
	FileSize int64
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

// SaveSnapshot writes s to dir as gzip-compressed JSON and returns the file
// path. The filename is <sanitized-db>-<UTC timestamp>.json.gz so snapshots
// sort and read chronologically. See writeGzipJSON for the shared permission
// handling that makes the directory usable by every user on the host.
func SaveSnapshot(dir string, s *Snapshot) (string, error) {
	// Stamp the row count so ListSnapshots can read it from the header (it is
	// serialized before Stats) without decompressing the array, and the version
	// so a Snapshot the caller built itself is still labelled — only
	// CaptureSnapshot sets it, so every other construction would land on disk as
	// version 0. SaveSnapshot is the only writer, so this keeps the on-disk
	// values authoritative regardless of how the caller built the Snapshot.
	s.Version = snapshotVersion
	s.QueryCount = len(s.Stats)
	return writeGzipJSON(dir, s.Database, s.CapturedAt, kindSnapshot, s)
}

// snapMetaCache memoizes snapshot listing metadata. It is per-kind, so listing
// the size samples that share the directory never evicts these entries.
var snapMetaCache = newMetaStore[SnapshotMeta]()

// ListSnapshots returns the metadata of every readable *.json.gz snapshot in
// dir, newest capture first. A missing directory is not an error (no snapshots
// yet); unreadable or undecodable files are skipped so one bad file doesn't hide
// the rest. Pre-compression plain *.json files are ignored, as are the size
// samples that share the directory.
//
// Metadata is read by streaming only the leading header fields and stopping at
// the Stats array, so listing never decompresses the bulk of a file. Results are
// cached per path (validated by mod time + size), so repeat listings cost a stat
// per file rather than a full read.
func ListSnapshots(dir string) ([]SnapshotMeta, error) {
	out, err := snapMetaCache.list(dir, kindSnapshot, func(path string, size int64) (SnapshotMeta, error) {
		meta, err := readSnapshotMeta(path)
		meta.FileSize = size
		return meta, err
	})
	if err != nil {
		return nil, err
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
	meta := SnapshotMeta{Path: path}
	haveCount := false
	err := readHeader(path, "Stats", func(key string) any {
		switch key {
		case "Version":
			return &meta.Version
		case "Target":
			return &meta.Target
		case "Database":
			return &meta.Database
		case "CapturedAt":
			return &meta.CapturedAt
		case "StatsReset":
			return &meta.StatsReset
		case "TrackPlanning":
			return &meta.TrackPlanning
		case "QueryCount":
			haveCount = true
			return &meta.QueryCount
		}
		return nil // tolerate unknown fields
	}, func(dec *json.Decoder) error {
		if haveCount {
			return nil // header carried the count; skip the array entirely
		}
		n, err := countArrayElements(dec)
		if err != nil {
			return err
		}
		meta.QueryCount = n
		return nil
	})
	if err != nil {
		return SnapshotMeta{}, err
	}
	return meta, nil
}

// LoadSnapshot reads and decodes a single gzip-compressed snapshot file.
func LoadSnapshot(path string) (*Snapshot, error) {
	data, err := readGzipFile(path)
	if err != nil {
		return nil, fmt.Errorf("read snapshot %q: %w", path, err)
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("decode snapshot %q: %w", path, err)
	}
	return &s, nil
}

// snapID projects a listing entry down to the identity the kind-blind sweeps
// work on. See sampleID for why this is a function rather than a method.
func snapID(m SnapshotMeta) sampleID {
	return sampleID{path: m.Path, target: m.Target, database: m.Database, at: m.CapturedAt}
}

// PruneSnapshots keeps the newest keep query snapshots in dir for one
// (target, database) and deletes the rest, returning how many it removed.
// keep < 1 prunes nothing.
//
// It lists through ListSnapshots, which is both why the size samples sharing
// the directory are unreachable from here and why metas arrive newest first —
// the order pruneKeepNewest needs to treat keep as a prefix count.
func PruneSnapshots(dir, target, database string, keep int) (int, error) {
	metas, err := ListSnapshots(dir)
	if err != nil {
		return 0, err
	}
	return pruneKeepNewest(metas, snapID, target, database, keep)
}

// SnapshotCapturedToday reports whether metas already holds a snapshot of
// (target, database) taken on now's UTC calendar day.
//
// It takes the listing rather than a directory because its one caller — the
// auto-capture decision on tool open — has just listed it, and because that
// caller must pass the RAW listing: the browser drops snapshots older than the
// live stats_reset, and a reset since this morning's capture would otherwise
// make today look empty and re-capture on every tool open. See capturedToday
// for why UTC.
func SnapshotCapturedToday(metas []SnapshotMeta, target, database string, now time.Time) bool {
	return capturedToday(metas, snapID, target, database, now)
}
