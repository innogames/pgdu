package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
	"pgdu/internal/pglog"
)

// logCallCache is the parsed current server log the query detail reads logged
// calls from (Model.logCall).
type logCallCache struct {
	path   string
	src    pglog.Source
	report *pglog.Report
	// busy serialises lookups: pglog.Refresh reuses the report's parser, so two
	// concurrent ones would race. A detail screen that asked while one was
	// running is served by that lookup's handler re-issuing for it.
	busy bool
}

// statementLogSampleLoadedMsg delivers the server-log lookup for a query
// detail's sample call. report/src/path are the (re)parsed current log and go
// to Model.logCall whatever became of the screen; call/params/info are the
// result for the screen identified by query.
type statementLogSampleLoadedMsg struct {
	db     string
	query  string           // matches screen.stat.detail.Query for stale-message rejection
	call   string           // the logged statement with its bind values spliced in; "" when no execution qualified
	params []pg.SampleParam // per-$n breakdown of call, Source pg.ParamLog
	info   sampleLogInfo
	noLog  bool  // discovery found no current server log to read
	err    error // the log could not be read or parsed
	path   string
	src    pglog.Source
	report *pglog.Report // nil when nothing was read (noLog / err)
}

// lookupLogCallCmd searches the current server log for the newest execution of
// s's query that the server logged with its bind values (log_min_duration_statement
// or log_statement with log_parameter_max_length), the second and last source of
// real parameter values after pg_qualstats. Returns nil while another lookup is
// running — its handler re-issues for this screen — or without a detail.
func (m *Model) lookupLogCallCmd(s *screen) tea.Cmd {
	if s.stat.detail == nil || m.logCall.busy {
		return nil
	}
	m.logCall.busy = true
	db, q, qid := s.db, s.stat.detail.Query, s.stat.detail.QueryID
	types := s.stat.sampleParams
	cache := m.logCall
	logFile := m.logFile
	return query(func(ctx context.Context) tea.Msg {
		msg := statementLogSampleLoadedMsg{db: db, query: q}
		cands := m.client.DiscoverLogs(ctx, logFile)
		i := currentLogIndex(cands)
		if i < 0 {
			msg.noLog = true
			return msg
		}
		cand := cands[i]
		settings := m.client.LogSettings(ctx)
		loc := pglog.Location(settings)
		var r *pglog.Report
		var err error
		src := cache.src
		if cache.report != nil && cache.path == cand.Info.Path {
			r, err = pglog.Refresh(ctx, cache.report, src, loc, pglog.AggOptions{})
		} else {
			src = cand.Open()
			r, err = pglog.Load(ctx, src, settings["log_line_prefix"], loc, logDefaultWindow, pglog.AggOptions{})
		}
		if err != nil {
			msg.err = err
			return msg
		}
		msg.path, msg.src, msg.report = cand.Info.Path, src, r
		bc, ok := r.LatestBoundCall(qid, q, db)
		if !ok {
			return msg
		}
		// Continuation lines of a multi-line statement carry the server's tab
		// indentation; strip it the way the log entry view does.
		vals := typedLogValues(bc.Values, types)
		call := string(bc.Entry.SQL)
		if len(vals) > 0 {
			call, _ = pglog.SubstituteParams(call, vals)
		}
		msg.call = dedent(call)
		msg.params = logSampleParams(vals, types)
		msg.info = sampleLogInfo{path: cand.Info.Path, at: bc.Entry.Time, durationMs: bc.Entry.DurationMs}
		return msg
	})
}

// typedLogValues casts each logged bind value to the type PREPARE inferred for
// its $n (the pg_stat_statements breakdown), so the spliced call type-checks the
// way the bound call did: the server logs every value as a plain quoted string,
// and `NOW() - '04:00:00'` would read as a timestamp subtraction instead of the
// interval the client bound. NULL and values whose $n has no inferred type are
// left as logged.
func typedLogValues(vals []string, types []pg.SampleParam) []string {
	if len(vals) == 0 {
		return nil
	}
	byOrd := make(map[int]string, len(types))
	for _, t := range types {
		byOrd[t.Ordinal] = t.Type
	}
	out := make([]string, len(vals))
	for i, v := range vals {
		if t := byOrd[i+1]; t != "" && strings.HasPrefix(v, "'") {
			v += "::" + t
		}
		out[i] = v
	}
	return out
}

// logSampleParams is the verbose breakdown of a call taken from the server log:
// one row per logged bind value in $n order, typed and column-tied from the
// pg_stat_statements side when the ordinal exists there — a client that binds
// $1…$k keeps those numbers in the pgss text; only inlined constants are
// numbered after them, and those are already literal in the call.
func logSampleParams(vals []string, types []pg.SampleParam) []pg.SampleParam {
	if len(vals) == 0 {
		return nil
	}
	byOrd := make(map[int]pg.SampleParam, len(types))
	for _, t := range types {
		byOrd[t.Ordinal] = t
	}
	out := make([]pg.SampleParam, 0, len(vals))
	for i, v := range vals {
		t := byOrd[i+1]
		out = append(out, pg.SampleParam{Ordinal: i + 1, Type: t.Type, Column: t.Column, Value: v, Source: pg.ParamLog})
	}
	return out
}
