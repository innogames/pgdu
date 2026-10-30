package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"pgdu/internal/pg"
)

// growthNow is the growth browser's only synthetic anchor: the live end of a
// window, re-captured on every refresh. The query-snapshot browser's other two
// anchors have no counterpart here — sizes have no in-memory baseline to stand
// in for @session, and no reset to stand in for @reset.
const growthNow = "@now"

// growthCaptureTimeout bounds one interactive S capture. CaptureSizes stats
// every fork of every relation, so it is deliberately NOT wrapped in query()'s
// 30 s budget — the same exemption REINDEX, VACUUM and log loading take. It is
// still bounded so a wedged server cannot hang the key forever.
const growthCaptureTimeout = 10 * time.Minute

// --- messages ---

type sizeSamplesListedMsg struct {
	dir          string
	db           string
	metas        []pg.SizeSampleMeta
	liveOID      uint32
	liveSystemID int64
	liveTotal    int64
	err          error
}

// sizeSampleCapturedMsg carries an S capture (save = true, written to disk) or
// the live end of an applied window (save = false, never written).
type sizeSampleCapturedMsg struct {
	save   bool
	db     string
	path   string
	sample *pg.SizeSample
	err    error
}

type growthWindowLoadedMsg struct {
	db                string
	base, end         *pg.SizeSample
	basePath, endPath string
	result            *pg.GrowthResult
	// stay keeps the picker open: the pick landed as the window's end, so the
	// user is mid-range and likely to adjust the other end.
	stay bool
	err  error
}

// listSizeSamplesCmd lists the samples for db and reads the live identity the
// compatibility check needs. The identity reads are best-effort decorations:
// an unreadable system id degrades to 0, which reads as "unknown", not as a
// mismatch.
func (m *Model) listSizeSamplesCmd(dir, db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		return m.listSizeSamples(ctx, dir, db)
	})
}

// captureSizesCmd captures the current sizes. With save it writes the sample
// to the snapshot directory so it joins the browser; without, it is the live
// end of an applied window and stays in memory.
func (m *Model) captureSizesCmd(db string, save bool) tea.Cmd {
	dir := m.snapshotDir
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), growthCaptureTimeout)
		defer cancel()

		msg := sizeSampleCapturedMsg{save: save, db: db}
		sample, err := m.client.CaptureSizes(ctx, db, 0)
		if err != nil {
			msg.err = err
			return msg
		}
		msg.sample = sample
		if save {
			// Interactive captures keep every relation and never prune: the
			// size floor and the retention horizon are cron's concerns, and a
			// key press should not silently delete history.
			msg.path, msg.err = pg.SaveSizeSample(dir, sample)
		}
		return msg
	}
}

// deleteSizeSampleCmd removes one sample and re-lists, so a delete error rides
// back inside the listing rather than needing its own message.
func (m *Model) deleteSizeSampleCmd(path, dir, db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		if err := pg.DeleteSnapshot(path); err != nil {
			return sizeSamplesListedMsg{dir: dir, db: db, err: err}
		}
		return m.listSizeSamples(ctx, dir, db)
	})
}

// listSizeSamples is the body listSizeSamplesCmd and deleteSizeSampleCmd
// share. The live identity is a best-effort decoration: a capture just to bar
// the @now row would be wasteful, so it comes from cheap catalog reads, and a
// failure simply leaves the row unbarred.
func (m *Model) listSizeSamples(ctx context.Context, dir, db string) sizeSamplesListedMsg {
	msg := sizeSamplesListedMsg{dir: dir, db: db}
	metas, err := pg.ListSizeSamples(dir)
	if err != nil {
		msg.err = err
		return msg
	}
	msg.metas = metas
	if live, err := m.client.LiveSizeIdentity(ctx, db); err == nil {
		msg.liveOID, msg.liveSystemID, msg.liveTotal = live.OID, live.SystemID, live.TotalBytes
	}
	return msg
}

// loadGrowthWindowCmd loads the window's two ends and diffs them. An empty
// endPath means the window ends "now", which is a real capture rather than a
// stored file: sizes have no counters to difference in memory.
//
// It is not wrapped in query() when it captures, for the reason on
// growthCaptureTimeout.
func (m *Model) loadGrowthWindowCmd(db, basePath, endPath string, stay bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), growthCaptureTimeout)
		defer cancel()

		msg := growthWindowLoadedMsg{db: db, basePath: basePath, endPath: endPath, stay: stay}
		base, err := pg.LoadSizeSample(basePath)
		if err != nil {
			msg.err = err
			return msg
		}
		msg.base = base

		if endPath == growthNow || endPath == "" {
			msg.endPath = growthNow
			if msg.end, err = m.client.CaptureSizes(ctx, db, 0); err != nil {
				msg.err = err
				return msg
			}
		} else if msg.end, err = pg.LoadSizeSample(endPath); err != nil {
			msg.err = err
			return msg
		}

		msg.result, msg.err = pg.DiffSizes(msg.base, msg.end)
		return msg
	}
}
