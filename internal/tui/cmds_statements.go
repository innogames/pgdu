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
	liveCount int       // distinct statements tracked right now — bars the live anchors (0 = unknown)
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
	query     string       // matches screen.stat.detail.Query for stale-message rejection
	sample    string       // complete, runnable call; "" when no pg_qualstats source covered every $n
	source    sampleSource // where sample came from (sampleNone when sample == "")
	qualstats bool         // pg_qualstats is installed in db (drives the source hint)
	// qualSamples is true when pg_qualstats holds constants for this query, so
	// the p captured-values browser has something to show.
	qualSamples bool
	// qualTracked counts the quals pg_qualstats tracked for this query when it
	// captured no constant for any of them — the bind-parameter case, which the
	// detail view names instead of claiming nothing was seen. Only read when
	// qualSamples is false.
	qualTracked int
	// installable is true when pg_qualstats is absent but already in
	// shared_preload_libraries, so a one-key CREATE EXTENSION would enable real
	// values. Drives the detail view's optional install hint.
	installable bool
	// params is the per-placeholder breakdown (type, predicate column, value,
	// source) for the verbose detail view. Nil on the whole-example path; kept
	// when sample == "" so the view can say which $n are missing.
	params []pg.SampleParam
	// needLog asks the handler to search the server log for a logged call:
	// pg_qualstats produced no complete call.
	needLog bool
	err     error // InferParams failure
}
type statementExplainLoadedMsg struct {
	db      string
	query   string // matches screen.statDetail.Query for stale-message rejection
	call    string // the literal call the plan ran on; "" for the generic plan
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
type statementHotLoadedMsg struct {
	query string // matches screen.statDetail.Query for stale-message rejection
	stats *pg.TableHotStats
	err   error
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
		return m.listSnapshots(ctx, dir, db)
	})
}

// listSnapshots is the shared body of the list and delete commands: the
// directory listing plus the live decorations the browser needs alongside it.
func (m *Model) listSnapshots(ctx context.Context, dir, db string) snapshotsListedMsg {
	metas, err := pg.ListSnapshots(dir)
	reset, _ := m.client.StatementsInfo(ctx, db)  // best-effort validity filter
	count, _ := m.client.StatementsCount(ctx, db) // best-effort bar for the live anchors
	return snapshotsListedMsg{dir: dir, metas: metas, liveReset: reset, liveCount: count, err: err}
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
		return m.listSnapshots(ctx, dir, db)
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

// loadStatementTableHotCmd fetches the HOT-update counters for the statement's
// main table (parsed from the query, resolved server-side) so the detail view
// can show its HOT update ratio. Returns nil when no table can be parsed.
func (m *Model) loadStatementTableHotCmd(db, queryText string) tea.Cmd {
	name := pg.MainTable(queryText)
	if name == "" {
		return nil
	}
	return query(func(ctx context.Context) tea.Msg {
		st, err := m.client.TableHotStats(ctx, db, name)
		return statementHotLoadedMsg{query: queryText, stats: st, err: err}
	})
}

// loadStatementSampleCmd resolves the example call to show under a query from
// pg_qualstats: the whole-statement example it captured (real constants, so
// EXPLAIN reflects the plan a real call gets), else its per-predicate constants
// mapped onto every $n. Values are never sampled from tables or synthesized: a
// call is reported only when complete, and otherwise needLog asks the handler to
// try the server log. The qualstats flag is reported either way so the detail
// view can label the source and offer the captured-values list only when there's
// real data behind it.
func (m *Model) loadStatementSampleCmd(db string, queryID int64, queryText string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		qualstats := m.client.EnsureQualstats(ctx, db) == nil
		if qualstats {
			// pg_qualstats caps example queries at track_activity_query_size, so a
			// long statement comes back truncated mid-token — unusable for EXPLAIN.
			// Reject those and fall through to the per-predicate constants.
			if ex, err := m.client.QualstatsExampleQuery(ctx, db, queryID); err == nil && ex != "" && pg.QualstatsExampleUsable(queryText, ex) {
				return statementSampleLoadedMsg{db: db, query: queryText, sample: ex, source: sampleQualExample, qualstats: true, qualSamples: true}
			}
		}
		// Absent but preloaded → a plain CREATE EXTENSION would enable real values;
		// surface that as an install hint.
		installable := false
		if !qualstats {
			installable, _ = m.client.QualstatsPreloaded(ctx, db)
		}
		// Even when the whole-statement example is unusable, pg_qualstats may hold
		// per-predicate constants for individual placeholders — the data the `p`
		// browser shows, so it is offered only when there is some.
		var samples []pg.QualSample
		tracked := 0
		if qualstats {
			samples, _ = m.client.QualstatsSamples(ctx, db, queryID)
			if len(samples) == 0 {
				// Nothing captured: count the quals anyway, so the view can say
				// whether pg_qualstats never saw the query or only ever saw it
				// with bound parameters (no literal to deparse).
				tracked, _ = m.client.QualstatsQualTracked(ctx, db, queryID)
			}
		}
		params, err := m.client.InferParams(ctx, db, queryText)
		if err != nil {
			return statementSampleLoadedMsg{db: db, query: queryText, err: err, qualstats: qualstats, qualSamples: len(samples) > 0, qualTracked: tracked, installable: installable, needLog: true}
		}
		// Map the constants to their $n; a call results only when every placeholder
		// got one.
		var qual map[int]string
		if len(samples) > 0 {
			qual = pg.MapQualConstants(queryText, params, samples)
		}
		real, breakdown := pg.ResolveSampleParams(queryText, params, qual)
		msg := statementSampleLoadedMsg{db: db, query: queryText, qualstats: qualstats, qualSamples: len(samples) > 0, qualTracked: tracked, installable: installable, params: breakdown}
		msg.sample = pg.BuildSampleCall(queryText, params, real)
		switch {
		case msg.sample == "":
			msg.needLog = true
		case len(params) == 0:
			msg.source = sampleNoParams
		default:
			msg.source = sampleQualPredicates
		}
		return msg
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
// ANALYZE) on sampleCall, a fully-literal real call. matchQuery is the
// normalized text used only to reject stale messages. Used in place of the
// generic plan whenever a sample call exists, so the planner sees the captured
// values instead of $n.
func (m *Model) loadStatementExplainLiteralCmd(db, matchQuery, sampleCall string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		plan, err := m.client.ExplainLiteral(ctx, db, sampleCall)
		return statementExplainLoadedMsg{db: db, query: matchQuery, call: sampleCall, plan: plan, err: err}
	})
}

// loadStatementExplainAnalyzeCmd runs EXPLAIN ANALYZE on sampleCall (a fully
// literal query). matchQuery is the normalized query text used only to reject
// stale messages — sampleCall is what actually executes.
func (m *Model) loadStatementExplainAnalyzeCmd(db, matchQuery, sampleCall string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		plan, err := m.client.ExplainAnalyze(ctx, db, sampleCall)
		return statementExplainLoadedMsg{db: db, query: matchQuery, call: sampleCall, plan: plan, err: err, analyze: true}
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
