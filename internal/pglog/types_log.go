package pglog

import (
	"context"
	"time"
)

// Format is the on-disk shape of a server log (log_destination).
type Format int

const (
	FormatStderr    Format = iota // log_line_prefix + "SEVERITY:  message"
	FormatCSV                     // csvlog
	FormatJSON                    // jsonlog (PG15+)
	FormatPgBouncer               // pgbouncer's own log: "%m [%p] SEVERITY message" (no colon, no %l)
)

func (f Format) String() string {
	switch f {
	case FormatCSV:
		return "csvlog"
	case FormatJSON:
		return "jsonlog"
	case FormatPgBouncer:
		return "pgbouncer"
	}
	return "stderr"
}

// Severity orders PostgreSQL's message severities so a "floor" filter is a
// plain comparison. The non-primary tags (DETAIL, HINT, …) are not severities —
// they attach to the preceding primary entry and never appear here.
type Severity int

const (
	SevDebug Severity = iota
	SevInfo
	SevNotice
	SevLog
	SevWarning
	SevError
	SevFatal
	SevPanic
	numLogSeverities
)

func (s Severity) String() string {
	switch s {
	case SevDebug:
		return "DEBUG"
	case SevInfo:
		return "INFO"
	case SevNotice:
		return "NOTICE"
	case SevLog:
		return "LOG"
	case SevWarning:
		return "WARNING"
	case SevError:
		return "ERROR"
	case SevFatal:
		return "FATAL"
	case SevPanic:
		return "PANIC"
	}
	return "?"
}

// Category is the analyzer's own classification of a primary entry. Every
// entry gets exactly one; it drives grouping and the section headers.
type Category int

const (
	CatError       Category = iota // ERROR/FATAL/PANIC (except deadlocks → CatLock)
	CatWarning                     // WARNING
	CatLock                        // lock waits, deadlocks
	CatTempFile                    // temporary file: path …, size N
	CatReplication                 // recovery / streaming / archiving
	CatOther                       // anything unclassified
	CatSlowQuery                   // duration: N ms  statement/execute …
	CatStatement                   // log_statement: "statement: …" / "execute <name>: …"
	CatCheckpoint                  // checkpoint/restartpoint starting/complete
	CatAutovacuum                  // automatic vacuum/analyze of table
	CatConnection                  // connection received/authorized/disconnection
	numLogCategories
)

// Categories lists categories in display order: signal first, chatter last.
var Categories = []Category{
	CatError, CatWarning, CatLock, CatTempFile, CatReplication, CatOther,
	CatSlowQuery, CatStatement, CatCheckpoint, CatAutovacuum, CatConnection,
}

func (c Category) Label() string {
	switch c {
	case CatError:
		return "errors"
	case CatWarning:
		return "warnings"
	case CatLock:
		return "locks"
	case CatTempFile:
		return "temp files"
	case CatReplication:
		return "replication"
	case CatOther:
		return "other"
	case CatSlowQuery:
		return "slow queries"
	case CatStatement:
		return "statements"
	case CatCheckpoint:
		return "checkpoints"
	case CatAutovacuum:
		return "autovacuum"
	case CatConnection:
		return "connections"
	}
	return "?"
}

// Short is the compact tag used in table cells.
func (c Category) Short() string {
	switch c {
	case CatError:
		return "error"
	case CatWarning:
		return "warn"
	case CatLock:
		return "lock"
	case CatTempFile:
		return "tmpfile"
	case CatReplication:
		return "repl"
	case CatOther:
		return "other"
	case CatSlowQuery:
		return "slow"
	case CatStatement:
		return "stmt"
	case CatCheckpoint:
		return "ckpt"
	case CatAutovacuum:
		return "autovac"
	case CatConnection:
		return "conn"
	}
	return "?"
}

// CheckpointFields is the parsed body of a "checkpoint complete" (or
// restartpoint) line. Starting lines only carry Starting + Reason.
type CheckpointFields struct {
	Starting   bool
	Reason     string
	Buffers    int64
	BuffersPct float64
	WALAdded   int64
	WALRemoved int64
	WALRecycle int64
	WriteSec   float64
	SyncSec    float64
	TotalSec   float64
	DistanceKB int64
	EstimateKB int64
}

// PgBouncerStats is the parsed periodic "stats:" line pgbouncer logs every
// stats_period: per-second rates and mean durations over that period.
type PgBouncerStats struct {
	XactsPerSec, QueriesPerSec, ClientParsesPerSec, ServerParsesPerSec, BindsPerSec int64
	InBytesPerSec, OutBytesPerSec                                                   int64
	XactUs, QueryUs, WaitUs                                                         int64
}

// Entry is one primary log record plus the DETAIL/HINT/STATEMENT/CONTEXT
// lines PostgreSQL emits right after it. Text fields are sub-slices of the
// window buffer (zero-copy) — the TUI converts only what it renders.
type Entry struct {
	Off      int64 // byte offset of the primary line within the window buffer
	Time     time.Time
	PID      int32
	Line     int32 // %l session line number, 0 when the prefix lacks it
	Session  []byte
	User     []byte
	DB       []byte
	Host     []byte
	App      []byte
	SQLState []byte

	Severity Severity
	Category Category
	// Orphan marks a synthetic primary created for an attachment (DETAIL, …)
	// whose real primary lies before the window start.
	Orphan bool

	Message   []byte
	Detail    []byte
	Hint      []byte
	Statement []byte
	Context   []byte
	Query     []byte
	Location  []byte

	// Category-specific values; only the relevant ones are set.
	DurationMs float64 // CatSlowQuery
	SQL        []byte  // CatSlowQuery: the statement text after "statement:"/"execute …:" (or auto_explain's "Query Text:")
	// Plan is the auto_explain output for this statement: the body of a
	// "duration: … plan:" line, moved onto the matching "statement:" entry by
	// MergePlans when both were logged for the same execution.
	Plan       []byte
	Checkpoint *CheckpointFields
	// PoolerStats is set on pgbouncer's periodic "stats:" line (category stays
	// CatOther; the TUI shows them as their own pane).
	PoolerStats *PgBouncerStats
	TempBytes   int64  // CatTempFile
	AVTable     []byte // CatAutovacuum: "db.schema.table"
	LockWaitMs  float64
}

// FirstLine returns the first line of the message (what group titles and
// single-line table cells show).
func (e *Entry) FirstLine() string {
	return firstLineOf(e.Message)
}

// Window describes how much of the file the report covers.
type Window struct {
	Requested   int64 // bytes asked for; 0 = whole file
	FileSize    int64 // -1 when unknown (gz, server without pg_stat_file)
	Start       int64 // file offset of the first parsed byte (Entry.Off is absolute)
	Bytes       int64 // bytes actually parsed after head alignment
	Truncated   bool  // the window did not reach the file start
	DroppedHead int64 // bytes discarded before the first complete primary line
	From, To    time.Time
	Lines       int
}

// SourceInfo identifies where a log came from, for the picker and header.
type SourceInfo struct {
	Kind    string // "local", "gz", "server"
	Path    string
	Size    int64 // -1 when unknown
	ModTime time.Time
	Rotated bool // .1 / .2.gz …
	Current bool // pg_current_logfile() / the active log
	// Lines is an estimate of the file's line count (see EstimateLines); -1
	// when unknown (server-side files).
	Lines int64
}

// Source abstracts local, gzip and server-side files so the parser only
// sees bytes.
type Source interface {
	Info() SourceInfo
	// ReadTail returns the last n bytes (n <= 0: whole file) aligned to the
	// first complete line, plus window metadata.
	ReadTail(ctx context.Context, n int64) ([]byte, Window, error)
	// ReadFrom reads [off, EOF) for incremental refresh. Sources that cannot
	// seek (gzip) return ErrNotIncremental; the caller falls back to ReadTail.
	ReadFrom(ctx context.Context, off int64) ([]byte, error)
	// Cursor snapshots the file identity (inode, size) so the next refresh can
	// tell an append from a rotation. Sources without one return nil.
	Cursor(ctx context.Context) *Cursor
}

// Cursor is the incremental-refresh bookmark for a seekable source.
type Cursor struct {
	Inode uint64
	Size  int64 // file size at the time of the read
	Off   int64 // first byte not yet parsed (start of the unterminated tail)
}

// SlowStats summarises the durations of one slow-query group.
type SlowStats struct {
	SumMs float64
	MaxMs float64
	P95Ms float64
	res   []float64 // bounded reservoir for the percentile
}

// AvgMs is the mean over count entries.
func (s *SlowStats) AvgMs(count int) float64 {
	if count == 0 {
		return 0
	}
	return s.SumMs / float64(count)
}

// CheckpointStats summarises "checkpoint complete" lines.
type CheckpointStats struct {
	Complete   int
	SumWrite   float64
	SumTotal   float64
	SumBuffers int64
	MaxTotal   float64
}

// TempStats summarises temp-file lines.
type TempStats struct {
	TotalBytes int64
	MaxBytes   int64
}

// Group is one aggregated message: entries sharing a fingerprint.
type Group struct {
	Key      string
	Title    string // normalized message or SQL, single line
	Category Category
	Severity Severity // highest seen
	Count    int
	First    time.Time
	Last     time.Time
	// Samples indexes into Report.Entries, oldest first, capped by the
	// aggregator so a 512 MiB window cannot pin every entry.
	Samples []int
	// Plans counts entries that carry an auto_explain plan.
	Plans int

	Slow       *SlowStats
	Checkpoint *CheckpointStats
	Temp       *TempStats
	Autovac    map[string]int // CatAutovacuum: per-table counts
}

// Histogram counts entries per time bucket and severity for the header
// sparklines.
type Histogram struct {
	Bucket time.Duration
	Start  time.Time
	Counts [][numLogSeverities]int
}

// Series returns the per-bucket count for one severity.
func (h *Histogram) Series(sev Severity) []int {
	out := make([]int, len(h.Counts))
	for i := range h.Counts {
		out[i] = h.Counts[i][sev]
	}
	return out
}

// Total returns the per-bucket count over all severities.
func (h *Histogram) Total() []int {
	out := make([]int, len(h.Counts))
	for i := range h.Counts {
		for _, n := range h.Counts[i] {
			out[i] += n
		}
	}
	return out
}

// PoolerMetric indexes the figures of a pgbouncer stats line that the header
// sparklines plot.
type PoolerMetric int

const (
	PoolerXacts PoolerMetric = iota
	PoolerQueries
	PoolerIn
	PoolerOut
	PoolerXactUs
	PoolerQueryUs
	PoolerWaitUs
	numPoolerMetrics
)

// PoolerMetrics lists the plotted metrics in display order.
var PoolerMetrics = []PoolerMetric{PoolerXacts, PoolerQueries, PoolerIn, PoolerOut, PoolerXactUs, PoolerQueryUs, PoolerWaitUs}

func (m PoolerMetric) Label() string {
	switch m {
	case PoolerXacts:
		return "xacts/s"
	case PoolerQueries:
		return "queries/s"
	case PoolerIn:
		return "in/s"
	case PoolerOut:
		return "out/s"
	case PoolerXactUs:
		return "xact"
	case PoolerQueryUs:
		return "query"
	case PoolerWaitUs:
		return "wait"
	}
	return "?"
}

// Value picks the metric's raw figure (rates per second, durations in µs).
func (m PoolerMetric) Value(st *PgBouncerStats) float64 {
	switch m {
	case PoolerXacts:
		return float64(st.XactsPerSec)
	case PoolerQueries:
		return float64(st.QueriesPerSec)
	case PoolerIn:
		return float64(st.InBytesPerSec)
	case PoolerOut:
		return float64(st.OutBytesPerSec)
	case PoolerXactUs:
		return float64(st.XactUs)
	case PoolerQueryUs:
		return float64(st.QueryUs)
	case PoolerWaitUs:
		return float64(st.WaitUs)
	}
	return 0
}

// PoolerHist is the pgbouncer stats series bucketed on the same time axis
// as Histogram (Bucket/Start), so the sparklines line up with the severity
// ones. Each bucket holds the mean of the stats lines that fell into it.
type PoolerHist struct {
	Sums   [numPoolerMetrics][]float64
	Counts []int
}

// Series returns the per-bucket mean of one metric; buckets without a stats
// line are 0.
func (h *PoolerHist) Series(m PoolerMetric) []float64 {
	out := make([]float64, len(h.Counts))
	for i, n := range h.Counts {
		if n > 0 {
			out[i] = h.Sums[m][i] / float64(n)
		}
	}
	return out
}

// Report is the parsed + aggregated view of one window of one log file.
type Report struct {
	Source         SourceInfo
	Window         Window
	Format         Format
	Prefix         string // effective log_line_prefix
	PrefixDetected bool   // true when guessed from the file rather than taken from the server

	Buf     []byte // the window; every Entry byte slice points into it
	Entries []Entry
	Groups  []Group // sorted by count desc, last-seen desc
	Hist    Histogram
	Pooler  PoolerHist // pgbouncer stats series on Hist's axis; empty otherwise

	BySeverity [numLogSeverities]int
	ByCategory [numLogCategories]int
	Unparsed   int // lines that matched no prefix and had no open entry
	// PoolerStats counts entries carrying a parsed pgbouncer stats line; the
	// TUI offers the pooler-stats pane only when it is nonzero.
	PoolerStats int
	Cursor      *Cursor

	// parser is retained so an incremental refresh can resume mid-entry.
	parser *Parser
}
