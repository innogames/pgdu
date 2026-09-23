package pglog

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// logField names the Entry text field a prefixed line (or its continuation
// lines) is written to.
type logField int

const (
	fieldMessage logField = iota
	fieldDetail
	fieldHint
	fieldStatement
	fieldContext
	fieldQuery
	fieldLocation
)

func (e *Entry) fieldPtr(f logField) *[]byte {
	switch f {
	case fieldDetail:
		return &e.Detail
	case fieldHint:
		return &e.Hint
	case fieldStatement:
		return &e.Statement
	case fieldContext:
		return &e.Context
	case fieldQuery:
		return &e.Query
	case fieldLocation:
		return &e.Location
	}
	return &e.Message
}

// Parser turns raw log bytes into LogEntries. It is resumable: a second
// Feed continues the entry left open by the first, which is how an incremental
// refresh re-parses from the start of the last (possibly unfinished) entry
// rather than re-reading the whole window.
type Parser struct {
	m      *prefixMatcher
	format Format
	loc    *time.Location

	entries []Entry
	// open is the entry receiving continuation lines, -1 when none; openField
	// is the field they extend and openBuf/openStart locate that field's start
	// so the extension stays a re-slice of the same buffer (zero-copy).
	open      int
	openField logField
	openBuf   []byte
	openStart int

	// lastPrimary maps a backend (pid, or %c when the prefix has it) to the
	// index of its most recent primary entry — where DETAIL/HINT/… attach.
	lastPrimary map[string]int

	lines    int
	unparsed int
}

// NewParser builds a parser for one file. m may be nil for csv/json.
func NewParser(format Format, m *prefixMatcher, loc *time.Location) *Parser {
	if loc == nil {
		loc = time.UTC
	}
	return &Parser{m: m, format: format, loc: loc, open: -1, lastPrimary: make(map[string]int)}
}

// Entries returns everything parsed so far.
func (p *Parser) Entries() []Entry { return p.entries }

// Lines is the number of physical lines fed.
func (p *Parser) Lines() int { return p.lines }

// Unparsed counts continuation-looking lines that had no entry to extend.
func (p *Parser) Unparsed() int { return p.unparsed }

// LastEntryOff returns the window offset of the last primary entry, or -1.
// An incremental refresh re-reads from here so a still-growing entry (its
// STATEMENT lines may not be flushed yet) is parsed whole.
func (p *Parser) LastEntryOff() int64 {
	if len(p.entries) == 0 {
		return -1
	}
	return p.entries[len(p.entries)-1].Off
}

// TruncateLast drops the last entry so the caller can re-feed it. Attachment
// bookkeeping pointing at it is cleared.
func (p *Parser) TruncateLast() {
	if len(p.entries) == 0 {
		return
	}
	last := len(p.entries) - 1
	for k, i := range p.lastPrimary {
		if i == last {
			delete(p.lastPrimary, k)
		}
	}
	p.entries = p.entries[:last]
	p.open = -1
}

// Feed parses buf, whose first byte sits at window offset base.
func (p *Parser) Feed(buf []byte, base int64) {
	switch p.format {
	case FormatCSV:
		p.feedCSV(buf, base)
	case FormatJSON:
		p.feedJSON(buf, base)
	default:
		p.feedStderr(buf, base)
	}
}

func (p *Parser) feedStderr(buf []byte, base int64) {
	off := 0
	for off < len(buf) {
		end := bytes.IndexByte(buf[off:], '\n')
		lineEnd := len(buf)
		next := len(buf)
		if end >= 0 {
			lineEnd = off + end
			next = lineEnd + 1
		}
		line := buf[off:lineEnd]
		p.lines++

		f, ok := p.m.Match(line)
		if !ok {
			// Continuation: extend the open field to the end of this line.
			if p.open >= 0 && sameBacking(p.openBuf, buf) {
				e := &p.entries[p.open]
				*e.fieldPtr(p.openField) = buf[p.openStart:lineEnd]
			} else if p.open >= 0 {
				// Continuation across a buffer seam (incremental refresh): copy.
				e := &p.entries[p.open]
				fp := e.fieldPtr(p.openField)
				joined := make([]byte, 0, len(*fp)+1+len(line))
				joined = append(joined, *fp...)
				joined = append(joined, '\n')
				joined = append(joined, line...)
				*fp = joined
				p.openBuf, p.openStart = nil, 0
			} else {
				p.unparsed++
			}
			off = next
			continue
		}

		restStart := lineEnd - len(f.rest)
		if fld, isAttach := attachmentField(f.tag); isAttach {
			idx := p.primaryFor(f)
			e := &p.entries[idx]
			*e.fieldPtr(fld) = f.rest
			p.open, p.openField, p.openBuf, p.openStart = idx, fld, buf, restStart
			off = next
			continue
		}

		e := Entry{
			Off:      base + int64(off),
			Time:     f.time,
			PID:      f.pid,
			Line:     f.line,
			Session:  f.session,
			User:     f.user,
			DB:       f.db,
			Host:     f.host,
			App:      f.app,
			SQLState: f.sqlstate,
			Severity: severityOf(f.tag),
			Message:  f.rest,
			QueryID:  f.queryID,
		}
		p.entries = append(p.entries, e)
		idx := len(p.entries) - 1
		if key := p.sessionKey(f); key != "" {
			p.lastPrimary[key] = idx
		}
		p.open, p.openField, p.openBuf, p.openStart = idx, fieldMessage, buf, restStart
		off = next
	}
}

// sameBacking reports whether a and b are the same buffer — i.e. the open
// field can be extended by re-slicing b. Feed always records the whole chunk
// as openBuf, so comparing the first element is exact.
func sameBacking(a, b []byte) bool {
	return len(a) > 0 && len(b) > 0 && &a[0] == &b[0]
}

func (p *Parser) sessionKey(f prefixFields) string {
	if len(f.session) > 0 {
		return string(f.session)
	}
	if f.pid > 0 {
		return strconv.Itoa(int(f.pid))
	}
	return ""
}

// primaryFor finds the entry an attachment line belongs to: the backend's
// last primary, provided the session line number (when present) is later. A
// missing primary — cut off by the window head — gets a synthetic orphan so the
// detail is not lost.
func (p *Parser) primaryFor(f prefixFields) int {
	key := p.sessionKey(f)
	if key != "" {
		if idx, ok := p.lastPrimary[key]; ok {
			e := &p.entries[idx]
			if f.line == 0 || e.Line == 0 || f.line > e.Line {
				return idx
			}
		}
	} else if p.open >= 0 {
		// No pid in the prefix: PostgreSQL emits DETAIL right after its
		// primary, so the open entry is the best available guess.
		return p.open
	}
	p.entries = append(p.entries, Entry{
		Time: f.time, PID: f.pid, Line: f.line, Session: f.session,
		User: f.user, DB: f.db, Host: f.host, App: f.app,
		Severity: SevLog, Category: CatOther, Orphan: true,
	})
	idx := len(p.entries) - 1
	if key != "" {
		p.lastPrimary[key] = idx
	}
	return idx
}

func attachmentField(tag []byte) (logField, bool) {
	switch string(tag) {
	case "DETAIL":
		return fieldDetail, true
	case "HINT":
		return fieldHint, true
	case "STATEMENT":
		return fieldStatement, true
	case "CONTEXT":
		return fieldContext, true
	case "QUERY":
		return fieldQuery, true
	case "LOCATION":
		return fieldLocation, true
	}
	return fieldMessage, false
}

func severityOf(tag []byte) Severity {
	switch string(tag) {
	case "ERROR":
		return SevError
	case "FATAL":
		return SevFatal
	case "PANIC":
		return SevPanic
	case "WARNING":
		return SevWarning
	case "NOTICE":
		return SevNotice
	case "INFO":
		return SevInfo
	case "LOG":
		return SevLog
	case "NOISE": // pgbouncer's below-debug level
		return SevDebug
	}
	if bytes.HasPrefix(tag, []byte("DEBUG")) {
		return SevDebug
	}
	return SevLog
}

// ── csvlog / jsonlog ─────────────────────────────────────────────────────────

// csvlog column positions (PG13+ appended backend_type; PG14+ leader_pid and
// query_id). Older files are shorter; the reader tolerates that.
const (
	csvLogTime = iota
	csvUser
	csvDB
	csvPID
	csvConnFrom
	csvSession
	csvLineNum
	csvCmdTag
	csvSessStart
	csvVXID
	csvXID
	csvSeverity
	csvSQLState
	csvMessage
	csvDetail
	csvHint
	csvInternalQuery
	csvInternalPos
	csvContext
	csvQuery
	csvQueryPos
	csvLocation
	csvAppName
	csvBackendType
	csvLeaderPID
	csvQueryID
)

const csvMinFields = csvLocation + 1

const csvTimeLayout = "2006-01-02 15:04:05.000 MST"

func (p *Parser) feedCSV(buf []byte, base int64) {
	r := csv.NewReader(bytes.NewReader(buf))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.ReuseRecord = false
	for {
		off := r.InputOffset()
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		p.lines++
		if err != nil || len(rec) < csvMinFields {
			// A window that starts inside a quoted multi-line field yields a
			// garbage record or two before the first clean one.
			p.unparsed++
			if err != nil && !isCSVParseErr(err) {
				break
			}
			continue
		}
		ts, terr := time.ParseInLocation(csvTimeLayout, rec[csvLogTime], p.loc)
		if terr != nil {
			p.unparsed++
			continue
		}
		e := Entry{
			Off:       base + off,
			Time:      ts,
			User:      []byte(rec[csvUser]),
			DB:        []byte(rec[csvDB]),
			Host:      []byte(rec[csvConnFrom]),
			Session:   []byte(rec[csvSession]),
			Severity:  severityOf([]byte(rec[csvSeverity])),
			SQLState:  []byte(rec[csvSQLState]),
			Message:   []byte(rec[csvMessage]),
			Detail:    []byte(rec[csvDetail]),
			Hint:      []byte(rec[csvHint]),
			Context:   []byte(rec[csvContext]),
			Statement: []byte(rec[csvQuery]),
			Location:  []byte(rec[csvLocation]),
		}
		if len(rec) > csvAppName {
			e.App = []byte(rec[csvAppName])
		}
		if len(rec) > csvQueryID {
			e.QueryID, _ = strconv.ParseInt(rec[csvQueryID], 10, 64)
		}
		if v, err := strconv.Atoi(rec[csvPID]); err == nil {
			e.PID = int32(v)
		}
		if v, err := strconv.Atoi(rec[csvLineNum]); err == nil {
			e.Line = int32(v)
		}
		p.entries = append(p.entries, e)
	}
}

func isCSVParseErr(err error) bool {
	var perr *csv.ParseError
	return errors.As(err, &perr)
}

// jsonLogLine mirrors the jsonlog record (PG15+). Only the fields the
// analyzer uses are declared; the rest is ignored by encoding/json.
type jsonLogLine struct {
	Timestamp string `json:"timestamp"`
	User      string `json:"user"`
	DBName    string `json:"dbname"`
	PID       int32  `json:"pid"`
	Host      string `json:"remote_host"`
	Session   string `json:"session_id"`
	LineNum   int32  `json:"line_num"`
	Severity  string `json:"error_severity"`
	SQLState  string `json:"state_code"`
	Message   string `json:"message"`
	Detail    string `json:"detail"`
	Hint      string `json:"hint"`
	Context   string `json:"context"`
	Statement string `json:"statement"`
	AppName   string `json:"application_name"`
	FuncName  string `json:"func_name"`
	QueryID   int64  `json:"query_id"`
}

func (p *Parser) feedJSON(buf []byte, base int64) {
	off := 0
	for off < len(buf) {
		end := bytes.IndexByte(buf[off:], '\n')
		lineEnd := len(buf)
		next := len(buf)
		if end >= 0 {
			lineEnd = off + end
			next = lineEnd + 1
		}
		line := bytes.TrimSpace(buf[off:lineEnd])
		p.lines++
		if len(line) == 0 {
			off = next
			continue
		}
		var j jsonLogLine
		if err := json.Unmarshal(line, &j); err != nil {
			p.unparsed++
			off = next
			continue
		}
		ts, _ := time.ParseInLocation(csvTimeLayout, j.Timestamp, p.loc)
		p.entries = append(p.entries, Entry{
			Off:       base + int64(off),
			Time:      ts,
			User:      []byte(j.User),
			DB:        []byte(j.DBName),
			PID:       j.PID,
			Host:      []byte(j.Host),
			Session:   []byte(j.Session),
			Line:      j.LineNum,
			Severity:  severityOf([]byte(j.Severity)),
			SQLState:  []byte(j.SQLState),
			Message:   []byte(j.Message),
			Detail:    []byte(j.Detail),
			Hint:      []byte(j.Hint),
			Context:   []byte(j.Context),
			Statement: []byte(j.Statement),
			Location:  []byte(j.FuncName),
			App:       []byte(j.AppName),
			QueryID:   j.QueryID,
		})
		off = next
	}
}

// DetectFormat sniffs the first complete line of a window.
func DetectFormat(buf []byte) Format {
	if lines := sampleLines(buf, 1); len(lines) > 0 {
		switch ln := lines[0]; {
		case ln[0] == '{':
			return FormatJSON
		case csvHeadRe.Match(ln):
			return FormatCSV
		case pgbHeadRe.Match(ln):
			return FormatPgBouncer
		}
	}
	return FormatStderr
}

var csvHeadRe = regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d\.\d{3} [A-Z0-9+\-]{1,6},`)

// ── classification ───────────────────────────────────────────────────────────

var (
	checkpointCompleteRe = regexp.MustCompile(`wrote (\d+) buffers \(([\d.]+)%\); (\d+) WAL file\(s\) added, (\d+) removed, (\d+) recycled; write=([\d.]+) s, sync=([\d.]+) s, total=([\d.]+) s(?:.*?distance=(\d+) kB, estimate=(\d+) kB)?`)
	tempFileRe           = regexp.MustCompile(`size (\d+)\s*$`)
	lockWaitRe           = regexp.MustCompile(`^process \d+ (?:still waiting for|acquired) .* after ([\d.]+) ms`)
)

// Classify assigns every entry a category and extracts the per-category
// fields. It runs once per entry after parsing and is safe to re-run.
func Classify(entries []Entry) {
	for i := range entries {
		classify(&entries[i])
	}
}

func classify(e *Entry) {
	if e.Orphan {
		e.Category = CatOther
		return
	}
	msg := firstLineBytes(e.Message)
	switch {
	case bytes.HasPrefix(msg, []byte("deadlock detected")):
		e.Category = CatLock
	case e.Severity >= SevError:
		e.Category = CatError
	case e.Severity == SevWarning:
		e.Category = CatWarning
	case bytes.HasPrefix(msg, []byte("duration: ")):
		e.Category = CatSlowQuery
		parseDuration(e)
	case bytes.HasPrefix(msg, []byte("statement: ")), bytes.HasPrefix(msg, []byte("execute ")):
		// log_statement output — the same shapes as after "duration: N ms",
		// just without the timing.
		if sql, ok := statementSQL(e.Message); ok {
			e.Category = CatStatement
			e.SQL = sql
		} else {
			e.Category = CatOther
		}
	case bytes.HasPrefix(msg, []byte("checkpoint ")) || bytes.HasPrefix(msg, []byte("restartpoint ")):
		e.Category = CatCheckpoint
		parseCheckpoint(e, msg)
	case bytes.HasPrefix(msg, []byte("temporary file: path ")):
		e.Category = CatTempFile
		if m := tempFileRe.FindSubmatch(msg); m != nil {
			e.TempBytes, _ = strconv.ParseInt(string(m[1]), 10, 64)
		}
	case bytes.HasPrefix(msg, []byte("automatic vacuum of table ")),
		bytes.HasPrefix(msg, []byte("automatic analyze of table ")),
		bytes.HasPrefix(msg, []byte("automatic aggressive vacuum of table ")),
		bytes.HasPrefix(msg, []byte("automatic aggressive vacuum to prevent wraparound of table ")),
		bytes.HasPrefix(msg, []byte("automatic vacuum to prevent wraparound of table ")):
		e.Category = CatAutovacuum
		e.AVTable = firstQuoted(msg)
	case bytes.HasPrefix(msg, []byte("connection received: ")),
		bytes.HasPrefix(msg, []byte("connection authorized: ")),
		bytes.HasPrefix(msg, []byte("connection authenticated: ")),
		bytes.HasPrefix(msg, []byte("disconnection: ")):
		e.Category = CatConnection
	case bytes.HasPrefix(msg, []byte("process ")) && lockWaitRe.Match(msg):
		e.Category = CatLock
		if m := lockWaitRe.FindSubmatch(msg); m != nil {
			e.LockWaitMs, _ = strconv.ParseFloat(string(m[1]), 64)
		}
	case bytes.HasPrefix(msg, []byte("stats: ")) && parsePgBouncerStats(e, msg):
		// pgbouncer's periodic stats line: parsed into PoolerStats, category
		// stays other so it never masquerades as a connection event.
		e.Category = CatOther
	case isPgBouncerSocketMsg(msg):
		// pgbouncer's per-socket lines (login attempt, new connection to server,
		// closing because: …) are connection lifecycle. Checked before the
		// replication markers: "login attempt … replication=no" contains the
		// bare word "replication".
		e.Category = CatConnection
	case isReplicationMsg(msg):
		e.Category = CatReplication
	default:
		e.Category = CatOther
	}
}

// parsePgBouncerStats reads pgbouncer's "stats: 90 xacts/s, 1357 queries/s,
// 0 client parses/s, 0 server parses/s, 0 binds/s, in 399055 B/s, out 1253187
// B/s, xact 17861 us, query 242 us, wait 0 us" line. Fields are matched by
// name, not position, so the shorter pre-1.18 line (no parses/binds) parses
// too. Reports false unless at least the xacts and queries rates were found.
func parsePgBouncerStats(e *Entry, msg []byte) bool {
	st := &PgBouncerStats{}
	var gotXacts, gotQueries bool
	for tok := range bytes.SplitSeq(msg[len("stats: "):], []byte(", ")) {
		f := bytes.Fields(tok)
		switch {
		case len(f) == 2 && bytes.HasSuffix(f[1], []byte("/s")):
			// "<n> xacts/s" · "<n> queries/s" · "<n> binds/s"
			n, err := strconv.ParseInt(string(f[0]), 10, 64)
			if err != nil {
				return false
			}
			switch string(f[1]) {
			case "xacts/s":
				st.XactsPerSec, gotXacts = n, true
			case "queries/s":
				st.QueriesPerSec, gotQueries = n, true
			case "binds/s":
				st.BindsPerSec = n
			}
		case len(f) == 3 && bytes.Equal(f[2], []byte("parses/s")):
			// "<n> client parses/s" · "<n> server parses/s"
			n, err := strconv.ParseInt(string(f[0]), 10, 64)
			if err != nil {
				return false
			}
			if string(f[1]) == "client" {
				st.ClientParsesPerSec = n
			} else {
				st.ServerParsesPerSec = n
			}
		case len(f) == 3 && bytes.Equal(f[2], []byte("B/s")):
			// "in <n> B/s" · "out <n> B/s"
			n, err := strconv.ParseInt(string(f[1]), 10, 64)
			if err != nil {
				return false
			}
			if string(f[0]) == "in" {
				st.InBytesPerSec = n
			} else {
				st.OutBytesPerSec = n
			}
		case len(f) == 3 && bytes.Equal(f[2], []byte("us")):
			// "xact <n> us" · "query <n> us" · "wait <n> us"
			n, err := strconv.ParseInt(string(f[1]), 10, 64)
			if err != nil {
				return false
			}
			switch string(f[0]) {
			case "xact":
				st.XactUs = n
			case "query":
				st.QueryUs = n
			case "wait":
				st.WaitUs = n
			}
		default:
			return false
		}
	}
	if !gotXacts || !gotQueries {
		return false
	}
	e.PoolerStats = st
	return true
}

// parseDuration handles "duration: 248.569 ms  execute <unnamed>/C_15: SQL",
// "duration: 1.2 ms  statement: SQL" and the bare log_duration form.
func parseDuration(e *Entry) {
	rest := e.Message[len("duration: "):]
	i := bytes.Index(rest, []byte(" ms"))
	if i < 0 {
		return
	}
	e.DurationMs, _ = strconv.ParseFloat(string(rest[:i]), 64)
	rest = rest[i+len(" ms"):]
	rest = bytes.TrimLeft(rest, " ")
	// auto_explain: "duration: N ms  plan:" followed by an indented "Query
	// Text: …" line and the plan tree.
	if bytes.HasPrefix(rest, []byte("plan:")) {
		e.SQL, e.Plan = splitAutoExplain(rest[len("plan:"):])
		return
	}
	e.SQL, _ = statementSQL(rest)
}

// statementSQL strips the "statement: " / "execute <name>: " / "parse <name>: "
// / "bind <name>: " lead-in and returns the SQL that follows; ok is false when
// b doesn't start with one of those kinds.
func statementSQL(b []byte) (sql []byte, ok bool) {
	for _, kind := range [][]byte{[]byte("statement: "), []byte("execute "), []byte("parse "), []byte("bind ")} {
		if !bytes.HasPrefix(b, kind) {
			continue
		}
		if string(kind) == "statement: " {
			return b[len(kind):], true
		}
		if _, after, ok := bytes.Cut(b, []byte(": ")); ok {
			return after, true
		} else if _, after, ok := bytes.Cut(b, []byte{':'}); ok {
			return bytes.TrimLeft(after, " "), true
		}
		return nil, true
	}
	return nil, false
}

// splitAutoExplain separates auto_explain's "Query Text: …" (possibly
// multi-line) from the plan tree that follows it. The plan's root node is the
// first line carrying a cost or actual-rows annotation; with both switched off
// the whole remainder counts as plan and the query text is its first line.
func splitAutoExplain(body []byte) (sql, plan []byte) {
	const marker = "Query Text: "
	_, after, ok := bytes.Cut(body, []byte(marker))
	if !ok {
		return nil, bytes.TrimSpace(body)
	}
	rest := after
	off := 0
	first := true
	for off < len(rest) {
		end := bytes.IndexByte(rest[off:], '\n')
		lineEnd := len(rest)
		if end >= 0 {
			lineEnd = off + end
		}
		line := rest[off:lineEnd]
		if !first && (bytes.Contains(line, []byte("(cost=")) || bytes.Contains(line, []byte("(actual "))) {
			return bytes.TrimSpace(rest[:off]), dedentBytes(bytes.TrimRight(rest[off:], "\n\t "))
		}
		first = false
		if end < 0 {
			break
		}
		off = lineEnd + 1
	}
	return bytes.TrimSpace(rest), nil
}

// dedentBytes strips the indentation the log continuation adds to every plan
// line (the smallest common run of leading blanks), keeping the tree's own
// relative indentation. It copies, so the result is independent of the window.
func dedentBytes(b []byte) []byte {
	lines := bytes.Split(b, []byte("\n"))
	common := -1
	for _, l := range lines {
		if len(bytes.TrimSpace(l)) == 0 {
			continue
		}
		n := 0
		for n < len(l) && (l[n] == ' ' || l[n] == '\t') {
			n++
		}
		if common < 0 || n < common {
			common = n
		}
	}
	if common <= 0 {
		return b
	}
	out := make([]byte, 0, len(b))
	for i, l := range lines {
		if i > 0 {
			out = append(out, '\n')
		}
		if len(l) >= common {
			l = l[common:]
		}
		out = append(out, l...)
	}
	return out
}

// MergePlans folds each auto_explain "plan:" entry into the "statement:" entry
// PostgreSQL logs right after it for the same execution (same backend, same
// query text, logged within moments), so one slow statement is counted once
// and carries its plan. A plan with no matching statement line — auto_explain
// on, log_min_duration_statement off — stays a slow-query entry of its own,
// grouped by its query text.
func MergePlans(entries []Entry) []Entry {
	const lookahead = 8
	drop := make([]bool, len(entries))
	for i := range entries {
		e := &entries[i]
		if e.Category != CatSlowQuery || len(e.Plan) == 0 || len(e.SQL) == 0 {
			continue
		}
		want := NormalizeSQL(string(e.SQL))
		for j := i + 1; j < len(entries) && j <= i+lookahead; j++ {
			c := &entries[j]
			if c.PID != e.PID || c.Category != CatSlowQuery || len(c.Plan) > 0 || len(c.SQL) == 0 {
				continue
			}
			if !c.Time.IsZero() && c.Time.Sub(e.Time) > 5*time.Second {
				break
			}
			if NormalizeSQL(string(c.SQL)) == want {
				c.Plan = e.Plan
				drop[i] = true
				break
			}
		}
	}
	out := make([]Entry, 0, len(entries))
	for i := range entries {
		if !drop[i] {
			out = append(out, entries[i])
		}
	}
	return out
}

func parseCheckpoint(e *Entry, msg []byte) {
	cf := &CheckpointFields{}
	e.Checkpoint = cf
	s := string(msg)
	if _, after, ok := strings.Cut(s, " starting: "); ok {
		cf.Starting = true
		cf.Reason = after
		return
	}
	m := checkpointCompleteRe.FindStringSubmatch(s)
	if m == nil {
		return
	}
	cf.Buffers, _ = strconv.ParseInt(m[1], 10, 64)
	cf.BuffersPct, _ = strconv.ParseFloat(m[2], 64)
	cf.WALAdded, _ = strconv.ParseInt(m[3], 10, 64)
	cf.WALRemoved, _ = strconv.ParseInt(m[4], 10, 64)
	cf.WALRecycle, _ = strconv.ParseInt(m[5], 10, 64)
	cf.WriteSec, _ = strconv.ParseFloat(m[6], 64)
	cf.SyncSec, _ = strconv.ParseFloat(m[7], 64)
	cf.TotalSec, _ = strconv.ParseFloat(m[8], 64)
	if m[9] != "" {
		cf.DistanceKB, _ = strconv.ParseInt(m[9], 10, 64)
		cf.EstimateKB, _ = strconv.ParseInt(m[10], 10, 64)
	}
}

var replicationMarkers = []string{
	"started streaming WAL", "redo ", "recovery ", "standby ", "consistent recovery state",
	"database system is ready", "database system was", "database system is shut",
	"replication", "wal receiver", "walreceiver", "archive ", "restored log file",
	"entering standby mode", "invalid record length", "starting PostgreSQL",
	"received ", "shutting down", "checkpointer process", "background writer",
}

func isReplicationMsg(msg []byte) bool {
	s := string(msg)
	for _, m := range replicationMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// isPgBouncerSocketMsg recognises pgbouncer's "C-0x…: db/user@host:port …"
// (client) and "S-0x…: …" (server) socket-tagged messages.
func isPgBouncerSocketMsg(msg []byte) bool {
	return (bytes.HasPrefix(msg, []byte("C-0x")) || bytes.HasPrefix(msg, []byte("S-0x"))) &&
		bytes.Contains(msg, []byte(": "))
}

// pgBouncerSocketTail splits a socket-tagged pgbouncer message into its side
// ("client"/"server") and the event text after the "db/user@host:port " head,
// e.g. "closing because: client close request (age=12s)". ok is false for
// anything else.
func pgBouncerSocketTail(msg string) (side, tail string, ok bool) {
	if !isPgBouncerSocketMsg([]byte(msg)) {
		return "", "", false
	}
	side = "client"
	if msg[0] == 'S' {
		side = "server"
	}
	_, rest, _ := strings.Cut(msg, ": ")
	// rest = "db/user@host:port event…"; the socket address never contains a
	// space, the event always follows one.
	if _, after, found := strings.Cut(rest, " "); found {
		return side, after, true
	}
	return side, rest, true
}

func firstQuoted(b []byte) []byte {
	i := bytes.IndexByte(b, '"')
	if i < 0 {
		return nil
	}
	j := bytes.IndexByte(b[i+1:], '"')
	if j < 0 {
		return nil
	}
	return b[i+1 : i+1+j]
}

func firstLineBytes(b []byte) []byte {
	if before, _, ok := bytes.Cut(b, []byte{'\n'}); ok {
		return before
	}
	return b
}
