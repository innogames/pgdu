package tui

import (
	"fmt"
	"maps"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// settleLoad finishes the common *LoadedMsg prologue after the caller's
// identity guard: flips loading→loaded, promotes a missing-extension error to
// the blocking install prompt (stop=true — the caller returns cmd right away),
// and otherwise records the error on the screen.
func settleLoad(s *screen, err error, reason string) (cmd tea.Cmd, stop bool) {
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(err); ext != nil {
		return setExtensionPrompt(s, ext, reason), true
	}
	s.err = err
	return nil, false
}

func (m *Model) onDatabasesLoaded(msg databasesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelDatabases)
	if s == nil {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = s.items[:0]
	for _, d := range msg.dbs {
		s.items = append(s.items, item{name: d.Name, size: d.SizeBytes, hasChildren: true, data: d})
	}
	m.applySort(s)
	// Single-database fast path: with only one connectable database, skip the
	// one-row picker and drill straight in (its schemas, the top-queries table
	// for toolQueries, or the diagnostic result for toolTools). Back/Esc then
	// returns to the tool menu (or diagnostic list).
	if msg.err == nil && len(msg.dbs) == 1 && s == m.top() {
		if s.tool == toolTools && s.diag != nil {
			m.stack[len(m.stack)-1] = diagnosticResultScreen(s.diag, msg.dbs[0].Name, false)
		} else {
			m.stack = append(m.stack[:len(m.stack)-1], databaseChildScreens(s.tool, msg.dbs[0].Name)...)
		}
		return m.loadCurrent()
	}
	// In the diagnostics tool, offer running the query across every database as
	// a synthetic row pinned to the top of the picker.
	if msg.err == nil && s.tool == toolTools && s.diag != nil && len(msg.dbs) > 1 {
		// Sized as the sum of all listed databases so the row reads as a real
		// total and sorts to the top on size↓ without pinning.
		var total int64
		for _, d := range msg.dbs {
			total += d.SizeBytes
		}
		allRow := item{name: "(all databases)", size: total, hasChildren: true, data: allDBsChoice{}}
		s.items = append([]item{allRow}, s.items...)
		s.itemsRev++ // items no longer match the applySort order; invalidate the filter cache
	}
	return nil
}

func (m *Model) onSchemasLoaded(msg schemasLoadedMsg) tea.Cmd {
	s := m.findLevel(levelSchemas)
	if s == nil || s.db != msg.db {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	// Drop schemas with no tables (relkind r/m/p): every tool that drills
	// through a schema operates on tables, so an empty namespace — e.g. an
	// extension's schema like pg_repack — is a dead end in the picker.
	var schemas []pg.Schema
	for _, sc := range msg.schemas {
		if sc.TableCount > 0 {
			schemas = append(schemas, sc)
		}
	}
	s.items = s.items[:0]
	for _, sc := range schemas {
		s.items = append(s.items, item{
			name: sc.Name, size: sc.SizeBytes, hasChildren: true,
			tableCount: sc.TableCount, hasTableCount: true, data: sc,
		})
	}
	m.applySort(s)
	// Single-schema fast path: skip the one-row schema picker by replacing it
	// in-place. Back/Esc then returns to the database list, not a dead-end schema view.
	if msg.err == nil && len(schemas) == 1 && s == m.top() {
		m.stack[len(m.stack)-1] = schemaChildScreen(s.tool, schemas[0])
		return m.loadCurrent()
	}
	return nil
}

func (m *Model) onTablesLoaded(msg tablesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelTables)
	if s == nil || s.db != msg.db || s.schema != msg.schema {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = s.items[:0]
	for _, t := range msg.tables {
		s.items = append(s.items, tableToItem(t, s.tool))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onPartsLoaded(msg partsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelParts)
	if s == nil || s.table.OID != msg.table.OID {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = s.items[:0]
	for _, p := range msg.parts {
		s.items = append(s.items, partToItem(p))
	}
	m.applySort(s)
	// Bloat is always measured: the pgstattuple scan starts as soon as the
	// parts are listed and fills the bloat columns in when it lands.
	if msg.err == nil {
		s.parts.bloatScanning = true
		return m.fillBloatCmd(msg.table, msg.parts)
	}
	return nil
}

// onDiskTableResolved completes the disk-usage jump from the top-queries views:
// the placeholder parts screen pushed by the key handler gets the resolved table
// (so loadCurrent can load its parts), or, when the name doesn't resolve to a
// real relation, the placeholder is popped and the failure shown as a notice.
func (m *Model) onDiskTableResolved(msg diskTableResolvedMsg) tea.Cmd {
	s := m.top()
	// Stale guard: only act on the placeholder we pushed (top, levelParts, no
	// table resolved yet). If the user navigated on, drop the result.
	if s == nil || s.level != levelParts || s.table.OID != 0 {
		return nil
	}
	if msg.err != nil {
		m.stack = m.stack[:len(m.stack)-1]
		m.notice = fmt.Sprintf("no disk usage for %q: %s", msg.name, errText(msg.err))
		return nil
	}
	s.table = msg.table
	s.schema = msg.table.Schema
	s.db = msg.table.DB
	s.title = msg.table.Name
	return m.loadCurrent()
}

func (m *Model) onColumnsLoaded(msg columnsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelColumns)
	if s == nil || s.table.OID != msg.tableOID {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = s.items[:0]
	for _, col := range msg.columns {
		s.items = append(s.items, columnToItem(col))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onBloatFilled(msg bloatFilledMsg) tea.Cmd {
	s := m.findLevel(levelParts)
	if s == nil || s.table.OID != msg.table.OID {
		return nil
	}
	s.parts.bloatScanning = false
	if msg.err != nil {
		s.err = msg.err
		return nil
	}
	// applySort reorders s.items after partsLoadedMsg, so indexing by the
	// original msg.parts position is wrong. Match by name (heap/toast and
	// each index name are unique within a table).
	byName := make(map[string]pg.Part, len(msg.parts))
	for _, p := range msg.parts {
		byName[p.Name] = p
	}
	for i := range s.items {
		if p, ok := byName[s.items[i].name]; ok {
			s.items[i].bloat = p.WastedBytes
			s.items[i].hasBloat = p.HasBloat
		}
	}
	m.applySort(s)
	return nil
}

func (m *Model) onExtStatus(msg extStatusMsg) tea.Cmd {
	// Dispatched by (level, ext): each consumer surfaces its own prompt or
	// flips its own ready flag. Anything not listed here is ignored, since
	// the same probe Cmd may run from multiple screens.
	switch msg.ext {
	case extPgStatTuple:
		s := m.findLevel(levelParts)
		if s == nil || s.db != msg.db {
			return nil
		}
		if msg.err == nil && msg.status.Available && !msg.status.Installed {
			s.extPrompt = &extPrompt{
				name:        msg.ext,
				db:          msg.db,
				installable: true,
				reason:      extPromptReasonPgStatTuple,
				blocking:    false,
			}
		}
	}
	return nil
}

func (m *Model) onExtInstalled(msg extInstalledMsg) tea.Cmd {
	// Find the screen that asked for this install. We don't carry the
	// level in the message — just match on prompt name + db.
	for _, sc := range m.stack {
		if sc.extPrompt != nil && sc.extPrompt.name == msg.ext && sc.extPrompt.db == msg.db {
			sc.installing = false
			if msg.err != nil {
				sc.extPrompt.err = msg.err
				return nil
			}
			sc.extPrompt = nil
			// Re-enter the current screen so the (now-working) extension
			// is used. Only meaningful when the install was on the
			// currently active screen — otherwise the stale data on the
			// background screen will refresh next time the user revisits.
			if sc == m.top() {
				return m.loadCurrent()
			}
			return nil
		}
	}
	return nil
}

func (m *Model) onDescribeLoaded(msg describeLoadedMsg) tea.Cmd {
	s := m.findLevel(levelDescribe)
	if s == nil {
		return nil
	}
	// Guard against stale messages: accept when this is the first load
	// (s.describe == nil) or when the OID matches a refresh.
	if s.desc.info != nil && s.desc.info.OID != msg.oid {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.desc.info = msg.desc
	// The name-resolved push path leaves s.table unset; adopt the resolved
	// table so `p` (page inspector) works on every table describe.
	if msg.err == nil && msg.table.OID != 0 {
		s.table = msg.table
	}
	// A cross-database resolve may have landed somewhere other than the
	// connection database; the screen follows so the crumb names the right
	// database and the buffer footprint / page inspector query the right pool.
	if msg.err == nil && msg.db != "" {
		s.db = msg.db
	}
	// (Re)load the cache-footprint section for table describes — but only while
	// detail mode is showing it: the plain view never scans pg_buffercache (the
	// `d` toggle issues the first load instead). Triggering here — rather than
	// at push time — covers refresh-with-detail-open uniformly and gives us the
	// resolved OID even when the screen was pushed by table name. Reset the
	// prior section state first so a refresh doesn't show stale figures. A
	// scan already in flight is left to finish and reused: its result is at
	// most a few seconds older than what a second concurrent scan would give.
	s.desc.buf = nil
	s.desc.bufErr = nil
	if s.desc.detail && msg.err == nil && msg.desc != nil &&
		msg.desc.Kind == pg.DescribeTable && msg.desc.OID != 0 && !s.desc.bufLoading {
		s.desc.bufLoading = true
		return m.loadDescribeBuffersCmd(s.db, msg.desc.OID)
	}
	return nil
}

// onDescribeBuffersLoaded fills the describe-table screen's cache-footprint
// section. It's independent of onDescribeLoaded (which owns loading/loaded), so
// a missing pg_buffercache or a buffer error degrades only the section, never
// the columns. A missing extension becomes a non-blocking install prompt that
// the generic `i` key acts on; the section renders the affordance inline.
func (m *Model) onDescribeBuffersLoaded(msg describeBuffersLoadedMsg) tea.Cmd {
	s := m.findLevel(levelDescribe)
	// Match on the loaded description's OID (not s.table, which is unset on the
	// name-resolved push path) to reject stale results.
	if s == nil {
		return nil
	}
	s.desc.bufLoading = false
	if s.db != msg.db || s.desc.info == nil || s.desc.info.OID != msg.oid {
		// Stale: the screen moved to another table while this scan ran. The
		// describe load for the new table skipped its own scan because ours was
		// in flight, so issue it now if detail mode still wants the section.
		if s.desc.detail && s.desc.buf == nil && s.desc.bufErr == nil && s.extPrompt == nil &&
			s.desc.info != nil && s.desc.info.Kind == pg.DescribeTable && s.desc.info.OID != 0 {
			s.desc.bufLoading = true
			return m.loadDescribeBuffersCmd(s.db, s.desc.info.OID)
		}
		return nil
	}
	if ext := asMissingExt(msg.err); ext != nil {
		s.extPrompt = &extPrompt{
			name:        ext.Extension,
			db:          ext.DB,
			installable: ext.Installable,
			reason:      extPromptReasonBufferCache,
			blocking:    false,
		}
		return nil
	}
	s.desc.bufErr = msg.err
	if msg.err == nil {
		stat := msg.stat
		s.desc.buf = &stat
	}
	return nil
}

func (m *Model) onReindexDone(msg reindexDoneMsg) tea.Cmd {
	s := m.findLevel(levelParts)
	if s == nil || s.table.OID != msg.tableOID {
		return nil
	}
	// Clearing reindexing stops the progress-poll tick on its next fire.
	s.reindex.running = ""
	s.reindex.prog = nil
	s.reindex.pctMax = 0
	if msg.err != nil {
		s.reindex.err = msg.err
		return nil
	}
	s.reindex.err = nil
	// Refresh: the index has been rebuilt, so size and bloat have changed.
	return m.loadCurrent()
}

// onReindexTick re-polls the progress view while a REINDEX is in flight and
// reschedules itself; it stops (returns nil) once reindexing clears, so no
// stray tick outlives the rebuild.
func (m *Model) onReindexTick() tea.Cmd {
	s := m.findLevel(levelParts)
	if s == nil || s.reindex.running == "" {
		return nil
	}
	return tea.Batch(m.loadReindexProgressCmd(s.db, s.table.OID), m.reindexTick())
}

func (m *Model) onReindexProgress(msg reindexProgressMsg) tea.Cmd {
	s := m.findLevel(levelParts)
	if s == nil || s.table.OID != msg.tableOID || s.reindex.running == "" {
		return nil
	}
	s.reindex.prog = msg.row
	if msg.row != nil {
		// OverallPct is -1 for unmapped phases; max() also absorbs that, so
		// the bar simply holds until a known phase reports again.
		s.reindex.pctMax = max(s.reindex.pctMax, msg.row.OverallPct())
	}
	return nil
}

func (m *Model) onDiagnosticLoaded(msg diagnosticLoadedMsg) tea.Cmd {
	s := m.findLevel(levelDiagnosticResult)
	if s == nil || s.diag == nil || s.diag.Key != msg.key {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = s.items[:0]
	if msg.err != nil || msg.result == nil {
		return nil
	}
	// Retain the full result and project it through the column-visibility
	// selection; rebuildDiagItems also resolves bar/sort indices and the footer.
	s.diagResult = msg.result
	m.rebuildDiagItems(s)
	return nil
}

func errText(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

func (m *Model) onMaintLoaded(msg maintLoadedMsg) tea.Cmd {
	s := m.findLevel(levelMaintenance)
	if s == nil || s.db != msg.db {
		return nil
	}
	s.loading = false
	s.loaded = true
	if msg.err != nil {
		s.maintenance.err = msg.err
		return nil
	}
	s.maintenance.err = nil
	s.maintenance.prev = s.maintenance.info
	s.maintenance.info = msg.info
	if s.maintenance.first == nil {
		s.maintenance.first = msg.info
	}
	s.maintenance.refreshAdvice()
	return nil
}

// onMaintSchemaLoaded lands the catalog sweep; the recommendations are
// re-derived so the schema findings join the action rows.
func (m *Model) onMaintSchemaLoaded(msg maintSchemaLoadedMsg) tea.Cmd {
	s := m.findLevel(levelMaintenance)
	if s == nil || s.db != msg.db {
		return nil
	}
	s.maintenance.schemaLoading = false
	s.maintenance.schema = msg.health
	s.maintenance.refreshAdvice()
	return nil
}

// onMaintTick re-samples the overview while it is the top screen and re-arms
// the tick; navigating away (or cycling the cadence off) ends the loop so a
// later re-entry starts a fresh one.
func (m *Model) onMaintTick() tea.Cmd {
	top := m.top()
	if top.level != levelMaintenance {
		m.maintTicking = false
		return nil
	}
	next := m.maintTick()
	if next == nil {
		m.maintTicking = false
		return nil
	}
	return tea.Batch(m.loadMaintenanceCmd(top.db), next)
}

func (m *Model) onSettingsLoaded(msg settingsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelSettings)
	if s == nil || s.db != msg.db {
		return nil
	}
	s.loading = false
	s.loaded = true
	if msg.err != nil {
		s.err = msg.err
		return nil
	}
	s.maintenance.settingRows = msg.rows
	s.items = make([]item, len(msg.rows))
	for i, r := range msg.rows {
		detail := r.Category
		if r.ShortDesc != "" {
			detail = r.ShortDesc
		}
		s.items[i] = item{name: r.Name, detail: detail, data: r}
	}
	return nil
}

func (m *Model) onMaintResetDone(msg maintResetDoneMsg) tea.Cmd {
	s := m.findLevel(levelMaintenance)
	if s == nil {
		return nil
	}
	s.maintenance.pendingReset = ""
	if msg.err != nil {
		// Surface the error as a transient notice so the dashboard stays visible.
		m.notice = fmt.Sprintf("reset %s failed: %s", maintResetTarget(msg.which), msg.err)
		return nil
	}
	m.notice = maintResetTarget(msg.which) + " reset"
	// Reload the maintenance view so the updated capacity numbers are visible.
	return m.loadCurrent()
}

func (m *Model) onTableStatsLoaded(msg tableStatsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelParts)
	if s == nil || s.table.OID != msg.table.OID {
		return nil
	}
	s.parts.tableStatsErr = msg.err
	if msg.err == nil {
		s.parts.tableStats = msg.stats
	}
	return nil
}

func (m *Model) onVacuumStarted(msg vacuumStartedMsg) tea.Cmd {
	s := m.findLevel(levelParts)
	if s == nil {
		return nil
	}
	s.parts.pendingVacuum = false
	m.vacuum = vacuumState{
		table:   msg.table,
		started: time.Now(),
		running: true,
		follow:  true,
	}
	return waitVacuumLineCmd(msg.lineCh, msg.doneCh)
}

func (m *Model) onVacuumLine(msg vacuumLineMsg) tea.Cmd {
	if !m.vacuum.running {
		return nil
	}
	m.vacuum.buf = append(m.vacuum.buf, msg.line)
	if m.vacuum.follow {
		m.vacuum.offset = len(m.vacuum.buf)
	}
	return waitVacuumLineCmd(msg.lineCh, msg.doneCh)
}

func (m *Model) onVacuumDone(msg vacuumDoneMsg) tea.Cmd {
	m.vacuum.running = false
	m.vacuum.finished = time.Now()
	m.vacuum.err = msg.err
	s := m.findLevel(levelParts)
	if s == nil {
		return nil
	}
	// Reload table stats now that vacuum has finished so the row counts are fresh.
	return m.loadTableStatsCmd(s.table)
}

// onFixLine appends one streamed line of a running suggested-fix to its
// screen's output buffer. The overlay is modal while the run is in flight, so
// the diagnostic-result screen is still on the stack; if the run somehow
// outlived it (or was reset), the line is dropped and the wait not re-armed.
func (m *Model) onFixLine(msg fixLineMsg) tea.Cmd {
	f := m.runningDiagFix()
	if f == nil {
		return nil
	}
	f.buf = append(f.buf, msg.line)
	if f.follow {
		f.offset = len(f.buf)
	}
	return waitFixLineCmd(msg.lineCh, msg.doneCh)
}

// onFixDone closes out a suggested-fix run. The result table is not reloaded
// here — the overlay stays up so the output can be read — but on dismiss
// (handleDiagFixKey) a successful run refreshes it, since its rows are stale
// by definition.
func (m *Model) onFixDone(msg fixDoneMsg) tea.Cmd {
	f := m.runningDiagFix()
	if f == nil {
		return nil
	}
	f.running = false
	f.finished = time.Now()
	f.err = msg.err
	return nil
}

// runningDiagFix returns the in-flight fix run of the nearest diagnostic-result
// screen, or nil when none is running.
func (m *Model) runningDiagFix() *diagFixRun {
	s := m.findLevel(levelDiagnosticResult)
	if s == nil || s.diagFix == nil || !s.diagFix.running {
		return nil
	}
	return s.diagFix
}

// ── Activity handlers ─────────────────────────────────────────────────────────

func (m *Model) onActivityLoaded(msg activityLoadedMsg) tea.Cmd {
	s := m.findLevel(levelActivity)
	if s == nil || s.db != msg.db {
		return nil
	}
	s.loading = false
	s.loaded = true
	if msg.err != nil {
		s.act.err = msg.err
		return nil
	}
	s.act.err = nil
	s.act.rows = msg.rows
	s.act.summary = msg.summary
	s.act.progressPct = clampProgressMarks(s.act.progressPct, msg.progress)

	if s.act.hosts == nil {
		s.act.hosts = make(map[string]string)
	}
	if s.act.toast == nil {
		s.act.toast = make(map[string]string)
	}
	m.rebuildActivityItems(s)
	// Feed the wait-event profile: every snapshot becomes one histogram bucket,
	// whether or not the profile screen is open.
	m.pushWaitBucket(msg.rows)

	// Collect PIDs for proc sampling, IPs for DNS resolution, and pg_toast.*
	// targets for owner resolution — all in the background so none block refresh.
	pids := make([]int32, len(msg.rows))
	var unresolved []string
	var toastReqs []toastResolveReq
	for i, r := range msg.rows {
		pids[i] = r.PID
		if r.ClientAddr != "" {
			if _, ok := s.act.hosts[r.ClientAddr]; !ok {
				unresolved = append(unresolved, r.ClientAddr)
			}
		}
		// A TOAST relation's OID is database-local, so resolution needs the row's
		// own datname; skip rows without one (walsenders, etc.) rather than guess.
		if r.Database != "" {
			if rn, ok := strings.CutPrefix(pg.MainTable(r.Query), "pg_toast."); ok {
				if _, seen := s.act.toast[toastKey(r.Database, rn)]; !seen {
					toastReqs = append(toastReqs, toastResolveReq{db: r.Database, relname: rn})
				}
			}
		}
	}
	return tea.Batch(
		m.resolveActivityHostsCmd(unresolved),
		m.resolveActivityToastCmd(toastReqs),
		m.sampleProcStatsCmd(pids),
	)
}

func (m *Model) onTableOverviewLoaded(msg tableOverviewLoadedMsg) tea.Cmd {
	s := m.findLevel(levelTableStats)
	if s == nil || s.db != msg.db || s.schema != msg.schema {
		return nil
	}
	s.loading = false
	s.loaded = true
	if msg.err != nil {
		s.err = msg.err
		return nil
	}
	s.err = nil
	s.tbl.rows = msg.rows
	s.tbl.statsReset = msg.statsReset
	m.rebuildTableStatItems(s)
	return nil
}

func (m *Model) onActivityTick() tea.Cmd {
	// Keep the tick alive while the user is in the activity table, its
	// lock-tree child, or the progress monitor. Stop when they navigate fully
	// away so re-entry can start a fresh loop.
	top := m.top()
	if top.level != levelActivity && top.level != levelLockTree && top.level != levelProgress &&
		top.level != levelWaitProfile {
		m.activityTicking = false
		return nil
	}
	next := m.activityTick()
	if next == nil {
		// Refresh was cycled off while the tool was open.
		m.activityTicking = false
		return nil
	}
	switch top.level {
	case levelLockTree:
		return tea.Batch(m.loadLockTreeCmd(top.db), next)
	case levelProgress:
		return tea.Batch(m.loadProgressCmd(top.db), next)
	}
	return tea.Batch(m.loadActivityCmd(top.db, top.act.filter), next)
}

func (m *Model) onLockTreeLoaded(msg lockTreeLoadedMsg) tea.Cmd {
	s := m.findLevel(levelLockTree)
	if s == nil || s.db != msg.db {
		return nil
	}
	s.loading = false
	s.loaded = true
	if msg.err != nil {
		s.lock.err = msg.err
		return nil
	}
	s.lock.err = nil
	s.lock.nodes = msg.nodes
	m.rebuildLockTreeItems(s)
	return nil
}

func (m *Model) onProgressLoaded(msg progressLoadedMsg) tea.Cmd {
	s := m.findLevel(levelProgress)
	if s == nil || s.db != msg.db {
		return nil
	}
	s.loading = false
	s.loaded = true
	if msg.err != nil {
		s.progress.err = msg.err
		return nil
	}
	s.progress.err = nil
	s.progress.rows = msg.rows
	s.progress.pctMax = clampProgressMarks(s.progress.pctMax, msg.rows)
	m.rebuildProgressItems(s)
	return nil
}

func (m *Model) onActivityHosts(msg activityHostsMsg) tea.Cmd {
	s := m.findLevel(levelActivity)
	if s == nil {
		return nil
	}
	if s.act.hosts == nil {
		s.act.hosts = make(map[string]string)
	}
	maps.Copy(s.act.hosts, msg.hosts)
	if s.act.rows != nil {
		m.rebuildActivityItems(s)
	}
	return nil
}

func (m *Model) onActivityToast(msg activityToastMsg) tea.Cmd {
	s := m.findLevel(levelActivity)
	if s == nil {
		return nil
	}
	if s.act.toast == nil {
		s.act.toast = make(map[string]string)
	}
	maps.Copy(s.act.toast, msg.owners)
	if s.act.rows != nil {
		m.rebuildActivityItems(s)
	}
	return nil
}

func (m *Model) onBackendAction(msg backendActionMsg) tea.Cmd {
	// The action may have been fired from the activity table or the lock tree;
	// act on whichever is on top so the right list refreshes.
	s := m.findLevel(levelLockTree)
	if s == nil {
		s = m.findLevel(levelActivity)
	}
	if s == nil {
		return nil
	}
	s.act.pendingAction = ""
	s.act.pendingPID = 0
	s.act.pendingQuery = ""
	switch {
	case msg.err != nil:
		m.notice = fmt.Sprintf("%s %d failed: %s", msg.action, msg.pid, msg.err)
	case !msg.ok:
		m.notice = fmt.Sprintf("%s %d: backend not found or permission denied", msg.action, msg.pid)
	default:
		m.notice = fmt.Sprintf("%s sent to backend %d", msg.action, msg.pid)
	}
	// Refresh the list so the terminated/cancelled backend disappears or changes state.
	if s.level == levelLockTree {
		return m.loadLockTreeCmd(s.db)
	}
	return m.loadActivityCmd(s.db, s.act.filter)
}

// onActivityStatement lands the QueryStat fetched for a backend's query in the
// loading placeholder drillActivityStatement pushed, so the stack holds exactly
// one detail screen (the trail shows one crumb, Back returns to activity). If
// the user already navigated away from the placeholder, push a fresh screen
// the way the placeholder-less path always did.
func (m *Model) onActivityStatement(msg activityStatementMsg) tea.Cmd {
	s := m.findLevel(levelActivity)
	if s == nil {
		return nil
	}
	top := m.top()
	placeholder := top.level == levelStatementDetail && top.loading && top.stat.detail == nil
	if msg.err != nil {
		if placeholder {
			m.stack = m.stack[:len(m.stack)-1]
		}
		m.notice = fmt.Sprintf("load query detail: %s", msg.err)
		return nil
	}
	if placeholder {
		top.db = msg.db
		top.stat.detail = msg.qs
		return m.loadCurrent()
	}
	next := &screen{
		level: levelStatementDetail,
		title: "query",
		tool:  s.tool,
		db:    msg.db,
		stat:  stmtState{detail: msg.qs}}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}
