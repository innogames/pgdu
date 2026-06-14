package pg

import (
	"fmt"
	"strings"
)

// WALSummary is the header snapshot for the WAL inspector overview: the
// current write position, the segment file it lands in, wal_level, the
// pg_wal directory's file count and size, and the cluster-wide pg_stat_wal
// generation counters. StartLSN/EndLSN/WindowBytes describe the LSN window
// the rmgr breakdown below was computed over. All built-in sources, so it
// renders even without pg_walinspect — but a privilege error on pg_ls_waldir
// / pg_stat_wal is treated as non-fatal by the caller (summary "unavailable").
type WALSummary struct {
	InsertLSN    string
	FlushLSN     string
	CurrentFile  string
	WalLevel     string
	SegmentFiles int64
	SegmentBytes int64
	StatRecords  int64
	StatFPI      int64
	StatBytes    int64

	// Window the rmgr stats were computed over (resolved by sqlWALWindow).
	StartLSN    string
	EndLSN      string
	WindowBytes int64
}

// WALRmgrStat is one resource-manager row of the WAL overview: how many
// records that manager wrote in the window and how those bytes split between
// record data and full-page images (FPI). CombinedSize = RecordSize + FPISize.
type WALRmgrStat struct {
	Name         string
	Count        int64
	RecordSize   int64
	FPISize      int64
	CombinedSize int64
}

// WALRecord is one entry from pg_get_wal_records_info: a single WAL record's
// position, owning xid, type, byte breakdown and human-readable description.
// LSN/xid fields are kept as text (pg_lsn/xid have no pgx codec; cast ::text
// in SQL). Xid is "0" for non-transactional records (checkpoints, etc.).
type WALRecord struct {
	StartLSN       string
	EndLSN         string
	PrevLSN        string
	Xid            string
	Rmgr           string
	RecordType     string
	RecordLength   int32
	MainDataLength int32
	FPILength      int32
	Description    string
	BlockRef       string
}

// CombinedSize is record bytes plus full-page-image bytes — what the bar in
// the records view scales against.
func (r WALRecord) CombinedSize() int64 { return int64(r.RecordLength) + int64(r.FPILength) }

// WALBlockRef is one block reference of a record, from pg_get_wal_block_info
// (PostgreSQL 16+). It ties the record back to a concrete relation block:
// (RelDatabase, RelFileNode, ForkNumber, BlockNumber). FPILength > 0 means
// this record carried a full-page image of the block — the dominant source
// of WAL write amplification.
type WALBlockRef struct {
	BlockID         int32
	RelTablespace   uint32
	RelDatabase     uint32
	RelFileNode     uint32
	ForkNumber      int32
	BlockNumber     int64
	Rmgr            string
	RecordType      string
	BlockDataLength int32
	FPILength       int32
	// FPIInfo is the text[] of full-page-image flag names (e.g. {APPLY},
	// {APPLY,COMPRESSED}); nil when this block carried no page image.
	FPIInfo     []string
	Description string
	// RelName is the relation this block belongs to, resolved from
	// relfilenode via pg_filenode_relation. For a TOAST relation this is the
	// owning table's name, not the pg_toast.pg_toast_<oid> internal name.
	// Empty when the relation is in another database or has been dropped
	// (relfilenode no longer maps).
	RelName string
	// IsToast reports that the block belongs to a TOAST relation; RelName then
	// names the owning table and the UI tags the row with "(toast)".
	IsToast bool
	// DBName is reldatabase resolved against pg_database. Empty for shared
	// relations (reldatabase 0) or an unknown OID, in which case the UI falls
	// back to the numeric database OID.
	DBName string
}

// HeapTID best-effort-extracts the tuple id (block, offset) this block
// reference touched. The block number is RelBlockNumber; the offset is parsed
// from the record description, which for heap records reads like
// "off 15 flags 0x00". Only meaningful on the main fork — index/fsm/vm forks
// have no heap tuple — so it returns ("", false) elsewhere or when the
// description carries no "off N". Multi-block records repeat one description,
// so the offset is the record's primary tuple, not necessarily this block's.
func (b WALBlockRef) HeapTID() (string, bool) {
	if b.ForkNumber != 0 {
		return "", false
	}
	const marker = "off "
	i := strings.Index(b.Description, marker)
	if i < 0 {
		return "", false
	}
	rest := b.Description[i+len(marker):]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == 0 {
		return "", false
	}
	return fmt.Sprintf("(%d,%s)", b.BlockNumber, rest[:j]), true
}

// ForkName maps relforknumber to its short fork name (main/fsm/vm/init).
func (b WALBlockRef) ForkName() string {
	switch b.ForkNumber {
	case 0:
		return "main"
	case 1:
		return "fsm"
	case 2:
		return "vm"
	case 3:
		return "init"
	}
	return fmt.Sprintf("fork%d", b.ForkNumber)
}
