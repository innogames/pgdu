package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// t on a table's describe panel opens the top-queries tool for that database
// exactly like picking the database in the tool does — the unloaded table under
// the window picker — with the table's filter preset to the relation name.
func TestDescribeTopQueriesJump(t *testing.T) {
	tbl := pg.Table{DB: "app", Schema: "public", Name: "game_production", OID: 42}
	desc := &screen{level: levelDescribe, tool: toolTools, db: "app", table: tbl, loaded: true,
		desc: describeState{info: &pg.Description{Kind: pg.DescribeTable, OID: 42}}}
	m := newTestModel(desc)
	m.width, m.height = 120, 40

	m.keys.applyContext(desc)
	if !m.keys.TopQueries.Enabled() {
		t.Fatal("t must be enabled on a table's describe panel")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})

	n := len(m.stack)
	if n < 3 || m.stack[n-2].level != levelStatements || m.stack[n-1].level != levelSnapshots {
		t.Fatalf("stack after t = %d screens, want …describe, statements, snapshots", n)
	}
	st := m.stack[n-2]
	if st.db != "app" || st.filter != "game_production" || st.tool != toolQueries {
		t.Errorf("statements screen db=%q filter=%q tool=%v", st.db, st.filter, st.tool)
	}
	if !m.stack[n-1].stat.entry {
		t.Error("the window picker must open in entry mode")
	}

	leaf := &screen{level: levelColumns, tool: toolDisk, loaded: true}
	m.keys.applyContext(leaf)
	if m.keys.TopQueries.Enabled() {
		t.Error("t must stay off away from the describe panel")
	}
}
