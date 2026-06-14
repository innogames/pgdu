package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

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
	actColQuery       actColID = "query"
)

// actCtx carries per-build inputs the activity column cells need beyond the row
// itself: the per-screen resolved hostname map.
type actCtx struct {
	hosts map[string]string // IP → resolved hostname (from actHosts on screen)
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
				return pg.DiagCell{Display: fmt.Sprintf("%d", r.PID), Num: float64(r.PID), HasNum: true}
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
		{id: actColState, name: "state", kind: pg.DiagText, defaultOn: true,
			desc: "backend state: active, idle, idle in transaction, …",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell { return pg.DiagCell{Display: r.State} }},
		{id: actColWait, name: "wait", kind: pg.DiagText, defaultOn: true,
			desc: "wait event (e.g. Lock/relation, IO/DataFileRead) or blank when not waiting",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell {
				w := strings.TrimSpace(r.WaitEventType + "/" + r.WaitEvent)
				w = strings.Trim(w, "/")
				return pg.DiagCell{Display: w}
			}},
		{id: actColQueryAge, name: "query_age", kind: pg.DiagCostGraded, defaultOn: true,
			desc: "elapsed time since the current statement started (now() - query_start)",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell {
				if r.QueryAgeMs <= 0 {
					return pg.DiagCell{Display: "—"}
				}
				return pg.DiagCell{Display: fmtMs(r.QueryAgeMs), Num: r.QueryAgeMs, HasNum: true}
			}},
		{id: actColXactAge, name: "xact_age", kind: pg.DiagCostGraded,
			desc: "elapsed time since the current transaction started (now() - xact_start)",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell {
				if r.XactAgeMs <= 0 {
					return pg.DiagCell{Display: "—"}
				}
				return pg.DiagCell{Display: fmtMs(r.XactAgeMs), Num: r.XactAgeMs, HasNum: true}
			}},
		{id: actColStateAge, name: "state_age", kind: pg.DiagFloat,
			desc: "elapsed time since the state last changed (now() - state_change)",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell {
				if r.StateAgeMs <= 0 {
					return pg.DiagCell{Display: "—"}
				}
				d := time.Duration(r.StateAgeMs * float64(time.Millisecond))
				return pg.DiagCell{Display: relativeAge(d), Num: r.StateAgeMs, HasNum: true}
			}},
		{id: actColBackendXid, name: "xid", kind: pg.DiagText,
			desc: "transaction ID held by this backend (backend_xid); empty when not in a transaction",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell { return pg.DiagCell{Display: r.BackendXid} }},
		{id: actColBackendXmin, name: "xmin", kind: pg.DiagText,
			desc: "oldest transaction whose row versions this backend may still need (backend_xmin)",
			cell: func(r pg.ActivityRow, _ actCtx) pg.DiagCell { return pg.DiagCell{Display: r.BackendXmin} }},
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

// buildActivityItems converts ActivityRows into generic-table rows. It returns
// the items and the projected column descriptors (parallel to each item's cells).
func (m *Model) buildActivityItems(rows []pg.ActivityRow, hosts map[string]string) ([]item, []actColDesc) {
	descs := m.visibleActCols()
	ctx := actCtx{hosts: hosts}
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
