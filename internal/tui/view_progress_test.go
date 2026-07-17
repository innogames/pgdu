package tui

import (
	"strings"
	"testing"

	"pgdu/internal/pg"
)

func TestProgressDoneTotal(t *testing.T) {
	cases := []struct {
		name string
		row  pg.ProgressRow
		want string
	}{
		{"blocks", pg.ProgressRow{Unit: "blocks", Done: 300, Total: 1000}, "300 / 1000"},
		{"blocks no total", pg.ProgressRow{Unit: "blocks", Done: 42}, "42"},
		{"bytes", pg.ProgressRow{Unit: "bytes", Done: 1 << 20, Total: 1 << 30}, "1.00 MB / 1.00 GB"},
		{"bytes no total", pg.ProgressRow{Unit: "bytes", Done: 1 << 20}, "1.00 MB"},
		{"indexes", pg.ProgressRow{Unit: "indexes", Done: 3, Total: 9}, "3 / 9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := progressDoneTotal(c.row); got != c.want {
				t.Errorf("progressDoneTotal(%+v) = %q, want %q", c.row, got, c.want)
			}
		})
	}
}

func TestProgressPct(t *testing.T) {
	if got := (pg.ProgressRow{Done: 25, Total: 100}).Pct(); got != 25 {
		t.Errorf("Pct() = %v, want 25", got)
	}
	if got := (pg.ProgressRow{Done: 25}).Pct(); got != -1 {
		t.Errorf("Pct() with zero total = %v, want -1", got)
	}
}

func TestProgressETA(t *testing.T) {
	r := pg.ProgressRow{RunningMs: 60_000}
	// 25% in one minute → three more minutes to go.
	if got, want := progressETA(r, 25), fmtAge(180_000); got != want {
		t.Errorf("progressETA = %q, want %q", got, want)
	}
	for _, pct := range []float64{-1, 0, 100, 120} {
		if got := progressETA(r, pct); got != "—" {
			t.Errorf("progressETA(pct=%v) = %q, want em-dash", pct, got)
		}
	}
	if got := progressETA(pg.ProgressRow{}, 50); got != "—" {
		t.Errorf("progressETA with no runtime = %q, want em-dash", got)
	}
}

func TestRenderProgress(t *testing.T) {
	m := &Model{width: 200}
	s := &screen{level: levelProgress, tool: toolMaintenance, db: "maindb"}

	// Empty state renders its own message instead of the generic "(no items)".
	m.rebuildProgressItems(s)
	if out := stripANSI(m.renderProgress(s, 5)); !strings.Contains(out, "no operations in progress") {
		t.Errorf("empty render missing empty-state message:\n%s", out)
	}

	s.progressRows = []pg.ProgressRow{
		{PID: 101, Command: "CREATE INDEX", Relation: "public.orders_idx", Phase: "building index",
			Unit: "blocks", Done: 640, Total: 1000, RunningMs: 252_000, Username: "app"},
		{PID: 102, Command: "COPY FROM", Relation: "public.events",
			Unit: "bytes", Done: 1 << 30, Total: 2 << 30, RunningMs: 63_000, Username: "etl"},
		// No size estimate (--no-estimate-size): pinned to the streaming
		// span's start, bare done counter.
		{PID: 103, Command: "BASE BACKUP", Phase: "streaming database files",
			Unit: "bytes", Done: 1 << 20, Total: 0, RunningMs: 1_000, Username: "repl"},
		// Vacuum in another database: relation shows with a db prefix, and its
		// index pass counts indexes rather than a parked heap-block counter.
		{PID: 104, Command: "VACUUM", Relation: "public.big", Database: "otherdb",
			Phase: "vacuuming indexes", Unit: "indexes", Done: 3, Total: 9,
			RunningMs: 10_000, Username: "postgres"},
	}
	m.rebuildProgressItems(s)
	if len(s.items) != 4 {
		t.Fatalf("rebuildProgressItems: %d items, want 4", len(s.items))
	}
	// Filter text must match pid, command, relation, phase and user.
	if !strings.Contains(s.items[0].name, "101") || !strings.Contains(s.items[0].name, "orders_idx") {
		t.Errorf("filter text incomplete: %q", s.items[0].name)
	}

	// The bar/pct column shows the overall phase-weighted estimate; the
	// done/total column keeps each phase's raw counters.
	out := stripANSI(m.renderProgress(s, 10))
	for _, want := range []string{
		"4 ops",
		"CREATE INDEX", "public.orders_idx", "building index", "640 / 1000", "42.3%", "4.2m",
		"COPY FROM", "1.00 GB / 2.00 GB", "50.0%", // no phases: raw counters are the overall pct
		"BASE BACKUP", "1.00 MB", "3.0%",
		"VACUUM", "otherdb.public.big", "vacuuming indexes", "3 / 9", "66.7%",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
}

// onProgressLoaded keeps a per-operation high-water mark of OverallPct — the
// same monotonic clamp the REINDEX banner uses — so a VACUUM restarting an
// index pass holds the bar instead of snapping it back, while a new operation
// reusing the pid starts fresh.
func TestProgressClamp(t *testing.T) {
	s := &screen{level: levelProgress, tool: toolMaintenance, db: "maindb"}
	m := &Model{stack: []*screen{s}}
	load := func(rows ...pg.ProgressRow) {
		m.onProgressLoaded(progressLoadedMsg{db: "maindb", rows: rows})
	}

	vac := pg.ProgressRow{PID: 7, Command: "VACUUM", RelID: 42, Database: "maindb",
		Phase: "vacuuming indexes", Unit: "indexes", Done: 1, Total: 2}
	load(vac)
	if got := s.progressPct(vac); got != 70 {
		t.Errorf("first sample: pct %.1f, want 70", got)
	}

	// Second index-cleanup cycle restarts the counter; the clamp holds.
	vac.Done = 0
	load(vac)
	if got := s.progressPct(vac); got != 70 {
		t.Errorf("regressed sample: pct %.1f, want held 70", got)
	}

	// Same pid, different relation (autovacuum worker moved on): fresh mark.
	next := pg.ProgressRow{PID: 7, Command: "VACUUM", RelID: 43, Database: "maindb",
		Phase: "scanning heap", Unit: "blocks", Done: 0, Total: 100}
	load(next)
	if got := s.progressPct(next); got != 1 {
		t.Errorf("new relation on reused pid: pct %.1f, want 1", got)
	}

	// Finished operations drop their marks with their rows.
	load()
	if len(s.progressPctMax) != 0 {
		t.Errorf("marks not pruned: %v", s.progressPctMax)
	}
}
