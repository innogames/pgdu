package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// actColID is the stable identity of an Activity tool column. It keys the user's
// visibility set (the C picker) and tracks the active sort column across rebuilds.
type actColID string

const (
	actColPID         actColID = "pid"
	actColDatabase    actColID = "database"
	actColUser        actColID = "user"
	actColApp         actColID = "app"
	actColClient      actColID = "client"  // raw IP from client_addr
	actColHost        actColID = "host"    // resolved hostname (or raw IP when unresolvable)
	actColBackend     actColID = "backend" // backend_type
	actColState       actColID = "state"
	actColWait        actColID = "wait"
	actColXactAge     actColID = "xact_age"
	actColQueryAge    actColID = "query_age"
	actColStateAge    actColID = "state_age"
	actColBackendXid  actColID = "xid"
	actColBackendXmin actColID = "xmin"
	actColBlockedBy   actColID = "blocked_by"
	actColTable       actColID = "table"
	actColType        actColID = "type" // command type (S/U/D/…), same parse as top-queries
	actColQuery       actColID = "query"
	// OS-level proc columns (Linux only, local server; show — otherwise).
	actColRSS      actColID = "rss"
	actColCPU      actColID = "cpu%"
	actColReadBps  actColID = "read/s"
	actColWriteBps actColID = "write/s"
)

// actCtx carries per-build inputs the activity column cells need beyond the row
// itself. All maps may be nil (non-Linux host, remote connection, first sample).
type actCtx struct {
	hosts map[string]string     // IP → resolved hostname (from actHosts on screen)
	proc  map[int32]procDerived // PID → /proc-derived stats; nil = unavailable
	toast map[string]string     // db+relname → owning table (from actToast on screen)
}

// toastKey keys the actToast map; it mirrors pg.ResolveToastOwners' own cache
// key (TOAST OIDs are database-local, so the db disambiguates a shared relname).
func toastKey(db, relname string) string { return db + "\x00" + relname }

// toastOwner resolves the "table" column for a row whose main relation is a
// TOAST relation (pg_toast.pg_toast_<oid>) to the owning table's name, or ""
// when it isn't a TOAST relation or hasn't been resolved yet.
func (ctx actCtx) toastOwner(db, mainTable string) string {
	rn, ok := strings.CutPrefix(mainTable, "pg_toast.")
	if !ok {
		return ""
	}
	return ctx.toast[toastKey(db, rn)]
}

// actColDesc describes one Activity tool column: stable id, header label, render
// kind, whether shown by default, whether it's mandatory, a one-line description
// for the C picker, and the cell builder.
type actColDesc struct {
	id        actColID
	name      string
	kind      pg.DiagColumnKind
	defaultOn bool
	mandatory bool   // can't be hidden
	desc      string // one-line explanation shown in the C picker
	cell      func(pg.ActivityRow, actCtx) pg.DiagCell
}

// actColumnRegistry is the single source of truth for the Activity table's columns
// in display order. Numeric/short columns first; the wide query text last so the
// no-bar last-column grow in renderDiagResult lands on the query.
func actColumnRegistry() []actColDesc {
	return []actColDesc{
		{id: actColPID, name: "pid", kind: pg.DiagInt, defaultOn: true,
			desc: "backend process ID",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell {
				return pg.DiagCell{Display: strconv.Itoa(int(r.PID)), Num: float64(r.PID), HasNum: true}
			}},
		{id: actColDatabase, name: "database", kind: pg.DiagText, defaultOn: true,
			desc: "database the backend is connected to",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell { return pg.DiagCell{Display: r.Database} }},
		{id: actColUser, name: "user", kind: pg.DiagText, defaultOn: true,
			desc: "role name of the connected user",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell { return pg.DiagCell{Display: r.Username} }},
		{id: actColApp, name: "app", kind: pg.DiagText,
			desc: "application_name set by the client",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell { return pg.DiagCell{Display: r.AppName} }},
		{id: actColClient, name: "client", kind: pg.DiagText,
			desc: "raw client IP address (empty for local / unix socket)",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell {
				if r.ClientAddr == "" {
					return pg.DiagCell{Display: "local"}
				}
				return pg.DiagCell{Display: r.ClientAddr}
			}},
		{id: actColHost, name: "host", kind: pg.DiagText, defaultOn: true,
			desc: "resolved hostname for client_addr (falls back to raw IP; cached per session)",
			cell: func(r pg.ActivityRow, ctx actCtx) pg.DiagCell {
				if r.ClientAddr == "" {
					return pg.DiagCell{Display: "local"}
				}
				if h, ok := ctx.hosts[r.ClientAddr]; ok {
					return pg.DiagCell{Display: h}
				}
				return pg.DiagCell{Display: r.ClientAddr}
			}},
		{id: actColBackend, name: "backend", kind: pg.DiagText,
			desc: "backend_type (client backend, autovacuum worker, walsender, …)",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell { return pg.DiagCell{Display: r.BackendType} }},
		{id: actColState, name: "state", kind: pg.DiagBackendState, defaultOn: true,
			desc: "backend state: active, idle, idle in transaction, …",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell { return pg.DiagCell{Display: r.State} }},
		{id: actColWait, name: "wait", kind: pg.DiagText, defaultOn: true,
			desc: "wait event (e.g. Lock/relation, IO/DataFileRead) or blank when not waiting",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell {
				w := strings.TrimSpace(r.WaitEventType + "/" + r.WaitEvent)
				w = strings.Trim(w, "/")
				return pg.DiagCell{Display: w}
			}},
		{id: actColQueryAge, name: "query_age", kind: pg.DiagDuration, defaultOn: true,
			desc: "elapsed time since the current statement started (now() - query_start)",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell {
				if r.QueryAgeMs <= 0 {
					return pg.DiagCell{Display: "—"}
				}
				return pg.DiagCell{Display: fmtAge(r.QueryAgeMs), Num: r.QueryAgeMs, HasNum: true}
			}},
		{id: actColXactAge, name: "xact_age", kind: pg.DiagDuration,
			desc: "elapsed time since the current transaction started (now() - xact_start)",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell {
				if r.XactAgeMs <= 0 {
					return pg.DiagCell{Display: "—"}
				}
				return pg.DiagCell{Display: fmtAge(r.XactAgeMs), Num: r.XactAgeMs, HasNum: true}
			}},
		{id: actColStateAge, name: "state_age", kind: pg.DiagDuration,
			desc: "elapsed time since the state last changed (now() - state_change)",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell {
				if r.StateAgeMs <= 0 {
					return pg.DiagCell{Display: "—"}
				}
				return pg.DiagCell{Display: fmtAge(r.StateAgeMs), Num: r.StateAgeMs, HasNum: true}
			}},
		{id: actColBackendXid, name: "xid", kind: pg.DiagText,
			desc: "transaction ID held by this backend (backend_xid); empty when not in a transaction",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell { return pg.DiagCell{Display: r.BackendXid} }},
		{id: actColBackendXmin, name: "xmin", kind: pg.DiagText,
			desc: "oldest transaction whose row versions this backend may still need (backend_xmin)",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell { return pg.DiagCell{Display: r.BackendXmin} }},
		{id: actColBlockedBy, name: "blocked_by", kind: pg.DiagText,
			desc: "PIDs blocking this backend on a lock (pg_blocking_pids); empty when it isn't waiting on anyone",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell {
				if r.BlockedBy == "" {
					return pg.DiagCell{Display: ""}
				}
				// Pre-style: this column is only non-empty for a genuinely blocked
				// backend, so paint it red to draw the eye. It's a short PID list,
				// so ANSI-in-Display survives the no-truncate path for narrow cells.
				return pg.DiagCell{Display: styleErr.Render(r.BlockedBy)}
			}},
		// OS-level columns sourced from /proc — opt-in, show — on non-Linux or
		// remote connections where the local /proc PIDs don't match.
		{id: actColRSS, name: "mem", kind: pg.DiagCostGraded,
			desc: "resident memory (RSS = physical RAM held by the backend process) from /proc/<pid>/status (Linux, local server only)",
			cell: func(r pg.ActivityRow, ctx actCtx) pg.DiagCell {
				d, ok := ctx.proc[r.PID]
				if !ok || d.RSSBytes <= 0 {
					return pg.DiagCell{Display: "—"}
				}
				return pg.DiagCell{Display: humanize.Bytes(d.RSSBytes), Num: float64(d.RSSBytes), HasNum: true}
			}},
		{id: actColCPU, name: "cpu%", kind: pg.DiagCostGraded,
			desc: "CPU usage sampled per refresh interval from /proc/<pid>/stat (Linux, local server only)",
			cell: func(r pg.ActivityRow, ctx actCtx) pg.DiagCell {
				d, ok := ctx.proc[r.PID]
				if !ok || d.CPUPct < 0 {
					return pg.DiagCell{Display: "—"}
				}
				return pg.DiagCell{Display: fmt.Sprintf("%.1f%%", d.CPUPct), Num: d.CPUPct, HasNum: true}
			}},
		{id: actColReadBps, name: "read/s", kind: pg.DiagCostGraded,
			desc: "storage read throughput from /proc/<pid>/io (Linux, same UID as postgres or root)",
			cell: func(r pg.ActivityRow, ctx actCtx) pg.DiagCell {
				d, ok := ctx.proc[r.PID]
				if !ok || d.ReadBps < 0 {
					return pg.DiagCell{Display: "—"}
				}
				return pg.DiagCell{Display: humanize.Bytes(int64(d.ReadBps)) + "/s", Num: d.ReadBps, HasNum: true}
			}},
		{id: actColWriteBps, name: "write/s", kind: pg.DiagCostGraded,
			desc: "storage write throughput from /proc/<pid>/io (Linux, same UID as postgres or root)",
			cell: func(r pg.ActivityRow, ctx actCtx) pg.DiagCell {
				d, ok := ctx.proc[r.PID]
				if !ok || d.WriteBps < 0 {
					return pg.DiagCell{Display: "—"}
				}
				return pg.DiagCell{Display: humanize.Bytes(int64(d.WriteBps)) + "/s", Num: d.WriteBps, HasNum: true}
			}},

		{id: actColTable, name: "table", kind: pg.DiagText,
			desc: "main table parsed from the current query; a pg_toast.* target resolves to its owning table",
			cell: func(r pg.ActivityRow, ctx actCtx) pg.DiagCell {
				// toastOwner needs the fully-qualified pg_toast.* name to recognise a
				// TOAST target; the plain fallback is public-stripped for display.
				if owner := ctx.toastOwner(r.Database, pg.MainTable(r.Query)); owner != "" {
					return pg.DiagCell{Display: owner}
				}
				return pg.DiagCell{Display: mainTableDisplay(r.Query)}
			}},

		{id: actColType, name: "T", kind: pg.DiagCmdType, defaultOn: true,
			desc: "command type: S/SL/L/I/U/D/M/T (same parse as top-queries; ? for utility/background)",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell { return pg.DiagCell{Display: pg.QueryKind(r.Query)} }},

		// query must stay the very last column: renderDiagResult grows the final
		// column into the leftover terminal width (no bar on this table), so the
		// long query text gets the slack instead of being clipped at diagColWidth.
		{id: actColQuery, name: "query", kind: pg.DiagText, defaultOn: true, mandatory: true,
			desc: "current or last query (always shown)",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell { return pg.DiagCell{Display: r.Query} }},
	}
}

// indexOfActCol returns the position of id within descs, or -1 when absent.
func indexOfActCol(descs []actColDesc, id actColID) int {
	return slices.IndexFunc(descs, func(d actColDesc) bool { return d.id == id })
}

// actColEnabled reports whether column id should be shown. Falls back to def
// (the registry default) when the visibility set has no explicit entry.
func (m *Model) actColEnabled(id actColID, def bool) bool {
	if v, ok := m.actColsVisible[id]; ok {
		return v
	}
	return def
}

// ensureActColsInit lazily materialises the visibility set from the registry
// defaults so the C picker shows concrete checkbox state.
func (m *Model) ensureActColsInit() {
	if m.actColsVisible != nil {
		return
	}
	m.actColsVisible = make(map[actColID]bool)
	for _, d := range actColumnRegistry() {
		m.actColsVisible[d.id] = d.defaultOn || d.mandatory
	}
}

// visibleActCols projects the registry to the columns enabled by the user, in
// registry order. Mandatory columns are always kept.
func (m *Model) visibleActCols() []actColDesc {
	var out []actColDesc
	for _, d := range actColumnRegistry() {
		if d.mandatory || m.actColEnabled(d.id, d.defaultOn) {
			out = append(out, d)
		}
	}
	return out
}

// actDiagColumnsFrom maps projected activity descriptors to the renderer's
// column schema (parallel to actCellsFor).
func actDiagColumnsFrom(descs []actColDesc) []pg.DiagColumn {
	cols := make([]pg.DiagColumn, len(descs))
	for i, d := range descs {
		cols[i] = pg.DiagColumn{Name: d.name, Kind: d.kind}
	}
	return cols
}

// actCellsFor builds one row's cells over the projected descriptors, keeping
// cells parallel to actDiagColumnsFrom(descs) by construction.
func actCellsFor(descs []actColDesc, r pg.ActivityRow, ctx actCtx) []pg.DiagCell {
	cells := make([]pg.DiagCell, len(descs))
	for i, d := range descs {
		cells[i] = d.cell(r, ctx)
	}
	return cells
}

// buildActivityItems converts ActivityRows into generic-table rows using the
// supplied context (resolved hostnames + proc stats). Returns items and the
// projected column descriptors (parallel to each item's cells).
func (m *Model) buildActivityItems(rows []pg.ActivityRow, ctx actCtx) ([]item, []actColDesc) {
	descs := m.visibleActCols()
	items := make([]item, len(rows))
	for i, r := range rows {
		cells := actCellsFor(descs, r, ctx)
		// item.name is the space-joined cell display so the fuzzy filter can
		// match any column value.
		parts := make([]string, len(cells))
		for j, c := range cells {
			parts[j] = c.Display
		}
		items[i] = item{
			name:        strings.Join(parts, " "),
			data:        cells,
			statQueryID: r.QueryID, // reuse field for drill into query detail
		}
	}
	return items, descs
}

// isAuxBackend reports whether the backend_type is an evergreen auxiliary
// process that runs even when no client is connected. These are hidden by
// default (verbose=false) because they carry no query text, clutter the list,
// and never indicate user-visible problems.
// Client-facing types (client backend, parallel worker, autovacuum worker,
// walsender, walreceiver, background worker) are always shown.
func isAuxBackend(backendType string) bool {
	switch backendType {
	case "walwriter", "checkpointer", "background writer",
		"autovacuum launcher", "logical replication launcher",
		"io worker", "startup", "archiver", "walsummarizer",
		"slotsync worker":
		return true
	}
	return false
}

// visibleActRows filters rows for the current verbose setting. When verbose is
// true every row is kept. When false, two kinds of noise are dropped so the
// list focuses on backends that are actually doing (or blocking) work:
//   - auxiliary/background-only backends (walwriter, checkpointer, …); and
//   - genuinely idle client backends (state == "idle"), which are merely parked
//     on Client/ClientRead waiting for the next statement.
//
// The "all" filter is the explicit "show idle too" mode, so idle is kept there
// regardless of verbose. idle-in-transaction is always shown — it can hold
// locks. The true idle count is still reported in the header from actSummary.
func visibleActRows(rows []pg.ActivityRow, verbose bool, filter pg.ActivityFilter) []pg.ActivityRow {
	if verbose {
		return rows
	}
	hideIdle := filter != pg.ActivityAll
	out := rows[:0:0] // reuse backing array only after append
	for _, r := range rows {
		if isAuxBackend(r.BackendType) {
			continue
		}
		if hideIdle && r.State == "idle" {
			continue
		}
		out = append(out, r)
	}
	return out
}

// rebuildActivityItems rebuilds the generic-table items for the activity screen
// from its cached actRows, applying the current verbose filter. Call this
// whenever actVerbose changes or a fresh snapshot arrives so the two code paths
// share exactly the same projection + sort logic.
func (m *Model) rebuildActivityItems(s *screen) {
	rows := visibleActRows(s.actRows, s.actVerbose, s.actFilter)
	ctx := actCtx{hosts: s.actHosts, proc: m.actProcStats, toast: s.actToast}
	items, descs := m.buildActivityItems(rows, ctx)
	s.actCols = descs
	s.diagCols = actDiagColumnsFrom(descs)
	s.diagBarCol = -1 // no headline bar on the activity table
	m.syncActSort(s, descs)
	s.items = items
	s.diagMetricsDirty = true
	m.applySort(s)
}

// syncActSort maps the stable actSortColID to the projected index diagSortCol.
// If the active sort column was hidden, falls back to query_age desc, then
// first column. Writes the resolved id back to m.actSortColID.
func (m *Model) syncActSort(s *screen, descs []actColDesc) {
	if i := indexOfActCol(descs, m.actSortColID); i >= 0 {
		s.diagSortCol = i
		return
	}
	// Preferred default: query_age descending (longest-running first).
	if i := indexOfActCol(descs, actColQueryAge); i >= 0 {
		s.diagSortCol = i
		s.sortDesc = true
		m.actSortColID = actColQueryAge
		return
	}
	s.diagSortCol = 0
	if len(descs) > 0 {
		m.actSortColID = descs[0].id
	}
}
