package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// sizeMetaCache memoizes size-sample listing metadata. It is per-kind, so
// listing the query snapshots that share the directory never evicts these
// entries (and vice versa) — see metaStore.
var sizeMetaCache = newMetaStore[SizeSampleMeta]()

// CaptureSizes reads every relation's on-disk footprint in db and wraps it with
// the identity a later diff needs. Relations smaller than minBytes fold into
// the sample's Other counters (0 keeps every relation), so a cluster with a
// six-figure relation count can still be sampled cheaply without the total
// going dishonest.
//
// The identity reads are best-effort: a missing privilege yields 0 or a zero
// time rather than failing the capture, matching how CaptureSnapshot treats
// stats_reset and track_planning. The relation scan itself is not — a failure
// there means the sample would be incomplete, and an incomplete sample read
// back as a window would report every missing relation as dropped.
func (c *Client) CaptureSizes(ctx context.Context, db string, minBytes int64) (*SizeSample, error) {
	if db == "" {
		db = c.DefaultDB()
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}

	rows, err := collect(ctx, pool, fmt.Sprintf("capture sizes in %q", db), sqlSizeSample, nil,
		func(row pgx.CollectableRow) (SizeRow, error) {
			var r SizeRow
			err := row.Scan(&r.OID, &r.Schema, &r.Name, &r.RelKind, &r.RelFilenode,
				&r.ParentOID, &r.PartBound,
				&r.HeapBytes, &r.IndexBytes, &r.ToastBytes, &r.TotalBytes,
				&r.EstRows, &r.NLive, &r.NDead, &r.NIns, &r.NDel, &r.NInsSinceVac)
			return r, err
		})
	if err != nil {
		return nil, err
	}

	s := &SizeSample{
		Kind:       sizeKind,
		Version:    sizeVersion,
		Target:     c.Target(),
		Database:   db,
		CapturedAt: time.Now(),
		Rows:       rows,
	}

	type sizeMeta struct {
		oid       uint32
		blockSize int64
		reset     *time.Time
	}
	for _, m := range collectBestEffort(ctx, pool, sqlSizeMeta, nil, func(r pgx.Rows) (sizeMeta, bool) {
		var m sizeMeta
		if err := r.Scan(&m.oid, &m.blockSize, &m.reset); err != nil {
			return sizeMeta{}, false
		}
		return m, true
	}) {
		s.DatabaseOID, s.BlockSize = m.oid, m.blockSize
		if m.reset != nil {
			s.StatsReset = *m.reset
		}
	}

	for _, id := range collectBestEffort(ctx, pool, sqlSizeSystemID, nil, func(r pgx.Rows) (int64, bool) {
		var id int64
		if err := r.Scan(&id); err != nil {
			return 0, false
		}
		return id, true
	}) {
		s.SystemID = id
	}

	// Per-database totals ride along so the growth tool's database scope works
	// from a single database's sample. has_database_privilege in sqlDatabases
	// already drops the ones this role cannot see.
	s.Databases = collectBestEffort(ctx, pool, sqlDatabases, nil, func(r pgx.Rows) (Database, bool) {
		var d Database
		if err := r.Scan(&d.Name, &d.SizeBytes); err != nil {
			return Database{}, false
		}
		return d, true
	})

	s.fold(minBytes)
	return s, nil
}

// LiveSizeIdentity is the current server's identity and database size, read
// without capturing a sample. The growth browser needs it on every listing —
// to bar the live "now" row and to tell a sample from another cluster or a
// recreated database apart from one that simply predates a restore — and a
// full CaptureSizes for that would scan every relation.
//
// Every field degrades independently: an unreadable system identifier is 0,
// which the compatibility check must read as "unknown", never as a mismatch.
type LiveSizeIdentity struct {
	OID        uint32
	SystemID   int64
	TotalBytes int64
	StatsReset time.Time
}

func (c *Client) LiveSizeIdentity(ctx context.Context, db string) (LiveSizeIdentity, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return LiveSizeIdentity{}, err
	}
	var live LiveSizeIdentity
	for _, m := range collectBestEffort(ctx, pool, sqlSizeMeta, nil, func(r pgx.Rows) (LiveSizeIdentity, bool) {
		var v LiveSizeIdentity
		var blockSize int64
		var reset *time.Time
		if err := r.Scan(&v.OID, &blockSize, &reset); err != nil {
			return LiveSizeIdentity{}, false
		}
		if reset != nil {
			v.StatsReset = *reset
		}
		return v, true
	}) {
		live = m
	}
	for _, id := range collectBestEffort(ctx, pool, sqlSizeSystemID, nil, func(r pgx.Rows) (int64, bool) {
		var id int64
		if err := r.Scan(&id); err != nil {
			return 0, false
		}
		return id, true
	}) {
		live.SystemID = id
	}
	for _, n := range collectBestEffort(ctx, pool, sqlSizeDatabaseSize, nil, func(r pgx.Rows) (int64, bool) {
		var n int64
		if err := r.Scan(&n); err != nil {
			return 0, false
		}
		return n, true
	}) {
		live.TotalBytes = n
	}
	return live, nil
}

// SaveSizeSample writes s to dir as gzip-compressed JSON and returns the file
// path. The filename is <sanitized-db>-<UTC timestamp>.sizes.gz, the same stem
// rule query snapshots use, so both kinds sort chronologically per database
// while staying unambiguously distinguishable by extension.
//
// It deliberately has no side effects beyond the write: pruning is the caller's
// to run, which is what lets a retention test save a directory full of samples
// without them pruning each other.
func SaveSizeSample(dir string, s *SizeSample) (string, error) {
	// Stamp the identity, the count and the total so the streaming reader can
	// trust the header. SaveSizeSample is the only writer, so this keeps the
	// on-disk values authoritative regardless of how the caller built the
	// sample. The total spans the folded rows too, via OtherBytes, so a sample
	// taken with a size floor still reports an honest database total.
	s.Kind = sizeKind
	s.Version = sizeVersion
	s.RowCount = len(s.Rows)
	s.TotalBytes = s.OtherBytes
	for _, r := range s.Rows {
		s.TotalBytes += r.TotalBytes
	}
	return writeGzipJSON(dir, s.Database, s.CapturedAt, kindSizes, s)
}

// ListSizeSamples returns the metadata of every readable *.sizes.gz sample in
// dir, newest capture first. A missing directory is not an error (no samples
// yet); unreadable, undecodable and too-new files are skipped so one bad file
// doesn't hide the rest. The query snapshots sharing the directory are ignored.
//
// Metadata is read by streaming only the leading header fields and stopping at
// the Rows array, so listing never decompresses the bulk of a file.
func ListSizeSamples(dir string) ([]SizeSampleMeta, error) {
	out, err := sizeMetaCache.list(dir, kindSizes, func(path string, size int64) (SizeSampleMeta, error) {
		meta, err := readSizeMeta(path)
		meta.FileSize = size
		return meta, err
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CapturedAt.After(out[j].CapturedAt) })
	return out, nil
}

// readSizeMeta extracts a SizeSampleMeta from a sample file without decoding
// its Rows array: it streams the header and stops the moment it reaches "Rows",
// reporting the RowCount the writer stamped there.
//
// Unlike readSnapshotMeta it validates Kind and Version. A file this build
// cannot read is an error rather than a half-filled row, so ListSizeSamples
// skips it instead of offering a window that would diff garbage.
func readSizeMeta(path string) (SizeSampleMeta, error) {
	var meta SizeSampleMeta
	err := readHeader(path, "Rows", func(key string) any {
		switch key {
		case "Kind":
			return &meta.Kind
		case "Version":
			return &meta.Version
		case "Target":
			return &meta.Target
		case "Database":
			return &meta.Database
		case "DatabaseOID":
			return &meta.DatabaseOID
		case "SystemID":
			return &meta.SystemID
		case "CapturedAt":
			return &meta.CapturedAt
		case "StatsReset":
			return &meta.StatsReset
		case "BlockSize":
			return &meta.BlockSize
		case "MinBytes":
			return &meta.MinBytes
		case "OtherBytes":
			return &meta.OtherBytes
		case "OtherCount":
			return &meta.OtherCount
		case "TotalBytes":
			return &meta.TotalBytes
		case "RowCount":
			return &meta.RowCount
		}
		return nil // tolerate fields a newer build added
	}, func(_ *json.Decoder) error { return nil })
	if err != nil {
		return SizeSampleMeta{}, err
	}
	if err := checkSizeHeader(path, meta.Kind, meta.Version); err != nil {
		return SizeSampleMeta{}, err
	}
	meta.Path = path
	return meta, nil
}

// checkSizeHeader rejects a file that isn't a size sample this build can read.
func checkSizeHeader(path, kind string, version int) error {
	if kind != sizeKind {
		return fmt.Errorf("%q: not a size sample (kind %q)", path, kind)
	}
	if version > sizeVersion {
		return fmt.Errorf("%w: %q is version %d, this build reads %d",
			ErrSampleVersion, path, version, sizeVersion)
	}
	return nil
}

// LoadSizeSample reads and decodes a single gzip-compressed size sample.
func LoadSizeSample(path string) (*SizeSample, error) {
	data, err := readGzipFile(path)
	if err != nil {
		return nil, fmt.Errorf("read size sample %q: %w", path, err)
	}
	var s SizeSample
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("decode size sample %q: %w", path, err)
	}
	if err := checkSizeHeader(path, s.Kind, s.Version); err != nil {
		return nil, err
	}
	return &s, nil
}

// fold moves every relation under min into the Other counters, so the bytes
// the sample stops itemising are still counted in its total (SaveSizeSample
// sums Rows plus OtherBytes). A min of 0 folds nothing.
//
// Rows are left in their existing order; the caller's SQL already sorts them.
func (s *SizeSample) fold(min int64) {
	s.MinBytes = min
	if min <= 0 {
		s.OtherBytes, s.OtherCount = 0, 0
		return
	}
	kept := s.Rows[:0]
	var otherBytes int64
	otherCount := 0
	for _, r := range s.Rows {
		if r.TotalBytes < min {
			otherBytes += r.TotalBytes
			otherCount++
			continue
		}
		kept = append(kept, r)
	}
	s.Rows = kept
	s.OtherBytes, s.OtherCount = otherBytes, otherCount
}

// RetentionPolicy is the tiered keep rule for size samples: every sample
// younger than All, then the newest per UTC calendar day back to Daily, then
// the newest per ISO week back to Weekly, then nothing.
//
// It drops WHOLE samples, never rows, so every retained file stays complete and
// self-describing and any two of them diff against each other.
type RetentionPolicy struct {
	All    time.Duration
	Daily  time.Duration
	Weekly time.Duration
}

// RetentionFor builds a policy from the single operator-facing knob: the hard
// horizon. The 7-day and 90-day tiers below it are an implementation detail of
// "keep history cheap", not a question anybody asks.
func RetentionFor(horizon time.Duration) RetentionPolicy {
	return RetentionPolicy{
		All:    7 * 24 * time.Hour,
		Daily:  90 * 24 * time.Hour,
		Weekly: horizon,
	}
}

// DefaultRetention keeps roughly two years of history at ~140 files per
// database.
var DefaultRetention = RetentionFor(2 * 365 * 24 * time.Hour)

// keep reports whether a sample captured at t survives the policy, given the
// newest sample already kept in its day and week bucket.
func (p RetentionPolicy) keep(age time.Duration, firstInDay, firstInWeek bool) bool {
	switch {
	case age <= p.All:
		return true
	case age <= p.Daily:
		return firstInDay
	case age <= p.Weekly:
		return firstInWeek
	}
	return false
}

// PruneSizeSamples deletes the size samples in dir for one (target, database)
// that the policy no longer keeps, and returns how many it removed.
//
// It is scoped three ways on purpose, because the directory is shared and
// world-writable: it lists through ListSizeSamples, so a query snapshot is
// unreachable from here whatever the extension rules become, and it matches
// Target and Database so a cron sampling one database on one host never prunes
// another's history. Deleting another *user's* samples of the same database is
// deliberate, and matches how snapshots are already shared.
//
// Because ListSizeSamples returns newest first, the most recent sample is
// always in the All tier and no policy can delete it.
func PruneSizeSamples(dir, target, database string, now time.Time, p RetentionPolicy) (int, error) {
	metas, err := ListSizeSamples(dir)
	if err != nil {
		return 0, err
	}

	seenDay := map[string]bool{}
	seenWeek := map[string]bool{}
	removed := 0
	var firstErr error
	for _, m := range metas {
		if m.Target != target || m.Database != database {
			continue
		}
		at := m.CapturedAt.UTC()
		day := at.Format("2006-01-02")
		year, week := at.ISOWeek()
		wk := fmt.Sprintf("%04d-W%02d", year, week)

		firstInDay, firstInWeek := !seenDay[day], !seenWeek[wk]
		seenDay[day], seenWeek[wk] = true, true

		if p.keep(now.Sub(m.CapturedAt), firstInDay, firstInWeek) {
			continue
		}
		if err := DeleteSnapshot(m.Path); err != nil {
			// A concurrent sampler may have pruned it already; that is not a
			// failure. Any other error is reported but never stops the sweep.
			if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}
