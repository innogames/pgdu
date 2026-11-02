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

// sizeCaptureMode says what a capture does beyond reading the sizes. The three
// rules are named rather than passed as a pair of bools because "written" and
// "prunes" are not independent, and the one combination that must never exist
// is a key press that deletes history.
type sizeCaptureMode int

const (
	captureLive   sizeCaptureMode = iota // the live end of an applied window; never written
	captureManual                        // S: written, never pruned
	captureAuto                          // once a day on tool open: written, then pruned
)

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

// sizeSampleCapturedMsg carries a capture back; mode says which of the three
// kinds it was, and so what the handler may say about it.
type sizeSampleCapturedMsg struct {
	mode   sizeCaptureMode
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

// captureSizesCmd captures the current sizes under one of the three modes:
// written and swept (auto), written and left alone (S), or kept in memory as
// the live end of a window.
//
// Every mode keeps every relation (minBytes 0). The size floor is cron's knob;
// an interactive-origin capture that silently dropped relations would make the
// growth table's row set depend on invisible config, and a window that spans
// one floored file and one unfloored one is already explained by
// GrowthResult.FloorChanged.
func (m *Model) captureSizesCmd(db string, mode sizeCaptureMode) tea.Cmd {
	dir, keep := m.snapshotDir, m.sampleRetention
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), growthCaptureTimeout)
		defer cancel()

		msg := sizeSampleCapturedMsg{mode: mode, db: db}
		sample, err := m.client.CaptureSizes(ctx, db, 0)
		if err != nil {
			msg.err = err
			return msg
		}
		msg.sample = sample
		if mode == captureLive {
			return msg
		}
		if msg.path, msg.err = pg.SaveSizeSample(dir, sample); msg.err != nil || mode != captureAuto {
			// S never prunes: a key press should not silently delete history.
			return msg
		}
		// The auto capture does, because it is pgdu's own doing — pgdu owns
		// bounding what it created. A prune failure is not worth reporting: the
		// sample is already safely written and the next capture retries.
		_, _ = pg.PruneSizeSamples(dir, sample.Target, sample.Database, keep)
		return msg
	}
}

// autoCaptureSizesCmd takes the day's size sample when the growth tool opens on
// a database that has none yet. It is the growth half of the rule that pgdu's
// history has to accrue from use, since the server keeps none and nothing here
// is a daemon.
//
// The listing it is handed is the one the entry browser just loaded, so asking
// "already today?" costs nothing. An empty snapshotDir bails rather than
// failing MkdirAll("") on every tool open.
func (m *Model) autoCaptureSizesCmd(db string, metas []pg.SizeSampleMeta) tea.Cmd {
	if !m.autoSample || m.snapshotDir == "" || m.capturingSizes() {
		return nil
	}
	if pg.SizeSampleCapturedToday(metas, m.target, db, time.Now()) {
		return nil
	}
	m.setCapturingSizes(true)
	return m.captureSizesCmd(db, captureAuto)
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

// capturingSizes / setCapturingSizes span both growth screens, because a
// capture started from the picker is still running when the user has entered a
// window and is standing on the table — the "already running" guard and the
// banner have to agree wherever the cursor is. The clear in
// onSizeSampleCaptured has always swept both levels; these make the set and the
// read match it.
func (m *Model) capturingSizes() bool {
	for _, l := range []level{levelGrowth, levelGrowthWindow} {
		if s := m.findLevel(l); s != nil && s.growth.capturing {
			return true
		}
	}
	return false
}

func (m *Model) setCapturingSizes(v bool) {
	for _, l := range []level{levelGrowth, levelGrowthWindow} {
		if s := m.findLevel(l); s != nil {
			s.growth.capturing = v
		}
	}
}
