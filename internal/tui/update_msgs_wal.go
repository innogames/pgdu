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
	s.items = s.items[:0]
	for _, st := range msg.stats {
		s.items = append(s.items, walRmgrToItem(st))
	}
	m.applySort(s)
	return nil
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
	sum := msg.summary
	sum.StartLSN = s.wal.start
	sum.EndLSN = s.wal.end
	sum.WindowBytes = walWindowBytes
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

func (m *Model) onWALRelationsLoaded(msg walRelationsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelWALRelations)
	if s == nil || s.db != msg.db {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonWALInspect); stop {
		return cmd
	}
	s.wal.start = msg.start
	s.wal.end = msg.end
	s.items = s.items[:0]
	for _, st := range msg.rels {
		s.items = append(s.items, walRelStatToItem(st))
	}
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
	}
	m.applySort(s)
	return nil
}
