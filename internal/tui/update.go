package tui

import (
	"errors"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// Update is the top-level Bubble Tea dispatcher. Each msg case delegates to a
// handler that owns the per-message state mutation and returns the follow-up
// command (or nil). Handlers live in update_msgs.go; key dispatch lives in
// update_keys.go; navigation in update_drill.go; sort logic in update_sort.go.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.Width = msg.Width
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case databasesLoadedMsg:
		return m, m.onDatabasesLoaded(msg)
	case schemasLoadedMsg:
		return m, m.onSchemasLoaded(msg)
	case tablesLoadedMsg:
		return m, m.onTablesLoaded(msg)
	case partsLoadedMsg:
		return m, m.onPartsLoaded(msg)
	case diskTableResolvedMsg:
		return m, m.onDiskTableResolved(msg)
	case bufferStatsLoadedMsg:
		return m, m.onBufferStatsLoaded(msg)
	case bufferSummaryLoadedMsg:
		return m, m.onBufferSummaryLoaded(msg)
	case bufferDetailLoadedMsg:
		return m, m.onBufferDetailLoaded(msg)
	case shmemLoadedMsg:
		return m, m.onShmemLoaded(msg)
	case columnsLoadedMsg:
		return m, m.onColumnsLoaded(msg)
	case bloatFilledMsg:
		return m, m.onBloatFilled(msg)
	case extStatusMsg:
		return m, m.onExtStatus(msg)
	case extInstalledMsg:
		return m, m.onExtInstalled(msg)
	case reindexDoneMsg:
		return m, m.onReindexDone(msg)
	case reindexTickMsg:
		return m, m.onReindexTick()
	case reindexProgressMsg:
		return m, m.onReindexProgress(msg)
	case heapPagesLoadedMsg:
		return m, m.onHeapPagesLoaded(msg)
	case toastTargetResolvedMsg:
		return m, m.onToastTargetResolved(msg)
	case heapTuplesLoadedMsg:
		return m, m.onHeapTuplesLoaded(msg)
	case tupleRowLoadedMsg:
		return m, m.onTupleRowLoaded(msg)
	case toastValueLoadedMsg:
		return m, m.onToastValueLoaded(msg)
	case tupleAttrsLoadedMsg:
		return m, m.onTupleAttrsLoaded(msg)
	case relationsLoadedMsg:
		return m, m.onRelationsLoaded(msg)
	case indexPagesLoadedMsg:
		return m, m.onIndexPagesLoaded(msg)
	case indexTuplesLoadedMsg:
		return m, m.onIndexTuplesLoaded(msg)
	case gistPagesLoadedMsg:
		return m, m.onGistPagesLoaded(msg)
	case gistItemsLoadedMsg:
		return m, m.onGistItemsLoaded(msg)
	case brinPagesLoadedMsg:
		return m, m.onBrinPagesLoaded(msg)
	case brinItemsLoadedMsg:
		return m, m.onBrinItemsLoaded(msg)
	case ginPagesLoadedMsg:
		return m, m.onGinPagesLoaded(msg)
	case ginItemsLoadedMsg:
		return m, m.onGinItemsLoaded(msg)
	case describeLoadedMsg:
		return m, m.onDescribeLoaded(msg)
	case describeBuffersLoadedMsg:
		return m, m.onDescribeBuffersLoaded(msg)
	case diagnosticLoadedMsg:
		return m, m.onDiagnosticLoaded(msg)
	case walOverviewLoadedMsg:
		return m, m.onWALOverviewLoaded(msg)
	case walSummaryLoadedMsg:
		return m, m.onWALSummaryLoaded(msg)
	case walRecordsLoadedMsg:
		return m, m.onWALRecordsLoaded(msg)
	case walBlocksLoadedMsg:
		return m, m.onWALBlocksLoaded(msg)
	case walCheckpointLoadedMsg:
		return m, m.onWALCheckpointLoaded(msg)
	case walRelationsLoadedMsg:
		return m, m.onWALRelationsLoaded(msg)
	case walRelBlocksLoadedMsg:
		return m, m.onWALRelBlocksLoaded(msg)
	case statementsLoadedMsg:
		return m, m.onStatementsLoaded(msg)
	case statementsTickMsg:
		return m, m.onStatementsTick()
	case statementSampleLoadedMsg:
		return m, m.onStatementSampleLoaded(msg)
	case statementExplainLoadedMsg:
		return m, m.onStatementExplainLoaded(msg)
	case statementHotLoadedMsg:
		return m, m.onStatementHotLoaded(msg)
	case statementSamplesLoadedMsg:
		return m, m.onStatementSamplesLoaded(msg)
	case statementResultLoadedMsg:
		return m, m.onStatementResultLoaded(msg)
	case snapshotSavedMsg:
		return m, m.onSnapshotSaved(msg)
	case snapshotsListedMsg:
		return m, m.onSnapshotsListed(msg)
	case snapshotBaseLoadedMsg:
		return m, m.onSnapshotBaseLoaded(msg)
	case snapshotFrozenLoadedMsg:
		return m, m.onSnapshotFrozenLoaded(msg)
	case exportDoneMsg:
		return m, m.onExportDone(msg)

	case maintLoadedMsg:
		return m, m.onMaintLoaded(msg)
	case settingsLoadedMsg:
		return m, m.onSettingsLoaded(msg)
	case progressLoadedMsg:
		return m, m.onProgressLoaded(msg)
	case maintResetDoneMsg:
		return m, m.onMaintResetDone(msg)
	case tableStatsLoadedMsg:
		return m, m.onTableStatsLoaded(msg)
	case vacuumStartedMsg:
		return m, m.onVacuumStarted(msg)
	case vacuumLineMsg:
		return m, m.onVacuumLine(msg)
	case vacuumDoneMsg:
		return m, m.onVacuumDone(msg)

	case activityLoadedMsg:
		return m, m.onActivityLoaded(msg)
	case lockTreeLoadedMsg:
		return m, m.onLockTreeLoaded(msg)
	case activityTickMsg:
		return m, m.onActivityTick()
	case activityHostsMsg:
		return m, m.onActivityHosts(msg)
	case activityToastMsg:
		return m, m.onActivityToast(msg)
	case activityProcMsg:
		return m, m.onActivityProc(msg)
	case backendActionMsg:
		return m, m.onBackendAction(msg)
	case activityStatementMsg:
		return m, m.onActivityStatement(msg)

	case tableOverviewLoadedMsg:
		return m, m.onTableOverviewLoaded(msg)

	case triageLoadedMsg:
		return m, m.onTriageLoaded(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// asMissingExt returns the underlying *pg.MissingExtensionError if err is one,
// or nil. errors.As handles wrapping so command callers can wrap freely.
func asMissingExt(err error) *pg.MissingExtensionError {
	var e *pg.MissingExtensionError
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// setExtensionPrompt replaces a screen's list with a blocking "install this
// extension?" prompt built from a MissingExtensionError, clearing the raw
// error so the load handler shows the affordance instead of an opaque message.
// Returns nil so handlers can `return setExtensionPrompt(...)` in one line.
func setExtensionPrompt(s *screen, ext *pg.MissingExtensionError, reason string) tea.Cmd {
	s.err = nil
	s.extPrompt = &extPrompt{
		name:        ext.Extension,
		db:          ext.DB,
		installable: ext.Installable,
		reason:      reason,
		blocking:    true,
	}
	s.items = s.items[:0]
	return nil
}

// asOutdatedExt returns the underlying *pg.OutdatedExtensionError if err is one,
// or nil. errors.As handles wrapping so command callers can wrap freely.
func asOutdatedExt(err error) *pg.OutdatedExtensionError {
	var e *pg.OutdatedExtensionError
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// setUpgradePrompt replaces a screen's list with a blocking "upgrade this
// extension?" prompt built from an OutdatedExtensionError. installable is reused
// as "can an ALTER EXTENSION UPDATE help" (Updatable): false means even the
// server's default version is too old, so the prompt explains there's nothing an
// in-database upgrade can do.
func setUpgradePrompt(s *screen, ext *pg.OutdatedExtensionError, reason string) tea.Cmd {
	s.err = nil
	s.extPrompt = &extPrompt{
		name:        ext.Extension,
		db:          ext.DB,
		installable: ext.Updatable,
		reason:      reason,
		blocking:    true,
		upgrade:     true,
		installed:   ext.Installed,
		available:   ext.Available,
		required:    ext.Required,
	}
	s.items = s.items[:0]
	return nil
}
