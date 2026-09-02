package tui

import (
	"slices"
	"strconv"

	"pgdu/internal/pg"
)

// logColID is the stable identity of a log-timeline column (C picker key and
// sort-column memory across rebuilds), mirroring actColID.
type logColID string

const (
	logColTime     logColID = "time"
	logColSeverity logColID = "sev"
	logColCategory logColID = "cat"
	logColPID      logColID = "pid"
	logColUser     logColID = "user"
	logColDB       logColID = "db"
	logColHost     logColID = "host"
	logColHostname logColID = "hostname"
	logColApp      logColID = "app"
	logColSQLState logColID = "sqlstate"
	logColDuration logColID = "dur"
	logColMessage  logColID = "message"
)

// logCtx carries per-build inputs: whether the window spans more than a day
// (then the time column carries the date) and the reverse-DNS cache for the
// hostname column (nil until the first lookups return).
type logCtx struct {
	multiDay bool
	hosts    map[string]string
}

type logColDesc struct {
	id        logColID
	name      string
	kind      pg.DiagColumnKind
	defaultOn bool
	mandatory bool
	desc      string
	cell      func(*pg.LogEntry, logCtx) pg.DiagCell
}

func text(b []byte) pg.DiagCell { return pg.DiagCell{Display: string(b)} }

// logColumnRegistry is the timeline's column set in display order; the wide
// message text is last so the no-bar last-column grow lands on it.
func logColumnRegistry() []logColDesc {
	return []logColDesc{
		{id: logColTime, name: "time", kind: pg.DiagText, defaultOn: true, mandatory: true,
			desc: "log timestamp (date shown when the window spans more than a day)",
			cell: func(e *pg.LogEntry, ctx logCtx) pg.DiagCell {
				if e.Time.IsZero() {
					return pg.DiagCell{Display: "—"}
				}
				if ctx.multiDay {
					return pg.DiagCell{Display: e.Time.Format("01-02 15:04:05")}
				}
				return pg.DiagCell{Display: e.Time.Format("15:04:05")}
			}},
		{id: logColSeverity, name: "sev", kind: pg.DiagLogSeverity, defaultOn: true,
			desc: "message severity (LOG / WARNING / ERROR / FATAL / PANIC)",
			cell: func(e *pg.LogEntry, _ logCtx) pg.DiagCell {
				return pg.DiagCell{Display: e.Severity.String(), Num: float64(e.Severity), HasNum: true}
			}},
		{id: logColCategory, name: "cat", kind: pg.DiagText, defaultOn: true,
			desc: "analyzer category (error, slow, ckpt, tmpfile, autovac, conn, lock, repl, other)",
			cell: func(e *pg.LogEntry, _ logCtx) pg.DiagCell { return pg.DiagCell{Display: e.Category.Short()} }},
		{id: logColPID, name: "pid", kind: pg.DiagInt, defaultOn: true,
			desc: "backend process id (%p)",
			cell: func(e *pg.LogEntry, _ logCtx) pg.DiagCell {
				if e.PID == 0 {
					return pg.DiagCell{}
				}
				return pg.DiagCell{Display: strconv.Itoa(int(e.PID)), Num: float64(e.PID), HasNum: true}
			}},
		{id: logColUser, name: "user", kind: pg.DiagText, defaultOn: true,
			desc: "session user (%u) — empty for background processes",
			cell: func(e *pg.LogEntry, _ logCtx) pg.DiagCell { return text(e.User) }},
		{id: logColDB, name: "db", kind: pg.DiagText,
			desc: "database (%d) — only when the prefix carries it",
			cell: func(e *pg.LogEntry, _ logCtx) pg.DiagCell { return text(e.DB) }},
		{id: logColHost, name: "host", kind: pg.DiagText, defaultOn: true,
			desc: "client host (%h / %r)",
			cell: func(e *pg.LogEntry, _ logCtx) pg.DiagCell { return text(e.Host) }},
		{id: logColHostname, name: "hostname", kind: pg.DiagText,
			desc: "client host resolved via reverse DNS (falls back to the raw address; cached per session)",
			cell: func(e *pg.LogEntry, ctx logCtx) pg.DiagCell {
				h := string(e.Host)
				if r, ok := ctx.hosts[h]; ok && r != "" {
					return pg.DiagCell{Display: r}
				}
				return pg.DiagCell{Display: h}
			}},
		{id: logColApp, name: "app", kind: pg.DiagText,
			desc: "application_name (%a)",
			cell: func(e *pg.LogEntry, _ logCtx) pg.DiagCell { return text(e.App) }},
		{id: logColSQLState, name: "sqlstate", kind: pg.DiagText,
			desc: "SQLSTATE code (%e, csvlog/jsonlog)",
			cell: func(e *pg.LogEntry, _ logCtx) pg.DiagCell { return text(e.SQLState) }},
		{id: logColDuration, name: "dur", kind: pg.DiagDuration, defaultOn: true,
			desc: "statement duration for slow-query lines; lock wait time for lock lines",
			cell: func(e *pg.LogEntry, _ logCtx) pg.DiagCell {
				switch {
				case e.Category == pg.CatSlowQuery:
					return pg.DiagCell{Display: fmtAge(e.DurationMs), Num: e.DurationMs, HasNum: true}
				case e.LockWaitMs > 0:
					return pg.DiagCell{Display: fmtAge(e.LockWaitMs), Num: e.LockWaitMs, HasNum: true}
				}
				return pg.DiagCell{}
			}},
		{id: logColMessage, name: "message", kind: pg.DiagText, defaultOn: true, mandatory: true,
			desc: "first line of the message (slow queries: the statement text)",
			cell: func(e *pg.LogEntry, _ logCtx) pg.DiagCell {
				if e.Category == pg.CatSlowQuery && len(e.SQL) > 0 {
					return pg.DiagCell{Display: collapseWS(string(e.SQL), 300)}
				}
				return pg.DiagCell{Display: e.FirstLine()}
			}},
	}
}

// collapseWS flattens whitespace runs and clips to n bytes for a table cell.
func collapseWS(s string, n int) string {
	var b []byte
	space := false
	for i := 0; i < len(s) && len(b) < n; i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if !space && len(b) > 0 {
				b = append(b, ' ')
			}
			space = true
			continue
		}
		space = false
		b = append(b, c)
	}
	if len(b) >= n {
		return string(b) + "…"
	}
	return string(b)
}

func indexOfLogCol(descs []logColDesc, id logColID) int {
	return slices.IndexFunc(descs, func(d logColDesc) bool { return d.id == id })
}

func (m *Model) logColEnabled(id logColID, def bool) bool {
	if v, ok := m.logColsVisible[id]; ok {
		return v
	}
	return def
}

func (m *Model) ensureLogColsInit() {
	if m.logColsVisible != nil {
		return
	}
	m.logColsVisible = make(map[logColID]bool)
	for _, d := range logColumnRegistry() {
		m.logColsVisible[d.id] = d.defaultOn || d.mandatory
	}
}

func (m *Model) visibleLogCols() []logColDesc {
	var out []logColDesc
	for _, d := range logColumnRegistry() {
		if d.mandatory || m.logColEnabled(d.id, d.defaultOn) {
			out = append(out, d)
		}
	}
	return out
}

func logDiagColumnsFrom(descs []logColDesc) []pg.DiagColumn {
	cols := make([]pg.DiagColumn, len(descs))
	for i, d := range descs {
		cols[i] = pg.DiagColumn{Name: d.name, Kind: d.kind}
	}
	return cols
}

// syncLogSort maps the remembered sort column onto the projected set, falling
// back to time descending (newest first) — the natural order for a log tail.
func (m *Model) syncLogSort(s *screen, descs []logColDesc) {
	if i := indexOfLogCol(descs, m.logSortColID); i >= 0 {
		s.diagSortCol = i
		return
	}
	s.diagSortCol = max(indexOfLogCol(descs, logColTime), 0)
	s.sortDesc = true
	m.logSortColID = logColTime
}
