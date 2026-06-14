package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// procRaw is one /proc sample for a single backend PID. ReadBytes and
// WriteBytes are -1 when /proc/<pid>/io was unreadable (e.g. permission
// denied). On non-Linux platforms sampleAllPids returns nil and no procRaw
// values are produced.
type procRaw struct {
	PID        int32
	RSSBytes   int64  // VmRSS in bytes (from /proc/<pid>/status)
	CPUTicks   uint64 // utime+stime in USER_HZ ticks (from /proc/<pid>/stat)
	ReadBytes  int64  // cumulative storage read_bytes (from /proc/<pid>/io); -1 = unreadable
	WriteBytes int64  // cumulative storage write_bytes; -1 = unreadable
	At         time.Time
}

// procDerived is the display-ready per-PID stats computed from two consecutive
// procRaw samples. Negative values mean "not available" (first sample, platform
// stub, or permission denied for I/O).
type procDerived struct {
	RSSBytes int64   // resident set size in bytes; 0 = unknown
	CPUPct   float64 // CPU usage % (100% = one full core); <0 = unknown
	ReadBps  float64 // storage read bytes/s; <0 = unavailable
	WriteBps float64 // storage write bytes/s; <0 = unavailable
}

// activityProcMsg delivers a fresh batch of /proc samples to the Update loop.
type activityProcMsg struct {
	samples []procRaw
}

// sampleProcStatsCmd fires a background goroutine that reads /proc for each
// PID. On non-Linux hosts sampleAllPids returns nil and this delivers an empty
// message, which onActivityProc ignores. Returns nil when pids is empty.
func (m *Model) sampleProcStatsCmd(pids []int32) tea.Cmd {
	if len(pids) == 0 {
		return nil
	}
	return func() tea.Msg {
		return activityProcMsg{samples: sampleAllPids(pids)}
	}
}

// onActivityProc merges a /proc sample batch into m.actProcStats, computing
// CPU% and I/O rates as deltas against the previous sample, then rebuilds the
// activity items in place so the proc columns update without a DB round-trip.
func (m *Model) onActivityProc(msg activityProcMsg) tea.Cmd {
	s := m.findLevel(levelActivity)
	if s == nil || len(msg.samples) == 0 {
		return nil
	}
	if m.actProcPrev == nil {
		m.actProcPrev = make(map[int32]procRaw, len(msg.samples))
	}
	if m.actProcStats == nil {
		m.actProcStats = make(map[int32]procDerived, len(msg.samples))
	}

	seen := make(map[int32]struct{}, len(msg.samples))
	for _, cur := range msg.samples {
		seen[cur.PID] = struct{}{}

		d := procDerived{
			RSSBytes: cur.RSSBytes,
			CPUPct:   -1,
			ReadBps:  -1,
			WriteBps: -1,
		}
		if prev, ok := m.actProcPrev[cur.PID]; ok {
			if dt := cur.At.Sub(prev.At).Seconds(); dt > 0 {
				// USER_HZ is 100 on every Linux kernel pgdu supports.
				const userHZ = 100.0
				if deltaTicks := float64(cur.CPUTicks) - float64(prev.CPUTicks); deltaTicks >= 0 {
					d.CPUPct = (deltaTicks / (dt * userHZ)) * 100
				}
				if cur.ReadBytes >= 0 && prev.ReadBytes >= 0 {
					if delta := float64(cur.ReadBytes - prev.ReadBytes); delta >= 0 {
						d.ReadBps = delta / dt
					}
				}
				if cur.WriteBytes >= 0 && prev.WriteBytes >= 0 {
					if delta := float64(cur.WriteBytes - prev.WriteBytes); delta >= 0 {
						d.WriteBps = delta / dt
					}
				}
			}
		}
		m.actProcPrev[cur.PID] = cur
		m.actProcStats[cur.PID] = d
	}

	// Evict entries for backends that have disappeared from the snapshot.
	for pid := range m.actProcPrev {
		if _, ok := seen[pid]; !ok {
			delete(m.actProcPrev, pid)
			delete(m.actProcStats, pid)
		}
	}

	m.rebuildActivityItems(s)
	return nil
}
