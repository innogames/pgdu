package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// logFilesLoadedMsg delivers the picker's candidates. Discovery never fails as
// a whole (each source is best-effort), so there is no err field; an empty list
// renders its own explanation.
type logFilesLoadedMsg struct {
	cands []pg.LogCandidate
}

// logLoadedMsg delivers a parsed window. path identifies the source so a stale
// result for a file the user already navigated away from is dropped; refresh
// marks a live-tail re-read (keep the cursor where it is).
type logLoadedMsg struct {
	path    string
	report  *pg.LogReport
	err     error
	refresh bool
}

type logTickMsg struct{}

// logDefaultWindow is the tail read on first open: big enough for a day of a
// busy server's log, small enough to parse in well under a second.
const logDefaultWindow = 32 << 20

// logWindowSteps is the w cycle: 32 → 64 → 128 → 256 → 512 MiB → whole file.
var logWindowSteps = []int64{32 << 20, 64 << 20, 128 << 20, 256 << 20, 512 << 20, 0}

// logLoadTimeout is used above 128 MiB (and for whole-file reads), where the
// parse alone can take several seconds and the shared 30 s query() cap would be
// a false failure. Reads remain cancellable through the context.
const logLoadTimeout = 120 * time.Second

func (m *Model) discoverLogsCmd() tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		return logFilesLoadedMsg{cands: m.client.DiscoverLogs(ctx, m.logFile)}
	})
}

// loadLogCmd reads s.logSrc. With refresh set and a report already loaded it
// takes the incremental path (append-only re-read from the last entry); a
// first load, a window change or a rotation go through the full LoadLog.
func (m *Model) loadLogCmd(s *screen, refresh bool) tea.Cmd {
	src := s.logSrc
	if src == nil {
		return nil
	}
	prev := s.logReport
	window := s.logWindow
	path := src.Info().Path
	timeout := queryTimeout
	if window <= 0 || window > 128<<20 {
		timeout = logLoadTimeout
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		settings := m.client.LogSettings(ctx)
		loc := pg.LogLocation(settings)
		if refresh && prev != nil {
			r, err := pg.RefreshLog(ctx, prev, src, loc, pg.AggOptions{})
			return logLoadedMsg{path: path, report: r, err: err, refresh: true}
		}
		r, err := pg.LoadLog(ctx, src, settings["log_line_prefix"], loc, window, pg.AggOptions{})
		return logLoadedMsg{path: path, report: r, err: err}
	}
}

// logTick schedules the next live-tail re-read, or nil when auto-refresh is
// off — returning nil ends the self-rescheduling loop.
func (m *Model) logTick() tea.Cmd {
	if m.logRefresh <= 0 {
		return nil
	}
	return tea.Tick(m.logRefresh, func(time.Time) tea.Msg { return logTickMsg{} })
}

// armLogTick appends the tick to cmds unless one is already running.
func (m *Model) armLogTick(cmds []tea.Cmd) []tea.Cmd {
	if !m.logTicking {
		if tick := m.logTick(); tick != nil {
			m.logTicking = true
			cmds = append(cmds, tick)
		}
	}
	return cmds
}

// cycleLogRefresh steps the live-tail cadence: off → 5s → 15s → 60s → off. A
// log is usually read after the fact, so off is the resting state.
func (m *Model) cycleLogRefresh() {
	switch m.logRefresh {
	case 0:
		m.logRefresh = 5 * time.Second
	case 5 * time.Second:
		m.logRefresh = 15 * time.Second
	case 15 * time.Second:
		m.logRefresh = 60 * time.Second
	default:
		m.logRefresh = 0
	}
}
