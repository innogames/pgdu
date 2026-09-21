package tui

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"pgdu/internal/pg"
	"pgdu/internal/pglog"
)

func detailScreen(q *pg.QueryStat) *screen {
	return &screen{level: levelStatementDetail, title: "query", tool: toolQueries, db: "shop",
		loaded: true, stat: stmtState{detail: q, explaining: true}}
}

// The pg_qualstats result lands on the detail: a complete call is shown as is,
// an incomplete one leaves the call empty, keeps the breakdown and starts the
// server-log search; the plan runs either way.
func TestOnStatementSampleLoaded(t *testing.T) {
	q := &pg.QueryStat{QueryID: 7, Query: "select * from t where a = $1 and b = $2"}

	s := detailScreen(q)
	m := newTestModel(s)
	cmd := m.onStatementSampleLoaded(statementSampleLoadedMsg{db: "shop", query: q.Query,
		sample: "select * from t where a = 1 and b = 'x'", source: sampleQualPredicates, qualstats: true})
	if s.stat.sampleCall == "" || s.stat.sampleSource != sampleQualPredicates || !s.stat.sampleResolved || s.stat.logLookup != logLookupIdle || cmd == nil {
		t.Errorf("complete: %+v cmd=%v", s.stat, cmd != nil)
	}

	s = detailScreen(q)
	m = newTestModel(s)
	partial := []pg.SampleParam{{Ordinal: 1, Column: "a", Value: "1", Source: pg.ParamQualstats}, {Ordinal: 2, Column: "b"}}
	cmd = m.onStatementSampleLoaded(statementSampleLoadedMsg{db: "shop", query: q.Query, qualstats: true, params: partial, needLog: true})
	if s.stat.sampleCall != "" || s.stat.logLookup != logLookupRunning || !reflect.DeepEqual(s.stat.sampleParams, partial) || cmd == nil || !m.logCall.busy {
		t.Errorf("incomplete: %+v cmd=%v busy=%v", s.stat, cmd != nil, m.logCall.busy)
	}

	// Absent but preloaded → the i-key install prompt; installed → none.
	s = detailScreen(q)
	m = newTestModel(s)
	m.onStatementSampleLoaded(statementSampleLoadedMsg{db: "shop", query: q.Query, installable: true, needLog: true})
	if s.extPrompt == nil || s.extPrompt.name != extQualstats {
		t.Errorf("installable: extPrompt = %+v", s.extPrompt)
	}

	// A result for another query is dropped.
	s = detailScreen(q)
	m = newTestModel(s)
	if cmd := m.onStatementSampleLoaded(statementSampleLoadedMsg{db: "shop", query: "other", sample: "x", source: sampleQualExample}); cmd != nil || s.stat.sampleResolved || s.stat.sampleCall != "" {
		t.Errorf("stale message applied: %+v", s.stat)
	}
}

// The server-log result caches the parsed log whatever screen is up, applies a
// found call to the detail that asked and re-plans on it, and reports the
// none / no-log / error outcomes.
func TestOnStatementLogSampleLoaded(t *testing.T) {
	q := &pg.QueryStat{QueryID: 7, Query: "select * from t where a = $1"}
	report := &pglog.Report{}
	src := pglog.OpenLocal("/var/log/postgresql/x.log")
	at := time.Date(2026, 9, 21, 14, 2, 11, 0, time.UTC)

	s := detailScreen(q)
	s.stat.explaining, s.stat.logLookup = false, logLookupRunning
	m := newTestModel(s)
	m.logCall.busy = true
	cmd := m.onStatementLogSampleLoaded(statementLogSampleLoadedMsg{db: "shop", query: q.Query,
		call: "select * from t where a = '5'", params: []pg.SampleParam{{Ordinal: 1, Value: "'5'", Source: pg.ParamLog}},
		info: sampleLogInfo{path: "/var/log/postgresql/x.log", at: at, durationMs: 12}, path: "/var/log/postgresql/x.log", src: src, report: report})
	if s.stat.sampleCall != "select * from t where a = '5'" || s.stat.sampleSource != sampleLog || s.stat.logLookup != logLookupFound ||
		s.stat.logInfo == nil || !s.stat.logInfo.at.Equal(at) || len(s.stat.sampleParams) != 1 {
		t.Errorf("found: %+v", s.stat)
	}
	if !s.stat.explaining || cmd == nil {
		t.Error("a found call must re-run the plan on it")
	}
	if m.logCall.busy || m.logCall.report != report || m.logCall.path != "/var/log/postgresql/x.log" {
		t.Errorf("cache = %+v", m.logCall)
	}

	// An EXPLAIN ANALYZE the user asked for is left alone.
	s = detailScreen(q)
	s.stat.explaining, s.stat.explainAnalyze, s.stat.logLookup = false, true, logLookupRunning
	m = newTestModel(s)
	if cmd := m.onStatementLogSampleLoaded(statementLogSampleLoadedMsg{query: q.Query, call: "x", report: report}); cmd != nil || s.stat.explaining {
		t.Error("found call must not replace an EXPLAIN ANALYZE")
	}

	// Stale query: the cache is still stored; the screen is untouched; a detail
	// still waiting gets its own lookup.
	s = detailScreen(q)
	s.stat.logLookup = logLookupRunning
	m = newTestModel(s)
	m.logCall.busy = true
	cmd = m.onStatementLogSampleLoaded(statementLogSampleLoadedMsg{query: "other", call: "x", path: "p", src: src, report: report})
	if s.stat.sampleCall != "" || m.logCall.report != report || cmd == nil || !m.logCall.busy {
		t.Errorf("stale: %+v cache=%+v cmd=%v", s.stat, m.logCall, cmd != nil)
	}
	s.stat.logLookup = logLookupNone
	m.logCall.busy = true
	if cmd := m.onStatementLogSampleLoaded(statementLogSampleLoadedMsg{query: "other", report: report}); cmd != nil {
		t.Error("a detail not waiting must not get a lookup")
	}

	for _, c := range []struct {
		msg  statementLogSampleLoadedMsg
		want logLookupState
	}{
		{statementLogSampleLoadedMsg{query: q.Query, report: report}, logLookupNone},
		{statementLogSampleLoadedMsg{query: q.Query, noLog: true}, logLookupNoLog},
		{statementLogSampleLoadedMsg{query: q.Query, err: errors.New("read: permission denied")}, logLookupErr},
	} {
		s = detailScreen(q)
		s.stat.explaining, s.stat.logLookup = false, logLookupRunning
		m = newTestModel(s)
		if cmd := m.onStatementLogSampleLoaded(c.msg); cmd != nil || s.stat.logLookup != c.want || s.stat.sampleCall != "" {
			t.Errorf("%+v → %v (want %v) cmd=%v", c.msg, s.stat.logLookup, c.want, cmd != nil)
		}
		if c.want == logLookupErr && s.stat.logErr == nil {
			t.Error("error not kept")
		}
	}
}

// A plan belongs to the call it ran on: a generic result landing after a logged
// call replaced the sample is dropped; ANALYZE results always land.
func TestOnStatementExplainLoadedStaleCall(t *testing.T) {
	q := &pg.QueryStat{QueryID: 7, Query: "select * from t where a = $1"}
	s := detailScreen(q)
	s.stat.sampleCall = "select * from t where a = '5'"
	m := newTestModel(s)
	m.onStatementExplainLoaded(statementExplainLoadedMsg{query: q.Query, call: "", plan: "Seq Scan"})
	if !s.stat.explaining || s.stat.explain != "" {
		t.Error("generic plan for a superseded call must be dropped")
	}
	m.onStatementExplainLoaded(statementExplainLoadedMsg{query: q.Query, call: s.stat.sampleCall, plan: "Index Scan"})
	if s.stat.explaining || s.stat.explain != "Index Scan" {
		t.Error("plan for the current call must land")
	}
	s.stat.explaining = true
	m.onStatementExplainLoaded(statementExplainLoadedMsg{query: q.Query, call: "", plan: "Analyzed", analyze: true})
	if s.stat.explaining || s.stat.explain != "Analyzed" || !s.stat.explainAnalyze {
		t.Error("ANALYZE result must land regardless of the call")
	}
}

// A refresh of the detail resets the sample state so a stale call is never
// runnable while its sources are re-resolved.
func TestResetSample(t *testing.T) {
	st := stmtState{sampleCall: "x", sampleSource: sampleLog, sampleParams: []pg.SampleParam{{Ordinal: 1}}, sampleResolved: true,
		logLookup: logLookupFound, logInfo: &sampleLogInfo{}, logErr: errors.New("e"), sampleErr: errors.New("e"), qualstats: true, verbose: true}
	st.resetSample()
	if st.sampleCall != "" || st.sampleSource != sampleNone || st.sampleParams != nil || st.sampleResolved || st.logLookup != logLookupIdle ||
		st.logInfo != nil || st.logErr != nil || st.sampleErr != nil {
		t.Errorf("resetSample left %+v", st)
	}
	if !st.qualstats || !st.verbose {
		t.Error("resetSample must not touch the qualstats flag or the v toggle")
	}
}

func TestLogSampleParams(t *testing.T) {
	types := []pg.SampleParam{{Ordinal: 1, Type: "bigint", Column: "alliance_id"}, {Ordinal: 3, Type: "text", Column: "c"}}
	got := logSampleParams([]string{"'191503'", "NULL"}, types)
	want := []pg.SampleParam{
		{Ordinal: 1, Type: "bigint", Column: "alliance_id", Value: "'191503'", Source: pg.ParamLog},
		{Ordinal: 2, Value: "NULL", Source: pg.ParamLog},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("logSampleParams = %+v, want %+v", got, want)
	}
	if got := logSampleParams(nil, types); got != nil {
		t.Errorf("no values → %+v", got)
	}
	if got := logSampleParams([]string{"'1'"}, nil); len(got) != 1 || got[0].Type != "" {
		t.Errorf("no types → %+v", got)
	}
}

// Logged values are plain quoted strings; the inferred parameter types make the
// spliced call type-check like the bound one (NOW() - '04:00:00' needs ::interval).
func TestTypedLogValues(t *testing.T) {
	types := []pg.SampleParam{{Ordinal: 1, Type: "interval"}, {Ordinal: 2, Type: "bigint"}, {Ordinal: 3, Type: "text"}}
	got := typedLogValues([]string{"'04:00:00'", "'10000'", "NULL", "'untyped'"}, types)
	want := []string{"'04:00:00'::interval", "'10000'::bigint", "NULL", "'untyped'"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("typedLogValues = %v, want %v", got, want)
	}
	if got := typedLogValues(nil, types); got != nil {
		t.Errorf("no values → %v", got)
	}
	if got := typedLogValues([]string{"'x'"}, nil); !reflect.DeepEqual(got, []string{"'x'"}) {
		t.Errorf("no types → %v", got)
	}
}

func TestCurrentLogIndex(t *testing.T) {
	if got := currentLogIndex(nil); got != -1 {
		t.Errorf("none = %d", got)
	}
	cands := []pg.LogCandidate{{Info: pglog.SourceInfo{Path: "a"}}, {Info: pglog.SourceInfo{Path: "b", Current: true}}}
	if got := currentLogIndex(cands); got != 1 {
		t.Errorf("current = %d", got)
	}
	if got := currentLogIndex(cands[:1]); got != 0 {
		t.Errorf("sole = %d", got)
	}
	if got := currentLogIndex([]pg.LogCandidate{{Info: pglog.SourceInfo{Path: "a"}}, {Info: pglog.SourceInfo{Path: "b"}}}); got != -1 {
		t.Errorf("two non-current = %d", got)
	}
}
