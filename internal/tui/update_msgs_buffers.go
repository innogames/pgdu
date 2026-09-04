package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

func (m *Model) onBufferStatsLoaded(msg bufferStatsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelBufferTables)
	if s == nil || s.db != msg.db || s.schema != msg.schema {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonBufferCache); stop {
		return cmd
	}
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
		s.buf.summaryErr = msg.err
		s.buf.summary = nil
	} else {
		sum := msg.summary
		s.buf.summary = &sum
		s.buf.summaryErr = nil
	}
	return nil
}

func (m *Model) onBufferDetailLoaded(msg bufferDetailLoadedMsg) tea.Cmd {
	s := m.findLevel(levelBufferDetail)
	if s == nil || s.db != msg.db || s.buf.detail == nil || s.buf.detail.OID != msg.oid {
		return nil
	}
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		return setExtensionPrompt(s, ext, extPromptReasonBufferCache)
	}
	s.buf.usageErr = msg.err
	s.buf.usage = msg.counts
	s.buf.blockSize = msg.blockSize
	return nil
}

func (m *Model) onShmemLoaded(msg shmemLoadedMsg) tea.Cmd {
	s := m.findLevel(levelShmem)
	if s == nil || s.db != msg.db {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = s.items[:0]
	for _, a := range msg.allocs {
		s.items = append(s.items, shmemAllocToItem(a))
	}
	m.applySort(s)
	return nil
}
