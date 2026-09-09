package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

func (m *Model) onWALOverviewLoaded(msg walOverviewLoadedMsg) tea.Cmd {
	s := m.findLevel(levelWAL)
	if s == nil || s.db != msg.db {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonWALInspect); stop {
		return cmd
	}
	s.wal.start = msg.start
	s.wal.end = msg.end
	s.wal.rmgrs = msg.stats
	// The relation table is re-aggregated over this exact window, so the
	// previous one is dropped rather than shown under a header it no longer
	// matches; the scan is chained here because it needs the resolved LSNs.
	s.wal.rels = nil
	s.wal.relsErr = nil
	s.wal.relsLoading = msg.err == nil && msg.start != "" && msg.end != ""
	m.applySort(s)
	if !s.wal.relsLoading {
		return nil
	}
	return m.loadWALRelationsCmd(s.db, s.wal.start, s.wal.end)
}

func (m *Model) onWALSummaryLoaded(msg walSummaryLoadedMsg) tea.Cmd {
	s := m.findLevel(levelWAL)
	if s == nil || s.db != msg.db {
		return nil
	}
	// Summary failure is non-fatal: the header sources (pg_ls_waldir /
	// pg_stat_wal) need a monitoring role the user may lack even when the
	// pg_walinspect rmgr list works. A missing-extension error here is
	// already covered by onWALOverviewLoaded's blocking prompt, so swallow it.
	if asMissingExt(msg.err) != nil {
		return nil
	}
	if msg.err != nil {
		s.wal.summaryErr = msg.err
		s.wal.summary = nil
		return nil
	}
	// The window (s.wal.start/end) is *not* copied in here: this fast built-ins
	// read usually lands before the pg_get_wal_stats scan that resolves it, so a
	// copy would be empty on first load and one refresh stale afterwards. The
	// header reads the window straight off the screen state instead.
	sum := msg.summary
	s.wal.summary = &sum
	s.wal.summaryErr = nil
	return nil
}

func (m *Model) onWALRecordsLoaded(msg walRecordsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelWALRecords)
	if s == nil || s.db != msg.db || s.wal.rmgr != msg.rmgr {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonWALInspect); stop {
		s.wal.recTypeStats = nil
		return cmd
	}
	s.wal.recTypeStats = msg.typeStats
	s.items = s.items[:0]
	for _, r := range msg.records {
		s.items = append(s.items, walRecordToItem(r))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onWALBlocksLoaded(msg walBlocksLoadedMsg) tea.Cmd {
	s := m.findLevel(levelWALBlocks)
	if s == nil || s.db != msg.db || s.wal.recLSN != msg.recLSN {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonWALInspect); stop {
		return cmd
	}
	s.items = s.items[:0]
	for _, b := range msg.blocks {
		s.items = append(s.items, walBlockToItem(b))
	}
	m.applySort(s)
	return nil
}

// onWALCheckpointLoaded caches the best-effort checkpoint context for the
// levelWAL header. Failure is non-fatal and not surfaced — the header's other
// lines (and the rmgr list) still render; the checkpoint lines just stay hidden.
func (m *Model) onWALCheckpointLoaded(msg walCheckpointLoadedMsg) tea.Cmd {
	s := m.findLevel(levelWAL)
	if s == nil || s.db != msg.db {
		return nil
	}
	if msg.err != nil {
		return nil
	}
	info := msg.info
	s.wal.checkpoint = &info
	return nil
}

// onWALRelationsLoaded fills the by-relation table of the WAL overview. The
// result is matched against the window the screen currently shows: a refresh
// re-resolves the LSNs and re-chains the scan, so a late answer for the old
// window is dropped. It never touches loaded/err — the screen settled with the
// rmgr rows; a failure here is the relation table's alone.
func (m *Model) onWALRelationsLoaded(msg walRelationsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelWAL)
	if s == nil || s.db != msg.db || s.wal.start != msg.start || s.wal.end != msg.end {
		return nil
	}
	s.wal.relsLoading = false
	s.wal.relsErr = msg.err
	s.wal.rels = msg.rels
	m.applySort(s)
	return nil
}

func (m *Model) onWALRelBlocksLoaded(msg walRelBlocksLoadedMsg) tea.Cmd {
	s := m.findLevel(levelWALRelBlocks)
	if s == nil || s.db != msg.db || s.wal.relFilenode != msg.relfilenode {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonWALInspect); stop {
		return cmd
	}
	s.items = s.items[:0]
	for _, b := range msg.blocks {
		s.items = append(s.items, walBlockToItem(b))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onWALBlockDetailLoaded(msg walBlockDetailLoadedMsg) tea.Cmd {
	s := m.findLevel(levelWALBlockDetail)
	if s == nil || s.db != msg.db || s.wal.blockRef == nil ||
		s.wal.blockRef.StartLSN != msg.ref.StartLSN || s.wal.blockRef.BlockID != msg.ref.BlockID {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonWALInspect); stop {
		return cmd
	}
	s.items = s.items[:0]
	if msg.err == nil {
		d := msg.detail
		s.wal.detail = &d
		s.items = append(s.items, buildWALDetailItems(d)...)
		// The record loaded; only the page-image decode lacked pageinspect.
		// Offer the install as a hint rather than a blocking prompt — the raw
		// bytes are still worth showing — and onExtInstalled's reload then
		// picks up the decoded page.
		if ext := d.PageInspectMissing; ext != nil {
			s.extPrompt = &extPrompt{
				name:        ext.Extension,
				db:          ext.DB,
				installable: ext.Installable,
				reason:      extPromptReasonWALPageImage,
				blocking:    false,
			}
		}
	}
	m.applySort(s)
	return nil
}
