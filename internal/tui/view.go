package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

func (m *Model) View() string {
	if m.width == 0 {
		return ""
	}
	s := m.top()

	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n")

	// Three header lines, the blank above the footer, and the help row. The
	// budget must be exact: Bubble Tea drops overflowing lines from the *top*,
	// so one line too many hides the title/breadcrumb line, not the footer.
	contentHeight := max(m.height-5, 3)

	var rankByOID map[uint32]int
	if s.level == levelBufferTables && (s.buf.summary != nil || s.buf.summaryErr != nil) {
		var summary string
		summary, rankByOID = m.renderBufferSummary(s)
		b.WriteString(summary)
		b.WriteString("\n")
		contentHeight -= strings.Count(summary, "\n") + 1
	}

	if s.level == levelShmem && s.loaded && s.err == nil && len(s.items) > 0 {
		summary := m.renderShmemSummary(s)
		b.WriteString(summary)
		b.WriteString("\n")
		contentHeight -= strings.Count(summary, "\n") + 1
	}

	if s.level == levelWAL && (s.extPrompt == nil || !s.extPrompt.blocking) &&
		(s.wal.summary != nil || s.wal.summaryErr != nil) {
		summary := m.renderWALSummary(s)
		b.WriteString(summary)
		b.WriteString("\n")
		contentHeight -= strings.Count(summary, "\n") + 1
	}

	if s.level == levelWALRecords && (s.extPrompt == nil || !s.extPrompt.blocking) &&
		len(s.wal.recTypeStats) > 0 {
		stats := m.renderWALRecTypeStats(s)
		b.WriteString(stats)
		b.WriteString("\n")
		contentHeight -= strings.Count(stats, "\n") + 1
	}

	if s.level == levelStatements && (s.extPrompt == nil || !s.extPrompt.blocking) {
		hdr := m.renderStatementsHeader(s)
		b.WriteString(hdr)
		b.WriteString("\n")
		contentHeight -= strings.Count(hdr, "\n") + 1
	}

	if s.level == levelActivity && s.loaded && s.act.err == nil {
		hdr := m.renderActivityHeader(s)
		b.WriteString(hdr)
		b.WriteString("\n")
		contentHeight -= strings.Count(hdr, "\n") + 1
	}

	if s.level == levelPgBouncers && s.loaded && len(s.items) > 0 {
		if hint := m.renderPgbListHint(s); hint != "" {
			b.WriteString(hint)
			b.WriteString("\n")
			contentHeight--
		}
	}
	if s.level == levelPgBouncer && s.loaded {
		hdr := m.renderPgBouncerHeader(s)
		b.WriteString(hdr)
		b.WriteString("\n")
		contentHeight -= strings.Count(hdr, "\n") + 1
	}
	if s.level == levelPgBouncerShow && s.loaded {
		hdr := m.renderPgbShowHeader(s)
		b.WriteString(hdr)
		b.WriteString("\n")
		contentHeight -= strings.Count(hdr, "\n") + 1
	}

	if (s.level == levelLogs || s.level == levelLogGroup || s.level == levelLogEntry) && s.loaded {
		if hdr := m.renderLogHeader(s); hdr != "" {
			b.WriteString(hdr)
			b.WriteString("\n")
			contentHeight -= strings.Count(hdr, "\n") + 1
		}
	}

	// B-tree page/tuple views carry an index-context banner (key columns, and on
	// the page list the metapage summary). Suppressed under a blocking
	// extension prompt, which takes over the whole content area.
	if (s.level == levelIndexPages || s.level == levelIndexTuples) &&
		(s.extPrompt == nil || !s.extPrompt.blocking) {
		if banner := m.renderIndexKeyBanner(s); banner != "" {
			b.WriteString(banner)
			b.WriteString("\n")
			contentHeight -= strings.Count(banner, "\n") + 1
		}
	}

	// Non-blocking prompts (hints) render above the list and consume one
	// line of the content area. Blocking prompts take over the whole
	// content area in the switch below. levelDescribe is excluded: it renders
	// the pg_buffercache install affordance inside its cache-footprint section.
	if s.extPrompt != nil && !s.extPrompt.blocking && s.level != levelDescribe {
		b.WriteString(m.renderExtHint(s))
		b.WriteString("\n")
		contentHeight--
	}

	if banner := m.renderReindexBanner(s); banner != "" {
		b.WriteString(banner)
		b.WriteString("\n")
		contentHeight--
	}

	if banner := m.renderVacuumBanner(s); banner != "" {
		b.WriteString(banner)
		b.WriteString("\n")
		contentHeight--
	}

	if s.level == levelActivity {
		if banner := activityPendingBanner(s, m.width); banner != "" {
			b.WriteString(banner)
			b.WriteString("\n")
			contentHeight -= strings.Count(banner, "\n") + 1
		}
	}

	if line := m.renderFilterLine(s); line != "" {
		b.WriteString(line)
		b.WriteString("\n")
		contentHeight--
	}

	if line := m.renderSeekLine(s); line != "" {
		b.WriteString(line)
		b.WriteString("\n")
		contentHeight--
	}

	// Reserve a line for the colour legend (rendered after the list, before
	// the help row) on levels whose bars carry more than one colour. The parts
	// level owns its own legend (rendered directly beneath its list by
	// renderPartsLevel), so it's excluded from the bottom-of-screen legend.
	legend := renderLegend(s)
	if s.level == levelParts {
		legend = ""
	}
	if legend != "" {
		contentHeight--
	}

	switch {
	case m.actTable.showCfg && s.level == levelActivity:
		b.WriteString(m.renderActColumnConfig(s, contentHeight))
	case s.level == levelLogs && m.logTableFor(s.log.view).showCfg:
		b.WriteString(m.renderLogColumnConfig(s.log.view, contentHeight))
	case (m.stmtTable.showCfg || m.stmtGroupTable.showCfg) && s.level == levelStatements:
		b.WriteString(m.renderColumnConfig(s, contentHeight))
	case m.tblTable.showCfg && s.level == levelTableStats:
		b.WriteString(m.renderTblColumnConfig(s, contentHeight))
	case m.showDiagColumnConfig && (s.level == levelDiagnosticResult || s.level == levelPgBouncerShow):
		b.WriteString(m.renderDiagColumnConfig(s, contentHeight))
	case m.showTupleColumnConfig && s.level == levelHeapTuples:
		b.WriteString(m.renderTupleColumnConfig(s, contentHeight))
	case m.showTupleLayout && s.level == levelHeapTuples:
		b.WriteString(m.renderTupleLayout(s, contentHeight))
	case m.showDiagQuery && s.diagForShowQuery() != nil:
		b.WriteString(m.renderDiagQuery(s, contentHeight))
	case m.showDiagFix && s.level == levelDiagnosticResult && s.diagFix != nil:
		b.WriteString(m.renderDiagFix(s, contentHeight))
	case m.showInfo && m.hasInfoOverlay(s):
		// The ? reference overlays scroll through scrollWindow — some (e.g. the
		// maintenance reference) are taller than the screen. renderInfoOverlay
		// dispatches to the per-level body; m.infoOffset is the scroll position.
		b.WriteString(scrollWindow(m.renderInfoOverlay(s, contentHeight), &m.infoOffset, contentHeight))
	case s.extPrompt != nil && s.extPrompt.blocking:
		b.WriteString(m.renderExtPrompt(s, contentHeight))
	case s.loading || !s.loaded:
		fmt.Fprintf(&b, "  %s loading %s…\n", m.spinner.View(), s.title)
		for i := 1; i < contentHeight; i++ {
			b.WriteString("\n")
		}
	case s.err != nil:
		b.WriteString(styleErr.Render("  error: "+s.err.Error()) + "\n")
		for i := 1; i < contentHeight; i++ {
			b.WriteString("\n")
		}
	case len(s.items) == 0 && s.level != levelDescribe && s.level != levelDiagnosticResult &&
		s.level != levelStatements && s.level != levelStatementDetail &&
		s.level != levelStatementResult && s.level != levelSnapshots &&
		s.level != levelBufferDetail && s.level != levelMaintenance && s.level != levelSettings &&
		s.level != levelActivity && s.level != levelLockTree && s.level != levelTableStats &&
		s.level != levelProgress && s.level != levelWaitProfile &&
		s.level != levelLogs && s.level != levelLogFiles && s.level != levelLogGroup && s.level != levelLogEntry &&
		s.level != levelPgBouncers && s.level != levelPgBouncer && s.level != levelPgBouncerShow:
		// levelDescribe never populates items — it renders from s.describe.
		// levelDiagnosticResult and levelStatementResult with 0 items mean the
		// query returned no rows, which is valid; fall through to the renderer
		// which shows "(no rows)". levelStatements (empty = no activity in the
		// window yet) and levelStatementDetail (renders from s.statDetail) are
		// likewise valid with no items.
		b.WriteString("  (no items)\n")
		for i := 1; i < contentHeight; i++ {
			b.WriteString("\n")
		}
	default:
		switch s.level {
		case levelTools:
			b.WriteString(m.renderToolPicker(s, contentHeight))
		case levelSchemas:
			b.WriteString(m.renderListWithFooter(s, contentHeight, m.renderSchemasTotals(s)))
		case levelTables:
			b.WriteString(m.renderListWithFooter(s, contentHeight, m.renderTablesTotals(s)))
		case levelBufferTables:
			b.WriteString(m.renderBufferList(s, contentHeight, rankByOID))
		case levelBufferDetail:
			b.WriteString(m.renderBufferDetail(s, contentHeight))
		case levelShmem:
			b.WriteString(m.renderShmemList(s, contentHeight))
		case levelHeapPages:
			b.WriteString(m.renderHeapPagesList(s, contentHeight))
		case levelHeapTuples:
			b.WriteString(m.renderHeapTuplesList(s, contentHeight))
		case levelRelations:
			b.WriteString(m.renderRelationsList(s, contentHeight))
		case levelIndexPages:
			switch s.pages.index.AccessMethod {
			case "gist":
				b.WriteString(m.renderGistPagesList(s, contentHeight))
			case "brin":
				b.WriteString(m.renderBrinPagesList(s, contentHeight))
			case "gin":
				b.WriteString(m.renderGinPagesList(s, contentHeight))
			default:
				b.WriteString(m.renderIndexPagesList(s, contentHeight))
			}
		case levelIndexTuples:
			switch s.pages.index.AccessMethod {
			case "gist":
				b.WriteString(m.renderGistTuplesList(s, contentHeight))
			case "brin":
				b.WriteString(m.renderBrinTuplesList(s, contentHeight))
			case "gin":
				b.WriteString(m.renderGinTuplesList(s, contentHeight))
			default:
				b.WriteString(m.renderIndexTuplesList(s, contentHeight))
			}
		case levelDescribe:
			b.WriteString(m.renderDescribe(s, contentHeight))
		case levelDiagnostics:
			b.WriteString(m.renderDiagnosticList(s, contentHeight))
		case levelDiagnosticResult:
			b.WriteString(m.renderDiagResult(s, contentHeight))
		case levelWAL:
			b.WriteString(m.renderWALList(s, contentHeight))
		case levelWALRecords:
			b.WriteString(m.renderWALRecordsList(s, contentHeight))
		case levelWALBlocks:
			b.WriteString(m.renderWALBlocksList(s, contentHeight))
		case levelWALRelBlocks:
			// Relation block-refs reuse the per-record block-refs renderer —
			// the payload is the same pg.WALBlockRef.
			b.WriteString(m.renderWALBlocksList(s, contentHeight))
		case levelWALBlockDetail:
			b.WriteString(m.renderWALBlockDetail(s, contentHeight))
		case levelStatements:
			// The top-queries table is a generic diagnostic-style table.
			b.WriteString(m.renderDiagResult(s, contentHeight))
		case levelStatementDetail:
			b.WriteString(m.renderStatementDetail(s, contentHeight))
		case levelStatementSamples:
			b.WriteString(m.renderStatementSamples(s, contentHeight))
		case levelStatementResult:
			// Executed-query rows reuse the generic result-table renderer.
			b.WriteString(m.renderDiagResult(s, contentHeight))
		case levelSnapshots:
			b.WriteString(m.renderStatementSnapshots(s, contentHeight))
		case levelParts:
			b.WriteString(m.renderPartsLevel(s, contentHeight))
		case levelMaintenance:
			b.WriteString(m.renderMaintenance(s, contentHeight))
		case levelSettings:
			b.WriteString(m.renderSettingsList(s, contentHeight))
		case levelActivity:
			// The activity table is a generic diagnostic-style table — same
			// renderer as levelStatements and levelDiagnosticResult.
			b.WriteString(m.renderDiagResult(s, contentHeight))
		case levelLockTree:
			b.WriteString(m.renderLockTree(s, contentHeight))
		case levelProgress:
			b.WriteString(m.renderProgress(s, contentHeight))
		case levelWaitProfile:
			b.WriteString(m.renderWaitProfile(s, contentHeight))
		case levelLogFiles:
			if s.diagCols != nil && len(s.items) > 0 {
				b.WriteString(m.renderDiagResult(s, contentHeight))
			} else {
				b.WriteString(m.renderLogFiles(s, contentHeight))
			}
		case levelPgBouncers:
			if s.diagCols != nil && len(s.items) > 0 {
				b.WriteString(m.renderDiagResult(s, contentHeight))
			} else {
				b.WriteString(m.renderPgBouncers(s, contentHeight))
			}
		case levelPgBouncer:
			b.WriteString(m.renderPgBouncerMenu(s, contentHeight))
		case levelPgBouncerShow:
			b.WriteString(m.renderDiagResult(s, contentHeight))
		case levelLogs:
			if s.log.view.table() && s.log.err == nil && s.log.report != nil {
				b.WriteString(m.renderDiagResult(s, contentHeight))
			} else {
				b.WriteString(m.renderLogGroups(s, contentHeight))
			}
		case levelLogGroup:
			if s.diagCols != nil {
				b.WriteString(m.renderDiagResult(s, contentHeight))
			} else {
				b.WriteString(m.renderLogGroup(s, contentHeight))
			}
		case levelLogEntry:
			b.WriteString(m.renderLogEntry(s, contentHeight))
		case levelTableStats:
			// The table overview is a generic diagnostic-style table too.
			b.WriteString(m.renderDiagResult(s, contentHeight))
		default:
			b.WriteString(m.renderList(s, contentHeight))
		}
	}

	if legend != "" {
		b.WriteString(legend)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	// Trim the footer to the current screen's bindings (disabled keys are
	// skipped by the help component), matching what handleKey will dispatch.
	m.keys.applyContext(m.top())
	b.WriteString(styleHelp.Render(m.help.View(m.keys)))
	return b.String()
}

// renderHeader draws the three fixed header lines: the pgdu chip with the
// breadcrumb trail, a rule, and the status row. The trail gets whatever
// width the chip leaves over.
func (m *Model) renderHeader() string {
	s := m.top()
	chip := styleHeader.Render(" pgdu ")
	line := chip + " " + renderTrail(m.crumbs(), m.width-displayWidth(chip)-1)
	return line + "\n" + styleMuted.Render(strings.Repeat("─", maxInt(m.width-1, 1))) + "\n" +
		"  " + m.renderStatus(s)
}

// renderStatus is the one-line status row under the header: sort mode, cursor
// position (e.g. "12/438") and per-level view state — scan progress, page
// window, LSN window, counter age, transient notice. Identity (what is being
// looked at) belongs to the breadcrumb trail above, never here.
func (m *Model) renderStatus(s *screen) string {
	sortLabel := s.sort.label(s.sortDesc)
	if s.diagCols != nil && s.diagSortCol < len(s.diagCols) {
		// Generic diagnostic-table: show the active column name and direction
		// instead of the sortMode label (which is meaningless here).
		arrow := "↑"
		if s.sortDesc {
			arrow = "↓"
		}
		sortLabel = s.diagCols[s.diagSortCol].Name + arrow
	}
	parts := []string{
		"sort: " + sortLabel,
		positionLabel(s),
	}
	if bs := bloatScanLabel(s); bs != "" {
		parts = append(parts, bs)
	}
	if pw := heapPageWindowLabel(s); pw != "" {
		parts = append(parts, pw)
	}
	if wl := walStatusLabel(s); wl != "" {
		parts = append(parts, wl)
	}
	if tr := tblStatsResetLabel(s); tr != "" {
		parts = append(parts, tr)
	}
	if m.notice != "" {
		parts = append(parts, styleSelected.Render(m.notice))
	}
	return strings.Join(parts, "  ·  ")
}

// walStatusLabel keeps the LSN window on the status row for the WAL levels
// whose header doesn't show it. The resource manager, record and relation are
// crumbs; the block-detail level adds the record type, which no crumb carries.
func walStatusLabel(s *screen) string {
	switch s.level {
	case levelWALRecords, levelWALRelBlocks:
		if s.wal.start != "" || s.wal.end != "" {
			return "window: " + shortLSN(s.wal.start) + "–" + shortLSN(s.wal.end)
		}
	case levelWALBlockDetail:
		if b := s.wal.blockRef; b != nil {
			return "record: " + b.StartLSN + "  ·  " + b.Rmgr + "/" + b.RecordType
		}
	}
	return ""
}

// crumbs is the breadcrumb trail as plain text, one entry per screen on the
// stack, so Back always removes exactly the last crumb. The root's crumb is the
// connection host; every other screen names what it shows from live state,
// falling back to its title and then to the level name so a crumb is never
// empty while a placeholder is still loading.
//
// Two decorations keep the trail self-sufficient without adding crumbs: a
// screen that switches tool mid-trail (describe → page inspector, an overview
// recommendation → lock tree, the single-database fast path that skips the
// picker) is prefixed with
// the tool name, and the first screen scoped to a database the trail hasn't
// named yet is suffixed with "(db)". Cluster-wide tools carry the connection
// database only as a handle, so they never get the suffix. Relations read the
// way psql prints them: bare, or "schema.name" when the trail hasn't spelled a
// non-default schema yet (crumbScope.qualify).
func (m *Model) crumbs() []string {
	host := m.hostLabel
	if host == "" {
		if host = m.target; host == "" {
			host = "server"
		}
	}
	parts := []string{host}
	var named crumbScope
	var prev *screen
	for _, sc := range m.stack {
		if sc.level == levelTools {
			prev = sc
			continue
		}
		text, names := crumbText(sc, prev, named)
		if text == "" {
			text = sc.title
		}
		if text == "" {
			text = levelLabel(sc.level)
		}
		switch {
		case names.db != "":
			named = names
		case dbScopedLevel(sc.level) && sc.db != "" && sc.db != named.db:
			text += " (" + sc.db + ")"
			named = crumbScope{db: sc.db, schema: names.schema}
		case names.schema != "":
			named.schema = names.schema
		}
		// Progress is a cross-link reachable from activity as well as the
		// system overview; stamping "system overview:" on it would mislead.
		switched := prev != nil && (prev.level == levelTools || sc.tool != prev.tool) && sc.level != levelProgress
		if switched && text != sc.tool.Name() {
			text = sc.tool.Name() + ": " + text
		}
		parts = append(parts, text)
		prev = sc
	}
	return parts
}

// crumbScope is what the trail has named so far — a database and, within it, a
// schema — so a crumb can leave out what an earlier one already says.
type crumbScope struct{ db, schema string }

// qualify names a relation the way psql prints it: bare when its schema is
// public (the default search_path entry, so it goes without saying) or the
// trail already names that schema in the same database, "schema.name"
// otherwise. A schema spelled under another database doesn't count.
func (n crumbScope) qualify(db, schema, name string) string {
	if name == "" || schema == "" || schema == "public" || (n.db == db && n.schema == schema) {
		return name
	}
	return schema + "." + name
}

// crumbText names what a screen shows, from live state; "" defers to the
// generic fallbacks in crumbs. named is what the trail says so far; names
// reports what this crumb adds — the database when the crumb is the database
// itself, the schema when the crumb spells (or stands for) one.
func crumbText(sc, prev *screen, named crumbScope) (text string, names crumbScope) {
	switch sc.level {
	case levelDatabases:
		if sc.diag != nil {
			// Per-database diagnostic: the picker asks "which db for <query>".
			return sc.diag.Title, names
		}
		return sc.tool.Name(), names
	case levelSchemas, levelStatements:
		return sc.db, crumbScope{db: sc.db}
	case levelTables, levelBufferTables, levelRelations, levelTableStats:
		if sc.db != named.db {
			// The single-schema fast path replaced the schema picker with this
			// screen, so it stands in for the database; its schema is left to
			// qualify the relations below it.
			return sc.db, crumbScope{db: sc.db}
		}
		return sc.schema, crumbScope{schema: sc.schema}
	case levelParts, levelHeapPages:
		return named.qualify(sc.db, sc.table.Schema, sc.table.Name), crumbScope{schema: sc.table.Schema}
	case levelColumns:
		return "heap", names
	case levelBufferDetail:
		if d := sc.buf.detail; d != nil {
			return named.qualify(sc.db, d.Schema, d.Name), crumbScope{schema: d.Schema}
		}
	case levelShmem:
		return "shmem", names
	case levelHeapTuples:
		text = fmt.Sprintf("page #%d", sc.pages.heapPageBlkno)
		if (prev == nil || prev.level != levelHeapPages) && sc.table.Name != "" {
			// Index → heap hop: the crumb before is an index page, so say
			// whose page this is.
			text = named.qualify(sc.db, sc.table.Schema, sc.table.Name) + " " + text
			names.schema = sc.table.Schema
		}
		return text, names
	case levelIndexPages:
		idx := sc.pages.index
		return named.qualify(sc.db, idx.Schema, idx.Name), crumbScope{schema: idx.Schema}
	case levelIndexTuples:
		text = fmt.Sprintf("page #%d", sc.pages.indexPageBlkno)
		switch {
		case sc.pages.indexPageLevel != nil:
			text += fmt.Sprintf(" L%d", *sc.pages.indexPageLevel)
		case sc.pages.indexPageType != "":
			text += " " + sc.pages.indexPageType
		}
		return text, names
	case levelDescribe:
		switch {
		case sc.table.Name != "":
			// Only table describes carry the table (pushed by OID or adopted
			// from the name-resolved load); an index describe keeps its title.
			return "describe " + named.qualify(sc.db, sc.table.Schema, sc.table.Name), crumbScope{schema: sc.table.Schema}
		case sc.desc.info != nil && sc.desc.info.Title != "":
			return "describe " + sc.desc.info.Title, names
		}
		return "describe", names
	case levelDiagnostics:
		return "tools", names
	case levelDiagnosticResult:
		if prev != nil && prev.level == levelDatabases && prev.diag != nil {
			// Reached through the database picker, whose crumb is already the
			// diagnostic's title: this crumb is the chosen scope.
			if sc.diagAllDBs {
				return "all databases", names
			}
			return sc.db, crumbScope{db: sc.db}
		}
		if sc.diag != nil {
			return sc.diag.Title, names
		}
	case levelWAL:
		return "wal", names
	case levelWALRecords:
		return sc.wal.rmgr, names
	case levelWALBlocks:
		if sc.wal.recLSN != "" {
			return "rec " + shortLSN(sc.wal.recLSN), names
		}
	case levelWALRelBlocks:
		return sc.wal.relLabel, names
	case levelWALBlockDetail:
		if b := sc.wal.blockRef; b != nil {
			return fmt.Sprintf("blk %d @ %s", b.BlockNumber, shortLSN(b.StartLSN)), names
		}
	case levelStatementDetail:
		if sc.stat.detail != nil {
			return fmt.Sprintf("query %d", sc.stat.detail.QueryID), names
		}
	case levelStatementSamples:
		return "values", names
	case levelStatementResult:
		return "result", names
	case levelSnapshots:
		return "snapshots", names
	case levelMaintenance:
		return "system overview", names
	case levelSettings:
		return "settings", names
	case levelProgress:
		return "progress", names
	case levelActivity:
		return "activity", names
	case levelLockTree:
		return "lock tree", names
	case levelWaitProfile:
		return "wait profile", names
	case levelLogFiles:
		return "logs", names
	case levelLogs:
		if sc.log.src != nil {
			return filepath.Base(sc.log.src.Info().Path), names
		}
		return "logs", names
	case levelLogGroup:
		if sc.log.paramKey != "" {
			return "parameters", names
		}
		if sc.log.group != nil {
			return sc.log.group.Category.Short() + " group", names
		}
	case levelLogEntry:
		return "entry", names
	case levelPgBouncers:
		return "pgbouncer", names
	case levelPgBouncer:
		if sc.pgb.inst != nil {
			return sc.pgb.inst.Name, names
		}
	case levelPgBouncerShow:
		return sc.pgb.show.spec().title, names
	}
	return "", names
}

// dbScopedLevel reports the levels whose content belongs to one database, so
// the trail must name that database somewhere before or on them. Levels of
// cluster-wide tools are deliberately absent even though their screens carry
// the connection database; the table lists name their database themselves
// when the schema picker was skipped.
func dbScopedLevel(l level) bool {
	switch l {
	case levelParts, levelColumns, levelHeapPages, levelHeapTuples,
		levelIndexPages, levelIndexTuples, levelDescribe, levelBufferDetail,
		levelStatementDetail, levelStatementSamples, levelStatementResult,
		levelDiagnosticResult:
		return true
	}
	return false
}

// renderTrail styles the crumb trail and fits it into budget cells: the
// current (last) crumb is highlighted, and when the trail is too long the
// middle collapses into "…" so the host and the current location survive. A
// tail that still doesn't fit is clipped rather than wrapped.
func renderTrail(crumbs []string, budget int) string {
	const sep = " ▸ "
	if len(crumbs) == 0 || budget <= 0 {
		return ""
	}
	width := func(cs []string) int {
		w := displayWidth(sep) * (len(cs) - 1)
		for _, c := range cs {
			w += displayWidth(c)
		}
		return w
	}
	cs := slices.Clone(crumbs)
	for width(cs) > budget && len(cs) > 2 {
		if cs[1] != "…" {
			cs[1] = "…"
			continue
		}
		if len(cs) == 3 {
			break
		}
		cs = slices.Delete(cs, 2, 3)
	}
	if w := width(cs); w > budget {
		last := len(cs) - 1
		cs[last] = clipCells(cs[last], max(budget-(w-displayWidth(cs[last])), 1))
	}
	out := make([]string, len(cs))
	for i, c := range cs {
		if i == len(cs)-1 {
			out[i] = styleCrumbActive.Render(c)
		} else {
			out[i] = styleBreadcrumb.Render(c)
		}
	}
	return strings.Join(out, styleBreadcrumb.Render(sep))
}

func (m *Model) renderToolPicker(s *screen, height int) string {
	vis := s.visibleIndexes()
	var b strings.Builder
	for vi, idx := range vis {
		it := s.items[idx]
		cursor := "  "
		name := it.name
		if vi == s.cursor {
			cursor = styleSelected.Render("▶ ")
			name = styleSelected.Render(name)
		}
		b.WriteString(cursor)
		b.WriteString(drillMark(it.hasChildren))
		b.WriteString(padRight(name, 20))
		b.WriteString("  ")
		b.WriteString(styleMuted.Render(it.detail))
		b.WriteString("\n")
	}
	return padInfo(&b, height)
}

// renderFilterLine draws the single-line filter affordance above the list.
// While focused it shows the live input with a trailing caret; once blurred
// but non-empty it shows the committed query plus a hint for how to clear
// or re-edit. Returns "" when there's nothing to draw (no filter, no focus).
func (m *Model) renderFilterLine(s *screen) string {
	if s.filter == "" && !s.filterFocused {
		return ""
	}
	matches := fmt.Sprintf("(%d/%d matches)", s.visibleLen(), len(s.items))
	if s.filterFocused {
		caret := styleSelected.Render("▏")
		return "  " + styleSelected.Render("/") + s.filter + caret + "  " + styleMuted.Render(matches)
	}
	hint := styleMuted.Render(matches+" — press ") +
		styleBadge.Render("/") + styleMuted.Render(" to edit, ") +
		styleBadge.Render("esc") + styleMuted.Render(" to clear")
	return "  " + styleMuted.Render("filter: ") + s.filter + "  " + hint
}

// renderSeekLine draws the seek-to-key affordance on the index-tuples view:
// "seek (player_id): <value>▏  <status>". The status reports where the cursor
// jumped. Returns "" unless the seek input is focused or carries a query.
func (m *Model) renderSeekLine(s *screen) string {
	if s.level != levelIndexTuples || (s.pages.seekQuery == "" && !s.pages.seekFocused) {
		return ""
	}
	label := "seek"
	if s.pages.index.AccessMethod == "brin" {
		// BRIN seeks by heap block number, not by key value.
		label = "seek (heap block)"
	} else if col := firstKeyColName(s.pages.indexKeyCols); col != "" {
		label = "seek (" + col + ")"
	}
	var status string
	if s.pages.seekStatus != "" {
		status = "  " + styleMuted.Render(s.pages.seekStatus)
	}
	if s.pages.seekFocused {
		caret := styleSelected.Render("▏")
		return "  " + styleSelected.Render(label+": ") + s.pages.seekQuery + caret + status
	}
	return "  " + styleMuted.Render(label+": ") + s.pages.seekQuery + status
}

// summaryLabelWidth is the width of the label column ("server memory" /
// "shared_buffers") at the head of each summary row. Set to max(len) of
// the two labels so the bars' opening brackets line up.
const summaryLabelWidth = 14

// summaryBarMax caps the summary bar width on very wide terminals so a
// 4k-cell window doesn't stretch the bar into ASCII art at the expense of
// the stats line's readability.
const summaryBarMax = 200

func (m *Model) renderList(s *screen, height int) string {
	return m.renderListWithFooter(s, height, "")
}

// renderListWithFooter is renderList with a footer (zero or more
// newline-terminated lines) rendered directly beneath the last row, so short
// lists read as a closed table instead of the footer hiding at the bottom of
// the screen. The footer's lines count against height; when the rows fill the
// viewport it sits on the last content lines, exactly as a bottom-pinned
// footer would.
func (m *Model) renderListWithFooter(s *screen, height int, footer string) string {
	vis := s.visibleIndexes()
	maxSz := maxItemSize(s.items, vis)
	barW := m.barWidth(s)
	rowsH := max(height-strings.Count(footer, "\n"), 0)

	var b strings.Builder
	// Sortable bar-list levels carry a column header so the active sort column is
	// labelled; the breakdown flag adds heap/idx columns on the tables level.
	header := ""
	breakdown := false
	switch s.level {
	case levelTables:
		header = renderTablesHeader(s, barW)
		breakdown = s.tool != toolPageInspect
	case levelParts:
		header = renderPartsHeader(s, barW)
	case levelDatabases:
		header = renderGenericHeader(s, barW, "database")
	case levelSchemas:
		header = renderSchemasHeader(s, barW)
	case levelColumns:
		header = renderGenericHeader(s, barW, "column")
	}
	if header != "" {
		rowsH = max(rowsH-1, 0)
		b.WriteString(header)
		b.WriteString("\n")
	}

	// The parts bloat columns appear once any sibling has been measured (the
	// same gate as the header); rows still unmeasured render blank cells so
	// the columns after them stay aligned.
	bloatCols := false
	if s.level == levelParts {
		for _, it := range s.items {
			if it.hasBloat {
				bloatCols = true
				break
			}
		}
	}

	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		b.WriteString(renderRow(row{
			size: it.size, bloat: it.bloat, hasBloat: it.hasBloat, hasChildren: it.hasChildren, maxSize: maxSz,
			barW: barW, bloatCols: bloatCols,
			heap: it.heap, idx: it.idx, toast: it.toast, hasBreakdown: breakdown,
			rows: it.rows, hasRows: it.hasRows,
			pages: it.pages, hasPages: it.hasPages,
			tableCount: it.tableCount, hasTableCount: it.hasTableCount,
			typeTag: it.typeTag, typeStyle: it.typeStyle,
			name: it.name, detail: it.detail, detailStyled: it.detailStyled, selected: vi == s.cursor,
		}))
		b.WriteString("\n")
	}
	b.WriteString(footer)
	// Pad to fixed height so help line stays put.
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}
