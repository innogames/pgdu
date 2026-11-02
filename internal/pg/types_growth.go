package pg

import (
	"errors"
	"time"
)

// sizeVersion is the on-disk schema version of a size sample.
//
// Unlike snapshotVersion this one IS checked on read. A growth table that
// silently mis-reads a future layout prints plausible wrong numbers, and
// because size samples share a directory with query snapshots a newer pgdu's
// files will sit right next to an older binary.
const sizeVersion = 1

// sizeKind is the Kind every size sample carries, so a file is self-describing
// under `zcat | head` and a rename across extensions is still caught on read.
const sizeKind = "sizes"

// ErrSampleVersion is returned by LoadSizeSample for a file written by a newer
// pgdu than this one can read.
var ErrSampleVersion = errors.New("size sample written by a newer pgdu")

// SizeSample is one immutable per-relation size capture of a single database.
//
// Field ORDER is load-bearing: every field a listing needs is serialized before
// the Rows array, so readSizeMeta stops at "Rows" and never decompresses the
// bulk of the file — the same trick Snapshot.QueryCount plays with Stats. The
// header fields carry no JSON tags, so they are decoded by exact Go field name
// exactly as Snapshot's are; SizeRow does carry them, for the reason given on
// that type.
type SizeSample struct {
	Kind        string
	Version     int
	Target      string // cli.Config.Target(), never HostLabel: samples are keyed on it
	Database    string
	DatabaseOID uint32 // identity guard — a drop/create reuses the name
	SystemID    int64  // pg_control_system().system_identifier; 0 = not readable
	CapturedAt  time.Time
	StatsReset  time.Time // pg_stat_database.stats_reset (zero = unknown)
	BlockSize   int64     // current_setting('block_size'); 0 = unknown
	MinBytes    int64     // the size floor in force at capture (0 = kept everything)
	OtherBytes  int64     // total bytes of the relations the floor folded away
	OtherCount  int       // how many, so the gap is visible rather than silent
	TotalBytes  int64     // sum over EVERY captured relation, folded ones included
	Databases   []Database
	RowCount    int // len(Rows), stamped by SaveSizeSample; the header-first stop
	Rows        []SizeRow
	// Indexes is nil unless per-index sampling was asked for. It reuses SizeRow
	// rather than a parallel type so turning it on is additive: same scan, same
	// diff, one more arm.
	Indexes []SizeRow
}

// SizeRow is one relation's footprint at capture time.
//
// The short JSON tags are a deliberate divergence from Snapshot's no-tags rule:
// this is the high-cardinality part — one row per relation, never looked up by
// key — where ~12-character Go field names repeated ten thousand times roughly
// double the pre-gzip size for no reader benefit.
type SizeRow struct {
	OID          uint32 `json:"o"`
	Schema       string `json:"s"`
	Name         string `json:"n"`
	RelKind      string `json:"k"`  // r | m | p (| i once indexes are sampled)
	RelFilenode  uint32 `json:"f"`  // a change between samples means a rewrite
	ParentOID    uint32 `json:"p"`  // pg_inherits.inhparent, 0 at the root
	PartBound    string `json:"pb"` // pg_get_expr(relpartbound) — a free time axis
	HeapBytes    int64  `json:"h"`
	IndexBytes   int64  `json:"i"`
	ToastBytes   int64  `json:"t"`
	TotalBytes   int64  `json:"b"`
	EstRows      int64  `json:"er"` // reltuples
	NLive        int64  `json:"nl"`
	NDead        int64  `json:"nd"`
	NIns         int64  `json:"ni"`
	NDel         int64  `json:"nx"`
	NInsSinceVac int64  `json:"nv"`
}

// Key is the row's logical identity within a sample. Schema and name beat OID,
// which survives pg_upgrade but not a dump/restore.
func (r SizeRow) Key() string { return r.Schema + "." + r.Name }

// SizeSampleMeta is the listing form of a SizeSample: the header without the
// arrays, which is everything the window picker needs to draw a row.
type SizeSampleMeta struct {
	Path        string
	Kind        string
	Version     int
	Target      string
	Database    string
	DatabaseOID uint32
	SystemID    int64
	CapturedAt  time.Time
	StatsReset  time.Time
	BlockSize   int64
	MinBytes    int64
	OtherBytes  int64
	OtherCount  int
	TotalBytes  int64
	RowCount    int
	// FileSize is the on-disk (compressed) size, filled by ListSizeSamples from
	// the directory entry rather than from the file's contents.
	FileSize int64
}

// CompatibleWith reports whether two samples describe the same thing, and names
// the reason when they don't. A mismatch is a label the picker shows, not a
// filter: a restored cluster is a real event to explain rather than hide.
//
// A zero DatabaseOID or SystemID means the identity was unreadable — the
// pg_control_system() read is revoked from PUBLIC — so it must read as
// compatible rather than as a mismatch.
func (m SizeSampleMeta) CompatibleWith(o SizeSampleMeta) (bool, string) {
	switch {
	case m.Target != o.Target:
		return false, "other server"
	case m.Database != o.Database:
		return false, "other database"
	case m.SystemID != 0 && o.SystemID != 0 && m.SystemID != o.SystemID:
		return false, "other cluster"
	case m.DatabaseOID != 0 && o.DatabaseOID != 0 && m.DatabaseOID != o.DatabaseOID:
		return false, "db recreated"
	}
	return true, ""
}

// meta projects the sample's header into its listing form, so the writer and
// the streaming reader can never disagree about what the header holds.
func (s *SizeSample) meta(path string, fileSize int64) SizeSampleMeta {
	return SizeSampleMeta{
		Path: path, Kind: s.Kind, Version: s.Version,
		Target: s.Target, Database: s.Database,
		DatabaseOID: s.DatabaseOID, SystemID: s.SystemID,
		CapturedAt: s.CapturedAt, StatsReset: s.StatsReset,
		BlockSize: s.BlockSize, MinBytes: s.MinBytes,
		OtherBytes: s.OtherBytes, OtherCount: s.OtherCount,
		TotalBytes: s.TotalBytes, RowCount: s.RowCount,
		FileSize: fileSize,
	}
}
