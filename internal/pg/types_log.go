package pg

import (
	"context"
	"time"
)

// LogFormat is the on-disk shape of a server log (log_destination).
type LogFormat int

const (
	LogFormatStderr    LogFormat = iota // log_line_prefix + "SEVERITY:  message"
	LogFormatCSV                        // csvlog
	LogFormatJSON                       // jsonlog (PG15+)
	LogFormatPgBouncer                  // pgbouncer's own log: "%m [%p] SEVERITY message" (no colon, no %l)
)

func (f LogFormat) String() string {
	switch f {
	case LogFormatCSV:
		return "csvlog"
	case LogFormatJSON:
		return "jsonlog"
	case LogFormatPgBouncer:
		return "pgbouncer"
	}
	return "stderr"
}

// LogSeverity orders PostgreSQL's message severities so a "floor" filter is a
// plain comparison. The non-primary tags (DETAIL, HINT, …) are not severities —
// they attach to the preceding primary entry and never appear here.
type LogSeverity int

const (
	SevDebug LogSeverity = iota
	SevInfo
	SevNotice
	SevLog
	SevWarning
	SevError
	SevFatal
	SevPanic
	numLogSeverities
)

func (s LogSeverity) String() string {
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

// LogCategory is the analyzer's own classification of a primary entry. Every
// entry gets exactly one; it drives grouping and the section headers.
type LogCategory int

const (
	CatError       LogCategory = iota // ERROR/FATAL/PANIC (except deadlocks → CatLock)
	CatWarning                        // WARNING
	CatLock                           // lock waits, deadlocks
	CatTempFile                       // temporary file: path …, size N
	CatReplication                    // recovery / streaming / archiving
	CatOther                          // anything unclassified
	CatSlowQuery                      // duration: N ms  statement/execute …
	CatStatement                      // log_statement: "statement: …" / "execute <name>: …"
	CatCheckpoint                     // checkpoint/restartpoint starting/complete
	CatAutovacuum                     // automatic vacuum/analyze of table
	CatConnection                     // connection received/authorized/disconnection
	numLogCategories
)

// LogCategories lists categories in display order: signal first, chatter last.
var LogCategories = []LogCategory{
	CatError, CatWarning, CatLock, CatTempFile, CatReplication, CatOther,
	CatSlowQuery, CatStatement, CatCheckpoint, CatAutovacuum, CatConnection,
}

func (c LogCategory) Label() string {
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
func (c LogCategory) Short() string {
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

// LogEntry is one primary log record plus the DETAIL/HINT/STATEMENT/CONTEXT
// lines PostgreSQL emits right after it. Text fields are sub-slices of the
// window buffer (zero-copy) — the TUI converts only what it renders.
type LogEntry struct {
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

	Severity LogSeverity
	Category LogCategory
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
	TempBytes  int64  // CatTempFile
	AVTable    []byte // CatAutovacuum: "db.schema.table"
	LockWaitMs float64
}

// FirstLine returns the first line of the message (what group titles and
// single-line table cells show).
func (e *LogEntry) FirstLine() string {
	return firstLineOf(e.Message)
}

// LogWindow describes how much of the file the report covers.
type LogWindow struct {
	Requested   int64 // bytes asked for; 0 = whole file
	FileSize    int64 // -1 when unknown (gz, server without pg_stat_file)
	Start       int64 // file offset of the first parsed byte (LogEntry.Off is absolute)
	Bytes       int64 // bytes actually parsed after head alignment
	Truncated   bool  // the window did not reach the file start
	DroppedHead int64 // bytes discarded before the first complete primary line
	From, To    time.Time
	Lines       int
}

// LogSourceInfo identifies where a log came from, for the picker and header.
type LogSourceInfo struct {
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

// LogSource abstracts local, gzip and server-side files so the parser only
// sees bytes.
type LogSource interface {
	Info() LogSourceInfo
	// ReadTail returns the last n bytes (n <= 0: whole file) aligned to the
	// first complete line, plus window metadata.
	ReadTail(ctx context.Context, n int64) ([]byte, LogWindow, error)
	// ReadFrom reads [off, EOF) for incremental refresh. Sources that cannot
	// seek (gzip) return ErrNotIncremental; the caller falls back to ReadTail.
	ReadFrom(ctx context.Context, off int64) ([]byte, error)
	// Cursor snapshots the file identity (inode, size) so the next refresh can
	// tell an append from a rotation. Sources without one return nil.
	Cursor(ctx context.Context) *LogCursor
}

// LogCursor is the incremental-refresh bookmark for a seekable source.
type LogCursor struct {
	Inode uint64
	Size  int64 // file size at the time of the read
	Off   int64 // first byte not yet parsed (start of the unterminated tail)
}

// LogCandidate is one file the picker offers.
type LogCandidate struct {
	Info   LogSourceInfo
	Reason string // how it was found: "--log-file", "pg_current_logfile", "/var/log/postgresql", "pg_ls_logdir"
	Open   func() LogSource
}

// LogSlowStats summarises the durations of one slow-query group.
type LogSlowStats struct {
	SumMs float64
	MaxMs float64
	P95Ms float64
	res   []float64 // bounded reservoir for the percentile
}

// AvgMs is the mean over count entries.
func (s *LogSlowStats) AvgMs(count int) float64 {
	if count == 0 {
		return 0
	}
	return s.SumMs / float64(count)
}

// LogCheckpointStats summarises "checkpoint complete" lines.
type LogCheckpointStats struct {
	Complete   int
	SumWrite   float64
	SumTotal   float64
	SumBuffers int64
	MaxTotal   float64
}

// LogTempStats summarises temp-file lines.
type LogTempStats struct {
	TotalBytes int64
	MaxBytes   int64
}

// LogGroup is one aggregated message: entries sharing a fingerprint.
type LogGroup struct {
	Key      string
	Title    string // normalized message or SQL, single line
	Category LogCategory
	Severity LogSeverity // highest seen
	Count    int
	First    time.Time
	Last     time.Time
	// Samples indexes into LogReport.Entries, oldest first, capped by the
	// aggregator so a 512 MiB window cannot pin every entry.
	Samples []int
	// Plans counts entries that carry an auto_explain plan.
	Plans int

	Slow       *LogSlowStats
	Checkpoint *LogCheckpointStats
	Temp       *LogTempStats
	Autovac    map[string]int // CatAutovacuum: per-table counts
}

// LogHistogram counts entries per time bucket and severity for the header
// sparklines.
type LogHistogram struct {
	Bucket time.Duration
	Start  time.Time
	Counts [][numLogSeverities]int
}

// Series returns the per-bucket count for one severity.
func (h *LogHistogram) Series(sev LogSeverity) []int {
	out := make([]int, len(h.Counts))
	for i := range h.Counts {
		out[i] = h.Counts[i][sev]
	}
	return out
}

// Total returns the per-bucket count over all severities.
func (h *LogHistogram) Total() []int {
	out := make([]int, len(h.Counts))
	for i := range h.Counts {
		for _, n := range h.Counts[i] {
			out[i] += n
		}
	}
	return out
}

// LogReport is the parsed + aggregated view of one window of one log file.
type LogReport struct {
	Source         LogSourceInfo
	Window         LogWindow
	Format         LogFormat
	Prefix         string // effective log_line_prefix
	PrefixDetected bool   // true when guessed from the file rather than taken from the server

	Buf     []byte // the window; every LogEntry byte slice points into it
	Entries []LogEntry
	Groups  []LogGroup // sorted by count desc, last-seen desc
	Hist    LogHistogram

	BySeverity [numLogSeverities]int
	ByCategory [numLogCategories]int
	Unparsed   int // lines that matched no prefix and had no open entry
	Cursor     *LogCursor

	// parser is retained so an incremental refresh can resume mid-entry.
	parser *LogParser
}
