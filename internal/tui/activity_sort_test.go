package tui

import (
	"testing"

	"pgdu/internal/cli"
	"pgdu/internal/pg"
)

// TestActivityReopenKeepsDefaultOrder guards the reopen path: the sort column
// is remembered on the shared actTable while the direction lives on the screen,
// so a second visit used to land on query_age ascending.
func TestActivityReopenKeepsDefaultOrder(t *testing.T) {
	m := &Model{client: pg.New(cli.Config{})}
	rows := []pg.ActivityRow{{PID: 1}, {PID: 2}}

	for visit := 1; visit <= 2; visit++ {
		s := m.toolEntryScreen(toolActivity)
		s.act.rows = rows
		m.rebuildActivityItems(s)
		if got := s.act.cols[s.diagSortCol].id; got != actColQueryAge {
			t.Fatalf("visit %d: sort column = %q, want %q", visit, got, actColQueryAge)
		}
		if !s.sortDesc {
			t.Fatalf("visit %d: activity opened ascending, want query_age desc", visit)
		}
	}
}
