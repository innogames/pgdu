package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

type statementsLoadedMsg struct {
	db            string
	stats         []pg.QueryStat // raw cumulative snapshot; diffed against the baseline
	trackPlanning bool           // whether plan time is being collected
	statsReset    time.Time      // pg_stat_statements_info.stats_reset — guards a disk baseline
	err           error
}

// Snapshot persistence messages (levelStatements / levelSnapshots).
type snapshotSavedMsg struct {
	path string
	err  error
}
type snapshotsListedMsg struct {
	dir       string
	metas     []pg.SnapshotMeta
	liveReset time.Time // current pg_stat_statements stats_reset — drops invalidated snapshots
	err       error
}
type snapshotBaseLoadedMsg struct {
	snap *pg.Snapshot
	err  error
}
type snapshotFrozenLoadedMsg struct {
	base       *pg.Snapshot // nil when cumulative (since-reset baseline)
	end        *pg.Snapshot
	cumulative bool // base is an empty map (since last reset), not a real snapshot
	stay       bool // keep the snapshots browser open (the pick landed as the end)
	err        error
}

// statementsTickMsg drives the self-rescheduling refresh of the top-queries
// table so it behaves as a live "since you opened it" monitor.
type statementsTickMsg struct{}

type statementSampleLoadedMsg struct {
	db        string
	query     string // matches screen.statDetail.Query for stale-message rejection
	sample    string
	real      bool // sample is a real captured example (pg_qualstats), not synthesized
	fromData  bool // synthesized, but ≥1 placeholder was filled with a real value sampled from the live table
	fromQual  bool // synthesized, but ≥1 placeholder was filled with a per-predicate pg_qualstats constant
	qualstats bool // pg_qualstats is installed in db (drives the source hint / captured-values affordance)
	// installable is true when pg_qualstats is absent but already in
	// shared_preload_libraries, so a one-key CREATE EXTENSION would enable real
	// values. Drives the detail view's optional install hint.
	installable bool
	// params is the per-placeholder breakdown behind sample (type, predicate
	// column, value, source) for the verbose detail view. Nil on the real
	// pg_qualstats path (the whole call is captured, not built from $n).
	params []pg.SampleParam
	err    error
}
type statementExplainLoadedMsg struct {
	db      string
	query   string // matches screen.statDetail.Query for stale-message rejection
	plan    string
	err     error
	analyze bool // plan came from EXPLAIN ANALYZE rather than the generic plan
}
type statementSamplesLoadedMsg struct {
	db      string
	queryID int64 // matches screen.statDetail.QueryID for stale-message rejection
	samples []pg.QualSample
	err     error
}
type statementResultLoadedMsg struct {
	query     string // matches screen.statDetail.Query for stale-message rejection
	result    *pg.DiagResult
	truncated bool // more rows were waiting than statementResultMaxRows
	err       error
}

func (m *Model) loadStatementsCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		stats, err := m.client.StatementSnapshot(ctx, db)
		if err != nil {
			return statementsLoadedMsg{db: db, err: err}
		}
		tp, _ := m.client.TrackPlanning(ctx, db)     // best-effort column decoration
		reset, _ := m.client.StatementsInfo(ctx, db) // best-effort reset guard for disk baselines
		return statementsLoadedMsg{db: db, stats: stats, trackPlanning: tp, statsReset: reset}
	})
}

// saveSnapshotCmd captures the current pg_stat_statements counters for db and
// writes them to the snapshot directory, reporting the resulting path.
func (m *Model) saveSnapshotCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		snap, err := m.client.CaptureSnapshot(ctx, db)
		if err != nil {
			return snapshotSavedMsg{err: err}
		}
		path, err := pg.SaveSnapshot(m.snapshotDir, snap)
		return snapshotSavedMsg{path: path, err: err}
	})
}

// listSnapshotsCmd reads the snapshot directory for the browser, along with the
// current pg_stat_statements stats_reset for db so the handler can drop snapshots
// the live counters have since outgrown (a reset between capture and now).
func (m *Model) listSnapshotsCmd(dir, db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		metas, err := pg.ListSnapshots(dir)
		reset, _ := m.client.StatementsInfo(ctx, db) // best-effort validity filter
		return snapshotsListedMsg{dir: dir, metas: metas, liveReset: reset, err: err}
	})
}

// loadSnapshotBaseCmd loads one snapshot to use as the live window's baseline.
func (m *Model) loadSnapshotBaseCmd(path string) tea.Cmd {
	return query(func(context.Context) tea.Msg {
		snap, err := pg.LoadSnapshot(path)
		return snapshotBaseLoadedMsg{snap: snap, err: err}
	})
}

// loadSnapshotFrozenCmd loads one or two snapshots for a frozen diff (base→end).
// When basePath is snapReset the baseline is empty (cumulative since last reset)
// and only the end snapshot is loaded from disk. stay keeps the snapshots
// browser open after the window applies (the pick landed as the end, so the
// user likely wants to adjust the start next).
func (m *Model) loadSnapshotFrozenCmd(basePath, endPath string, stay bool) tea.Cmd {
	return query(func(context.Context) tea.Msg {
		if basePath == snapReset {
			end, err := pg.LoadSnapshot(endPath)
			return snapshotFrozenLoadedMsg{end: end, cumulative: true, stay: stay, err: err}
		}
		base, err := pg.LoadSnapshot(basePath)
		if err != nil {
			return snapshotFrozenLoadedMsg{err: err}
		}
		end, err := pg.LoadSnapshot(endPath)
		return snapshotFrozenLoadedMsg{base: base, end: end, stay: stay, err: err}
	})
}

// deleteSnapshotCmd removes a snapshot file then re-lists the directory so the
// browser refreshes; the listing carries any delete error.
func (m *Model) deleteSnapshotCmd(path, dir, db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		if err := pg.DeleteSnapshot(path); err != nil {
			return snapshotsListedMsg{dir: dir, err: err}
		}
		metas, err := pg.ListSnapshots(dir)
		reset, _ := m.client.StatementsInfo(ctx, db)
		return snapshotsListedMsg{dir: dir, metas: metas, liveReset: reset, err: err}
	})
}

// statementsTick schedules the next top-queries re-sample, or returns nil when
// auto-refresh is off — disabled by config (--queries-refresh 0) or cycled off
// at runtime (t key). Returning nil stops the self-rescheduling loop; cycling
// refresh back on or re-entering the tool restarts it.
func (m *Model) statementsTick() tea.Cmd {
	if m.statRefresh <= 0 {
		return nil
	}
	return tea.Tick(m.statRefresh, func(time.Time) tea.Msg {
		return statementsTickMsg{}
	})
}

// cycleStatRefresh steps the live-window cadence through the t-key cycle:
// 2s (the default) → 60s → off → 2s. Any other value — a custom configured
// interval or 0 (off) — snaps to the 2s default on the first press.
func (m *Model) cycleStatRefresh() {
	switch m.statRefresh {
	case 2 * time.Second:
		m.statRefresh = 60 * time.Second
	case 60 * time.Second:
		m.statRefresh = 0
	default:
		m.statRefresh = 2 * time.Second
	}
}

// loadStatementSampleCmd resolves the example call to show under a query. It
// prefers a *real* example query from pg_qualstats (real captured constants, so
// EXPLAIN reflects the plan a real call gets); when pg_qualstats is absent or
// has sampled nothing for this queryid yet, it falls back to synthesizing typed
// literals from the inferred $n types (BuildSampleCall). The qualstats flag is
// reported either way so the detail view can label the source and offer the
// captured-values list only when there's real data behind it.
// paramsWithout returns params whose ordinal is not already resolved in cover,
// so live-table sampling skips placeholders a higher-precedence source filled.
func paramsWithout(params []pg.ParamType, cover map[int]string) []pg.ParamType {
	if len(cover) == 0 {
		return params
	}
	out := make([]pg.ParamType, 0, len(params))
	for _, p := range params {
		if cover[p.Ordinal] == "" {
			out = append(out, p)
		}
	}
	return out
}

func (m *Model) loadStatementSampleCmd(db string, queryID int64, queryText string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		qualstats := m.client.EnsureQualstats(ctx, db) == nil
		if qualstats {
			// pg_qualstats caps example queries at track_activity_query_size, so a
			// long statement comes back truncated mid-token — unusable for EXPLAIN.
			// Reject those and fall through to the synthesized (full-length) call.
			if ex, err := m.client.QualstatsExampleQuery(ctx, db, queryID); err == nil && ex != "" && pg.QualstatsExampleUsable(queryText, ex) {
				return statementSampleLoadedMsg{db: db, query: queryText, sample: ex, real: true, qualstats: true}
			}
		}
		// Absent but preloaded → a plain CREATE EXTENSION would enable real values;
		// surface that as an install hint rather than only synthesizing literals.
		installable := false
		if !qualstats {
			installable, _ = m.client.QualstatsPreloaded(ctx, db)
		}
		params, err := m.client.InferParams(ctx, db, queryText)
		if err != nil {
			return statementSampleLoadedMsg{db: db, query: queryText, err: err, qualstats: qualstats, installable: installable}
		}
		// Even when the whole-statement example is unusable, pg_qualstats may hold
		// per-predicate constants for individual placeholders (the same data the `p`
		// browser shows). Map those to their $n; they're real values that actually
		// appeared in real calls, so they take precedence over live-table samples.
		var qual map[int]string
		if qualstats {
			if samples, err := m.client.QualstatsSamples(ctx, db, queryID); err == nil {
				qual = pg.MapQualConstants(queryText, params, samples)
			}
		}
		// Pull live-table values only for the placeholders qualstats didn't cover, so
		// the synthesized call uses constants that actually exist in the data; any
		// ordinal still unresolved falls back to a generic typed literal.
		live := m.client.SampleParamValues(ctx, db, queryText, paramsWithout(params, qual))
		// EXTRACT($n FROM …) field slots and INTERVAL $n value slots aren't real
		// parameters; ResolveSampleParams fills them with bare literals ('epoch' /
		// '1 day') so the sample call stays parseable and runnable.
		real, breakdown := pg.ResolveSampleParams(queryText, params, qual, live, pg.ExtractFieldOrdinals(queryText), pg.IntervalParamOrdinals(queryText))
		return statementSampleLoadedMsg{db: db, query: queryText, sample: pg.BuildSampleCall(queryText, params, real),
			fromData: len(live) > 0, fromQual: len(qual) > 0, qualstats: qualstats, installable: installable,
			params: breakdown}
	})
}

func (m *Model) loadStatementExplainCmd(db, queryText string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		plan, err := m.client.ExplainGeneric(ctx, db, queryText)
		return statementExplainLoadedMsg{db: db, query: queryText, plan: plan, err: err}
	})
}

// loadStatementSamplesCmd fetches the real predicate constants pg_qualstats
// captured for queryID — the captured-values list behind the detail view's `p`.
func (m *Model) loadStatementSamplesCmd(db string, queryID int64) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		if err := m.client.EnsureQualstats(ctx, db); err != nil {
			return statementSamplesLoadedMsg{db: db, queryID: queryID, err: err}
		}
		samples, err := m.client.QualstatsSamples(ctx, db, queryID)
		return statementSamplesLoadedMsg{db: db, queryID: queryID, samples: samples, err: err}
	})
}

// loadStatementExplainLiteralCmd runs a plain EXPLAIN (no GENERIC_PLAN, no
// ANALYZE) on sampleCall, a fully-literal real example query. matchQuery is the
// normalized text used only to reject stale messages. Used in place of the
// generic plan when a real pg_qualstats example is available, so the planner
// sees real values instead of $n.
func (m *Model) loadStatementExplainLiteralCmd(db, matchQuery, sampleCall string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		plan, err := m.client.ExplainLiteral(ctx, db, sampleCall)
		return statementExplainLoadedMsg{db: db, query: matchQuery, plan: plan, err: err}
	})
}

// loadStatementExplainAnalyzeCmd runs EXPLAIN ANALYZE on sampleCall (a fully
// literal query). matchQuery is the normalized query text used only to reject
// stale messages — sampleCall is what actually executes.
func (m *Model) loadStatementExplainAnalyzeCmd(db, matchQuery, sampleCall string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		plan, err := m.client.ExplainAnalyze(ctx, db, sampleCall)
		return statementExplainLoadedMsg{db: db, query: matchQuery, plan: plan, err: err, analyze: true}
	})
}

// statementResultMaxRows caps how many rows the execute action fetches into the
// result table — enough to eyeball a query's output without stalling the TUI on
// a query that returns millions of rows.
const statementResultMaxRows = 100

// loadStatementResultCmd executes sampleCall (a fully literal, read-only query)
// and returns its rows as a generic result table. matchQuery is the normalized
// query text used only to reject stale messages — sampleCall is what executes.
func (m *Model) loadStatementResultCmd(db, matchQuery, sampleCall string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		result, truncated, err := m.client.RunReadOnlyQuery(ctx, db, sampleCall, statementResultMaxRows)
		return statementResultLoadedMsg{query: matchQuery, result: result, truncated: truncated, err: err}
	})
}
