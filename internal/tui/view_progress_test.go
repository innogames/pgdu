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
		{PID: 102, Command: "COPY", Relation: "public.events",
			Unit: "bytes", Done: 1 << 30, Total: 2 << 30, RunningMs: 63_000, Username: "etl"},
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

	out := stripANSI(m.renderProgress(s, 10))
	for _, want := range []string{
		"4 ops",
		"CREATE INDEX", "public.orders_idx", "building index", "640 / 1000", "64.0%", "4.2m",
		"COPY", "1.00 GB / 2.00 GB", "50.0%",
		"BASE BACKUP", "1.00 MB", "—", // unknown total: bare done + em-dash pct
		"VACUUM", "otherdb.public.big", "vacuuming indexes", "3 / 9", "33.3%",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
}
