package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

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
	s.items = s.items[:0]
	for _, sc := range msg.schemas {
		s.items = append(s.items, item{name: sc.Name, size: sc.SizeBytes, hasChildren: true, detail: schemaDetail(sc), data: sc})
	}
	m.applySort(s)
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
	if m.fetchBloat && msg.err == nil {
		s.bloatScanning = true
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

func (m *Model) onBufferStatsLoaded(msg bufferStatsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelBufferTables)
	if s == nil || s.db != msg.db || s.schema != msg.schema {
		return nil
	}
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		// Promote to a blocking install prompt instead of an opaque error.
		return setExtensionPrompt(s, ext, extPromptReasonBufferCache)
	}
	s.err = msg.err
	s.items = s.items[:0]
	for _, st := range msg.stats {
		s.items = append(s.items, bufferStatToItem(st))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onBufferSummaryLoaded(msg bufferSummaryLoadedMsg) tea.Cmd {
	s := m.findLevel(levelBufferTables)
	if s == nil || s.db != msg.db {
		return nil
	}
	if ext := asMissingExt(msg.err); ext != nil {
		// The summary error is swallowed; the blocking prompt set by
		// onBufferStatsLoaded already covers the user-visible state.
		return nil
	}
	if msg.err != nil {
		s.bufferSummaryErr = msg.err
		s.bufferSummary = nil
	} else {
		sum := msg.summary
		s.bufferSummary = &sum
		s.bufferSummaryErr = nil
	}
	return nil
}

func (m *Model) onBufferDetailLoaded(msg bufferDetailLoadedMsg) tea.Cmd {
	s := m.findLevel(levelBufferDetail)
	if s == nil || s.db != msg.db || s.bufDetail == nil || s.bufDetail.OID != msg.oid {
		return nil
	}
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		return setExtensionPrompt(s, ext, extPromptReasonBufferCache)
	}
	s.bufUsageErr = msg.err
	s.bufUsage = msg.counts
	s.bufBlockSize = msg.blockSize
	return nil
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
	s.bloatScanning = false
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
	if s.describe != nil && s.describe.OID != msg.oid {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.describe = msg.desc
	// (Re)load the cache-footprint section for table describes. Triggering here
	// — rather than at push time — covers every entry point uniformly (the `d`
	// push, the name-resolved push from top-queries, and Refresh), and gives us
	// the resolved OID even when the screen was pushed by table name. Reset the
	// prior section state first so a refresh doesn't show stale figures.
	s.descBuf = nil
	s.descBufErr = nil
	if msg.err == nil && msg.desc != nil && msg.desc.Kind == pg.DescribeTable && msg.desc.OID != 0 {
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
	if s == nil || s.db != msg.db || s.describe == nil || s.describe.OID != msg.oid {
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
	s.descBufErr = msg.err
	if msg.err == nil {
		stat := msg.stat
		s.descBuf = &stat
	}
	return nil
}

func (m *Model) onReindexDone(msg reindexDoneMsg) tea.Cmd {
	s := m.findLevel(levelParts)
	if s == nil || s.table.OID != msg.tableOID {
		return nil
	}
	s.reindexing = ""
	if msg.err != nil {
		s.reindexErr = msg.err
		return nil
	}
	s.reindexErr = nil
	// Refresh: the index has been rebuilt, so size and bloat have changed.
	return m.loadCurrent()
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
	s.diagCols = msg.result.Columns
	s.diagBarCol = msg.result.BarCol
	// Default sort: the result's SortCol (biggest first) if present, else col 0.
	if msg.result.SortCol >= 0 {
		s.diagSortCol = msg.result.SortCol
		s.sortDesc = true
	} else {
		s.diagSortCol = 0
		s.sortDesc = false
	}
	// Convert each result row to an item. item.name is the space-joined cell
	// display so the existing fuzzy filter can match any column value.
	for _, row := range msg.result.Rows {
		parts := make([]string, len(row))
		for i, cell := range row {
			parts[i] = cell.Display
		}
		s.items = append(s.items, item{
			name: strings.Join(parts, " "),
			data: row, // []pg.DiagCell
		})
	}
	s.diagMetricsDirty = true
	m.applySort(s)
	return nil
}

func errText(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}
