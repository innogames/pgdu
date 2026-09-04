package tui

import (
	"strconv"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
	"pgdu/internal/pglog"
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

	// pooler-stats pane (logStatsColumnRegistry); it shares logColTime so
	// syncLogSort's time fallback works on both registries.
	logColXacts    logColID = "xacts"
	logColQueries  logColID = "queries"
	logColClParse  logColID = "cl_parse"
	logColSvParse  logColID = "sv_parse"
	logColBinds    logColID = "binds"
	logColIn       logColID = "in"
	logColOut      logColID = "out"
	logColXactTime logColID = "xact"
	logColQryTime  logColID = "query"
	logColWait     logColID = "wait"
)

// logCtx carries per-build inputs: whether the window spans more than a day
// (then the time column carries the date) and the reverse-DNS cache for the
// hostname column (nil until the first lookups return).
type logCtx struct {
	multiDay bool
	hosts    map[string]string
}

// logColDesc is a log-table column. Two registries share the id space: the
// timeline/slow panes render logColumnRegistry, the pooler-stats pane its own;
// logSpec picks the spec and Model.logTableFor the state for a pane.
type logColDesc = colDesc[logColID, *pglog.Entry, logCtx]

var (
	logTimelineSpec = colSpec[logColID, *pglog.Entry, logCtx]{
		registry:    logColumnRegistry,
		prefsKey:    colPrefsLogs,
		defaultSort: logColTime,
		title:       "choose which columns the log timeline shows",
	}
	logStatsSpec = colSpec[logColID, *pglog.Entry, logCtx]{
		registry:    logStatsColumnRegistry,
		prefsKey:    colPrefsLogStats,
		defaultSort: logColTime,
		title:       "choose which columns the pooler stats pane shows",
	}
)

// logSpec picks the column spec a pane renders with.
func logSpec(v logView) colSpec[logColID, *pglog.Entry, logCtx] {
	if v == logViewStats {
		return logStatsSpec
	}
	return logTimelineSpec
}

// logTableFor is the picker state a pane uses: the timeline and slow panes
// share one visibility set, the stats pane has its own.
func (m *Model) logTableFor(v logView) *colTable[logColID] {
	if v == logViewStats {
		return &m.logStatsTable
	}
	return &m.logTable
}

// logColsVisibleFor returns a pane's (lazily seeded) visibility map.
func (m *Model) logColsVisibleFor(v logView) map[logColID]bool {
	t := m.logTableFor(v)
	logSpec(v).ensureInit(t)
	return t.visible
}

// syncLogSort maps the pane's remembered sort column onto the projected set.
// The timeline falls back to time descending (newest first) — the natural
// order for a log tail; the slow pane to duration descending (slowest first),
// or time when the dur column is hidden.
func (m *Model) syncLogSort(s *screen, descs []logColDesc) {
	id := m.logSortCol(s.log.view)
	if i := indexOfCol(descs, *id); i >= 0 {
		s.diagSortCol = i
		return
	}
	*id = logColTime
	if s.log.view == logViewSlow && indexOfCol(descs, logColDuration) >= 0 {
		*id = logColDuration
	}
	s.diagSortCol = max(indexOfCol(descs, *id), 0)
	s.sortDesc = true
}

// logSortCol is the remembered sort column of a table pane; each pane keeps
// its own so tabbing between them restores the order the user chose there. The
// slow pane shares the timeline's visibility set but not its sort.
func (m *Model) logSortCol(v logView) *logColID {
	switch v {
	case logViewSlow:
		return &m.logSlowSortColID
	case logViewStats:
		return &m.logStatsTable.sortColID
	}
	return &m.logTable.sortColID
}

func text(b []byte) pg.DiagCell { return pg.DiagCell{Display: string(b)} }

// logColumnRegistry is the timeline's column set in display order; the wide
// message text is last so the no-bar last-column grow lands on it.
func logColumnRegistry() []logColDesc {
	return []logColDesc{
		{id: logColTime, name: "time", kind: pg.DiagText, defaultOn: true, mandatory: true,
			desc: "log timestamp (date shown when the window spans more than a day)",
			cell: func(e *pglog.Entry, ctx logCtx) pg.DiagCell {
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
			cell: func(e *pglog.Entry, _ logCtx) pg.DiagCell {
				return pg.DiagCell{Display: e.Severity.String(), Num: float64(e.Severity), HasNum: true}
			}},
		{id: logColCategory, name: "cat", kind: pg.DiagText, defaultOn: true,
			desc: "analyzer category (error, slow, ckpt, tmpfile, autovac, conn, lock, repl, other)",
			cell: func(e *pglog.Entry, _ logCtx) pg.DiagCell { return pg.DiagCell{Display: e.Category.Short()} }},
		{id: logColPID, name: "pid", kind: pg.DiagInt, defaultOn: true,
			desc: "backend process id (%p)",
			cell: func(e *pglog.Entry, _ logCtx) pg.DiagCell {
				if e.PID == 0 {
					return pg.DiagCell{}
				}
				return pg.DiagCell{Display: strconv.Itoa(int(e.PID)), Num: float64(e.PID), HasNum: true}
			}},
		{id: logColUser, name: "user", kind: pg.DiagText, defaultOn: true,
			desc: "session user (%u) — empty for background processes",
			cell: func(e *pglog.Entry, _ logCtx) pg.DiagCell { return text(e.User) }},
		{id: logColDB, name: "db", kind: pg.DiagText,
			desc: "database (%d) — only when the prefix carries it",
			cell: func(e *pglog.Entry, _ logCtx) pg.DiagCell { return text(e.DB) }},
		{id: logColHost, name: "host", kind: pg.DiagText,
			desc: "raw client address (%h / %r)",
			cell: func(e *pglog.Entry, _ logCtx) pg.DiagCell { return text(e.Host) }},
		{id: logColHostname, name: "hostname", kind: pg.DiagText, defaultOn: true,
			desc: "client host resolved via reverse DNS (raw address until resolved; cached per session)",
			cell: func(e *pglog.Entry, ctx logCtx) pg.DiagCell {
				h := string(e.Host)
				if r, ok := ctx.hosts[h]; ok && r != "" {
					return pg.DiagCell{Display: r}
				}
				return pg.DiagCell{Display: h}
			}},
		{id: logColApp, name: "app", kind: pg.DiagText,
			desc: "application_name (%a)",
			cell: func(e *pglog.Entry, _ logCtx) pg.DiagCell { return text(e.App) }},
		{id: logColSQLState, name: "sqlstate", kind: pg.DiagText,
			desc: "SQLSTATE code (%e, csvlog/jsonlog)",
			cell: func(e *pglog.Entry, _ logCtx) pg.DiagCell { return text(e.SQLState) }},
		{id: logColDuration, name: "dur", kind: pg.DiagDuration, defaultOn: true,
			desc: "statement duration (slow queries), lock wait time (lock lines), total time of a completed checkpoint",
			cell: func(e *pglog.Entry, _ logCtx) pg.DiagCell {
				switch {
				case e.Category == pglog.CatSlowQuery:
					return pg.DiagCell{Display: fmtAge(e.DurationMs), Num: e.DurationMs, HasNum: true}
				case e.LockWaitMs > 0:
					return pg.DiagCell{Display: fmtAge(e.LockWaitMs), Num: e.LockWaitMs, HasNum: true}
				case e.Checkpoint != nil && !e.Checkpoint.Starting && e.Checkpoint.TotalSec > 0:
					ms := e.Checkpoint.TotalSec * 1000
					return pg.DiagCell{Display: fmtAge(ms), Num: ms, HasNum: true}
				}
				return pg.DiagCell{}
			}},
		{id: logColMessage, name: "message", kind: pg.DiagText, defaultOn: true, mandatory: true,
			desc: "first line of the message (slow queries / statements: the SQL text; ▤ = an auto_explain plan is attached)",
			cell: func(e *pglog.Entry, _ logCtx) pg.DiagCell {
				if len(e.SQL) > 0 {
					msg := collapseWS(string(e.SQL), 300)
					if len(e.Plan) > 0 {
						msg = "▤ " + msg
					}
					return pg.DiagCell{Display: msg}
				}
				return pg.DiagCell{Display: e.FirstLine()}
			}},
	}
}

// logStatsColumnRegistry is the pooler-stats pane's column set: one column per
// figure of pgbouncer's "stats:" line. Every metric is DiagCostGraded — coloured
// relative to the column's max over the window — so the busiest minutes stand
// out while scrolling; absolute duration bands would paint every sub-second
// value the same green.
func logStatsColumnRegistry() []logColDesc {
	timeCol := logColumnRegistry()[0]
	rate := func(id logColID, name, desc string, get func(*pglog.PgBouncerStats) int64) logColDesc {
		return logColDesc{id: id, name: name, kind: pg.DiagCostGraded, defaultOn: true, desc: desc,
			cell: func(e *pglog.Entry, _ logCtx) pg.DiagCell {
				if e.PoolerStats == nil {
					return pg.DiagCell{}
				}
				n := get(e.PoolerStats)
				return pg.DiagCell{Display: strconv.FormatInt(n, 10), Num: float64(n), HasNum: true}
			}}
	}
	bps := func(id logColID, name, desc string, get func(*pglog.PgBouncerStats) int64) logColDesc {
		return logColDesc{id: id, name: name, kind: pg.DiagCostGraded, defaultOn: true, desc: desc,
			cell: func(e *pglog.Entry, _ logCtx) pg.DiagCell {
				if e.PoolerStats == nil {
					return pg.DiagCell{}
				}
				n := get(e.PoolerStats)
				return pg.DiagCell{Display: humanize.Bytes(n) + "/s", Num: float64(n), HasNum: true}
			}}
	}
	// Durations carry Num in ms like every other duration cell; pgbouncer logs µs.
	dur := func(id logColID, name, desc string, get func(*pglog.PgBouncerStats) int64) logColDesc {
		return logColDesc{id: id, name: name, kind: pg.DiagCostGraded, defaultOn: true, desc: desc,
			cell: func(e *pglog.Entry, _ logCtx) pg.DiagCell {
				if e.PoolerStats == nil {
					return pg.DiagCell{}
				}
				us := get(e.PoolerStats)
				return pg.DiagCell{Display: fmtMicros(us), Num: float64(us) / 1000, HasNum: true}
			}}
	}
	return []logColDesc{
		timeCol,
		rate(logColXacts, "xacts/s", "transactions per second (average over stats_period)", func(s *pglog.PgBouncerStats) int64 { return s.XactsPerSec }),
		rate(logColQueries, "queries/s", "queries per second", func(s *pglog.PgBouncerStats) int64 { return s.QueriesPerSec }),
		rate(logColClParse, "cl_parse/s", "client-side prepared-statement parses per second (pgbouncer ≥ 1.18)", func(s *pglog.PgBouncerStats) int64 { return s.ClientParsesPerSec }),
		rate(logColSvParse, "sv_parse/s", "server-side prepared-statement parses per second (pgbouncer ≥ 1.18)", func(s *pglog.PgBouncerStats) int64 { return s.ServerParsesPerSec }),
		rate(logColBinds, "binds/s", "prepared-statement binds per second (pgbouncer ≥ 1.18)", func(s *pglog.PgBouncerStats) int64 { return s.BindsPerSec }),
		bps(logColIn, "in/s", "bytes received from clients per second", func(s *pglog.PgBouncerStats) int64 { return s.InBytesPerSec }),
		bps(logColOut, "out/s", "bytes sent to clients per second", func(s *pglog.PgBouncerStats) int64 { return s.OutBytesPerSec }),
		dur(logColXactTime, "xact", "mean transaction duration", func(s *pglog.PgBouncerStats) int64 { return s.XactUs }),
		dur(logColQryTime, "query", "mean query duration", func(s *pglog.PgBouncerStats) int64 { return s.QueryUs }),
		dur(logColWait, "wait", "mean time clients spent waiting for a server connection", func(s *pglog.PgBouncerStats) int64 { return s.WaitUs }),
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
