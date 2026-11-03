package pg

import (
	"context"
	"encoding/json"
	"fmt"
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
	meta, err := readSizeHeader(path, func(_ *json.Decoder) error { return nil })
	if err != nil {
		return SizeSampleMeta{}, err
	}
	return meta, nil
}

// readSizeHeader streams a sample's header into a SizeSampleMeta and hands the
// decoder — parked just before the Rows array — to atRows. Splitting it out of
// readSizeMeta is what lets the trend's projected reader get the header's
// MinBytes and CapturedAt from the same pass that reads the rows: the floor
// decides whether an absent relation reads as zero or as unknown, so a reader
// that saw the rows without the header could not tell those apart.
func readSizeHeader(path string, atRows func(*json.Decoder) error) (SizeSampleMeta, error) {
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
	}, func(dec *json.Decoder) error {
		// Kind and Version are header fields, so they are known by the time
		// the decoder reaches Rows: check before handing the array over
		// rather than after reading rows this build may not understand.
		if err := checkSizeHeader(path, meta.Kind, meta.Version); err != nil {
			return err
		}
		return atRows(dec)
	})
	if err != nil {
		return SizeSampleMeta{}, err
	}
	// A file whose header ends before "Rows" never reached the check above.
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

// sizeRowBrief is the trend's projection of SizeRow: the logical identity and
// the one figure a size trace plots. The JSON tags match SizeRow's, so the
// decoder fills it from the same objects; the fourteen fields left out are
// skipped by encoding/json rather than allocated per row, which is what makes
// reading twenty samples to build a history affordable where twenty
// LoadSizeSample calls would not be.
type sizeRowBrief struct {
	Schema string `json:"s"`
	Name   string `json:"n"`
	Bytes  int64  `json:"b"`
}

// Key is the row's logical identity, spelled exactly as SizeRow.Key does — the
// two must agree or a trace would not line up with the diff it annotates.
func (r sizeRowBrief) Key() string { return r.Schema + "." + r.Name }

// readSizeRowsProjected streams a sample's rows through fn without ever
// materialising the whole file, and returns the header alongside. It is the
// read path for size history, where LoadSizeSample's "gunzip it all, unmarshal
// every field" is the wrong shape: a thirty-sample history of a ten-thousand
// relation database is tens of megabytes of JSON, of which the trend reads
// three fields per row.
func readSizeRowsProjected(path string, fn func(sizeRowBrief)) (SizeSampleMeta, error) {
	return readSizeHeader(path, func(dec *json.Decoder) error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		// A null or absent Rows array is an empty sample, not an error: the
		// same tolerance countArrayElements shows.
		if d, ok := tok.(json.Delim); !ok || d != '[' {
			return nil
		}
		for dec.More() {
			var r sizeRowBrief
			if err := dec.Decode(&r); err != nil {
				return err
			}
			fn(r)
		}
		_, err = dec.Token() // closing ']'
		return err
	})
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

// sizeID projects a listing entry down to the identity the kind-blind sweeps
// work on. See sampleID for why this is a function rather than a method.
func sizeID(m SizeSampleMeta) sampleID {
	return sampleID{path: m.Path, target: m.Target, database: m.Database, at: m.CapturedAt}
}

// PruneSizeSamples keeps the newest keep size samples in dir for one
// (target, database) and deletes the rest, returning how many it removed.
// keep < 1 prunes nothing.
//
// It lists through ListSizeSamples, which is both why the query snapshots
// sharing the directory are unreachable from here and why metas arrive newest
// first — the order pruneKeepNewest needs to treat keep as a prefix count.
func PruneSizeSamples(dir, target, database string, keep int) (int, error) {
	metas, err := ListSizeSamples(dir)
	if err != nil {
		return 0, err
	}
	return pruneKeepNewest(metas, sizeID, target, database, keep)
}

// SizeSampleCapturedToday reports whether metas already holds a sample of
// (target, database) taken on now's UTC calendar day. It takes the listing
// rather than a directory because its one caller — the auto-capture decision
// on tool open — has just listed it. See capturedToday for why UTC.
func SizeSampleCapturedToday(metas []SizeSampleMeta, target, database string, now time.Time) bool {
	return capturedToday(metas, sizeID, target, database, now)
}
