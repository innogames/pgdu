package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"pgdu/internal/pg"
)

// A tiny but real value paints one cell; zero paints none; the fill clamps.
func TestGaugeBarFloorAndClamp(t *testing.T) {
	for _, tc := range []struct {
		ratio float64
		want  string
	}{
		{0, "[░░░░░░░░░░]"},
		{0.01, "[▇░░░░░░░░░]"},
		{0.5, "[▇▇▇▇▇░░░░░]"},
		{1.7, "[▇▇▇▇▇▇▇▇▇▇]"},
		{-3, "[░░░░░░░░░░]"},
	} {
		if got := stripANSI(gaugeBar(tc.ratio, styleBar, 10)); got != tc.want {
			t.Errorf("gaugeBar(%v) = %q, want %q", tc.ratio, got, tc.want)
		}
	}
}

// Parts that make up the whole fill the bar exactly; parts under the total
// leave a muted tail; every non-zero part keeps a cell.
func TestShareBarCells(t *testing.T) {
	full := stripANSI(shareBar(144, 20, barPart{140, styleBar}, barPart{4, styleErr}))
	if full != "[▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇]" {
		t.Errorf("parts summing to the total must fill the bar, got %q", full)
	}
	partial := stripANSI(shareBar(100, 20, barPart{2, styleBar}, barPart{5, styleErr}))
	if partial != "[▇▇░░░░░░░░░░░░░░░░░░]" {
		t.Errorf("2+5 of 100 must paint one cell each and leave the tail, got %q", partial)
	}
	if got := stripANSI(shareBar(0, 6, barPart{3, styleBar})); got != "[░░░░░░]" {
		t.Errorf("no total: empty bar, got %q", got)
	}
}

// The ratios the overview used to print as digits carry a gauge, every bar is
// the shared width, and in the stacked layout they all start in one column.
func TestRenderMaintenanceBars(t *testing.T) {
	info := overviewInfo()
	info.Host.SwapFree = info.Host.SwapTotal - 80<<20 // 1 % used: still one cell
	info.TupInserted, info.TupUpdated, info.TupHotUpdated = 1000, 500, 400
	info.SeqScans, info.IdxScans = 100, 900
	info.LiveTuples, info.DeadTuples = 9000, 1000
	for _, width := range []int{200, 120} {
		// Verbose, so the lightly used connection pool and the full-page
		// image share draw their bars too.
		m := &Model{width: width, maintVerbose: true}
		raw := stripANSI(m.renderMaintenance(overviewScreen(info), 400))
		out := squashSpaces(raw)
		for _, want := range []string{
			"pg_stat_statements [▇░░░░░░░░░░░░░░░░░░░] 100/5.0k 2.0%",
			"connections [▇▇░░░░░░░░░░░░░░░░░░] 7/100 7% (2 active · 5 idle)",
			"xid age · shop [▇░░░░░░░░░░░░░░░░░░░] 50.0M 3.1% failsafe · 25% freeze_max_age",
			"occupancy 1.0M / 1.0M buffers 95% · avg usage 2.4",
			"dirty 20.0k 2.0%",
			"checkpoints [▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇] 140 timed 4 requested 2.8% requested",
			"swap [▇░░░░░░░░░░░░░░░░░░░] 80.00 MB / 8.00 GB 1.0%",
			"workers [▇▇▇▇▇▇░░░░░░░░░░░░░░] 1 / 3 busy",
			"hot ratio [▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇░░░░] 400 of 500 upd 80.0%",
			"index usage [▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇░░] 900 idx / 100 seq 90.0%",
			"dead tuples [▇▇░░░░░░░░░░░░░░░░░░] 1.0k / 10.0k 10.0%",
			"full-page images [▇▇░░░░░░░░░░░░░░░░░░] 10.0% of records",
			"avg interval [▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇] 30m / 30m 100% of checkpoint_timeout",
			"host memory [▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇░] 32.00 GB total 20.00 GB available",
			"shared_buffers ▇ 7.63 GB used ▇ 379.50 MB unused\n",
			"▇ other 4.00 GB ▇ cache 18.00 GB ░ free 2.00 GB\n",
			"dirty-page writes [▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇▇] ▇ checkpointer 75.0% ▇ bgwriter 20.0% ▇ backends 5.0%",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("width %d: overview lacks %q\n%s", width, want, raw)
			}
		}
		// Every bar is exactly the shared width plus its brackets (a paned line
		// may carry one per pane), and stacked they all start in the value column.
		bars := 0
		for line := range strings.SplitSeq(raw, "\n") {
			for rest, off := line, 0; ; {
				i := strings.Index(rest, "[")
				j := strings.Index(rest, "]")
				if i < 0 || j < i {
					break
				}
				if strings.ContainsAny(rest[i:j], "▇░") {
					bars++
					if w := utf8.RuneCountInString(rest[i:j+1]) - 2; w != maintBarW {
						t.Errorf("width %d: bar of %d cells, want %d: %q", width, w, maintBarW, line)
					}
					if col := utf8.RuneCountInString(line[:off+i]); width == 120 && col != overviewLabelW+2 {
						t.Errorf("stacked bar must start in the value column, at %d: %q", col, line)
					}
				}
				rest, off = rest[j+1:], off+j+1
			}
		}
		// capacity, connections, hot/index/dead, temperature, host memory,
		// swap, workers, xid age, fpi, checkpoints, avg interval, dirty-page
		// writes — the fixture has no mxid age or WAL-in-flight, and the
		// always-full occupancy carries no bar.
		if bars != 14 {
			t.Errorf("width %d: %d bars rendered, want 14\n%s", width, bars, raw)
		}
	}
}

// Started without -d the screen has no database; the rows that name one fall
// back to the database the snapshot was taken in, and say nothing when even
// that is unknown.
func TestRenderMaintenanceDBFallback(t *testing.T) {
	info := overviewInfo()
	info.Database = "shop"
	s := overviewScreen(info)
	s.db = ""
	m := &Model{width: 120} // stacked, so every row ends its own line
	out := stripANSI(m.renderMaintenance(s, 300))
	for _, want := range []string{" schema health (shop) ", "table & index counters in shop", "not installed in shop",
		"no user-table counters in shop"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview without a screen db lacks %q\n%s", want, out)
		}
	}
	info.Database = ""
	out = stripANSI(m.renderMaintenance(s, 300))
	for _, want := range []string{" schema health ", "table & index counters\n", "not installed\n", "no user-table counters\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview with no db at all lacks %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "()") || strings.Contains(out, " in \n") || strings.Contains(out, " in  ") {
		t.Errorf("no db must not leave an empty name behind\n%s", out)
	}
}

// Section titles carry the panel's glyph for the worst finding they hold, and
// nothing on a healthy cluster.
func TestRenderMaintenanceSectionMarkers(t *testing.T) {
	m := &Model{width: 120, maintVerbose: true} // verbose: no ✓ summary trails the titles
	out := squashSpaces(stripANSI(m.renderMaintenance(overviewScreen(overviewInfo()), 400)))
	for _, h := range []string{"server", "observability", "autovacuum & wraparound", "wal & checkpoints"} {
		if !strings.Contains(out, "\n"+h+"\n") {
			t.Errorf("healthy section %q must carry no marker\n%s", h, out)
		}
	}
	info := overviewInfo()
	info.Settings["track_io_timing"] = "off" // warn
	info.IOSplit.ClientReadTimeMs = 0
	info.Checkpointer.Requested = 200 // 200/340 requested → max_wal_size crit
	out = squashSpaces(stripANSI(m.renderMaintenance(overviewScreen(info), 400)))
	for _, want := range []string{"\nobservability ~\n", "\nwal & checkpoints !\n", "\nserver\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("marked overview lacks %q\n%s", want, out)
		}
	}
}

// The wraparound advice colours the freeze-age bar; below its warn tier the
// bar stays plain however full it is.
func TestFreezeAgeBarFollowsAdvice(t *testing.T) {
	info := overviewInfo()
	info.XidAge = 198_000_000 // 99 % of freeze_max_age, 12 % of the failsafe
	raw := renderMaintAutovacuum(newMaintView(overviewScreen(info)), maintBarW)
	if !strings.Contains(raw, styleBar.Render(strings.Repeat("▇", 2))) {
		t.Errorf("99%% of freeze_max_age must paint a plain, short bar\n%s", raw)
	}
	info.XidAge = 900_000_000 // past half of vacuum_failsafe_age: crit
	raw = renderMaintAutovacuum(newMaintView(overviewScreen(info)), maintBarW)
	if !strings.Contains(raw, adviceStyle(pg.AdviceCrit).Render(strings.Repeat("▇", 11))) {
		t.Errorf("a critical wraparound finding must paint the bar in its colour\n%s", raw)
	}
}
