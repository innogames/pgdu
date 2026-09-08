package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/pg"
)

// colOf is the display column at which needle starts in line, or -1. Lines
// carry multi-byte glyphs (▶ ↵ ░ —), so byte offsets would not line up.
func colOf(line, needle string) int {
	before, _, ok := strings.Cut(line, needle)
	if !ok {
		return -1
	}
	return lipgloss.Width(before)
}

// drillMark is the one place the ↵ glyph lives: two cells wide either way so
// the name column never shifts, and blank on leaf rows.
func TestDrillMark(t *testing.T) {
	for _, tc := range []struct {
		drill bool
		want  string
	}{{true, "↵ "}, {false, "  "}} {
		got := drillMark(tc.drill)
		if stripANSI(got) != tc.want {
			t.Errorf("drillMark(%v) = %q, want %q", tc.drill, stripANSI(got), tc.want)
		}
		if w := lipgloss.Width(got); w != colMark {
			t.Errorf("drillMark(%v) is %d cells wide, want colMark=%d", tc.drill, w, colMark)
		}
	}
	if !anyDrillable([]item{{}, {hasChildren: true}}) || anyDrillable([]item{{}, {}}) {
		t.Error("anyDrillable must report exactly whether some row is flagged")
	}
}

// The generic barred row paints the mark right before the name and only when
// the item is flagged.
func TestRenderRowDrillMark(t *testing.T) {
	base := row{size: 100, maxSize: 100, barW: 10, name: "orders"}
	with := base
	with.hasChildren = true
	if got := stripANSI(renderRow(with)); !strings.Contains(got, "↵ orders") {
		t.Errorf("drillable row lacks the mark: %q", got)
	}
	if got := stripANSI(renderRow(base)); strings.Contains(got, "↵") {
		t.Errorf("leaf row must not carry a mark: %q", got)
	}
}

// The generic column table adds a two-cell mark slot after the cursor only when
// some row drills, and keeps header, rows and the Σ footer aligned either way.
func TestRenderDiagResultDrillMark(t *testing.T) {
	cols := []pg.DiagColumn{{Name: "table", Kind: pg.DiagText}, {Name: "rows", Kind: pg.DiagInt}}
	cell := func(s string) pg.DiagCell { return pg.DiagCell{Display: s} }
	mk := func(drill ...bool) *screen {
		s := &screen{level: levelTableStats, tool: toolTableStats, loaded: true, diagCols: cols, diagBarCol: -1}
		for i, d := range drill {
			s.items = append(s.items, item{name: "t" + string(rune('a'+i)), hasChildren: d,
				data: []pg.DiagCell{cell("t" + string(rune('a'+i))), cell("42")}})
		}
		s.diagTotalRow = []pg.DiagCell{cell("Σ"), cell("84")}
		s.diagMetricsDirty = true
		return s
	}
	render := func(s *screen) []string {
		m := newTestModel(s)
		m.width, m.height = 100, 20
		out := stripANSI(m.renderDiagResult(s, 6))
		return strings.Split(strings.TrimRight(out, "\n"), "\n")
	}

	leaf := render(mk(false, false))
	if strings.Contains(strings.Join(leaf, "\n"), "↵") {
		t.Errorf("leaf table must not paint a mark:\n%s", strings.Join(leaf, "\n"))
	}
	if !strings.HasPrefix(leaf[0], "  table") {
		t.Errorf("leaf header must start right after the cursor slot: %q", leaf[0])
	}

	mixed := render(mk(true, false))
	if !strings.HasPrefix(mixed[0], "    table") {
		t.Errorf("drillable table header must leave a mark slot: %q", mixed[0])
	}
	if !strings.HasPrefix(mixed[1], "▶ ↵ ta") || !strings.HasPrefix(mixed[2], "    tb") {
		t.Errorf("rows must paint the mark only when flagged:\n%q\n%q", mixed[1], mixed[2])
	}
	// The slot shifts every line by exactly colMark against the leaf rendering,
	// header, rows and Σ footer alike, so nothing drifts between them.
	for i, needle := range []string{"rows", "42", "42", "84"} {
		if got, want := colOf(mixed[i], needle), colOf(leaf[i], needle)+colMark; got != want {
			t.Errorf("line %d: %q at column %d, want %d\n%q\n%q", i, needle, got, want, leaf[i], mixed[i])
		}
	}
}

// Tuple renderers take raw structs, so the flag rides in as a parameter: the
// mark sits right after the cursor, before the "#NNNN" offset.
func TestTupleRowsDrillMark(t *testing.T) {
	ht := pg.HeapTuple{LP: 5, LPFlags: pg.LPNormal}
	if got := stripANSI(renderHeapTupleHeadline(ht, true, false, nil)); !strings.HasPrefix(got, "  ↵ #0005") {
		t.Errorf("drillable heap tuple = %q", got)
	}
	if got := stripANSI(renderHeapTupleHeadline(ht, false, false, nil)); !strings.HasPrefix(got, "    #0005") {
		t.Errorf("leaf heap tuple = %q", got)
	}
	hdr := stripANSI(renderHeapTuplesHeader(sortByLP, false, nil))
	if !strings.HasPrefix(hdr, "    lp") {
		t.Errorf("heap tuples header must reserve the mark slot: %q", hdr)
	}
	it := pg.IndexTuple{ItemOffset: 3}
	if got := stripANSI(renderIndexTupleRow(it, "l", idxRowOpts{drill: true}, nil, 60, false)); !strings.HasPrefix(got, "  ↵ #0003") {
		t.Errorf("drillable index tuple = %q", got)
	}
	if got := stripANSI(renderIndexTupleRow(it, "l", idxRowOpts{}, nil, 60, false)); !strings.HasPrefix(got, "    #0003") {
		t.Errorf("leaf index tuple = %q", got)
	}
}

// Page rows paint the mark from the item flag, in the slot before the name.
func TestHeapPageRowDrillMark(t *testing.T) {
	it := heapPageToItem(pg.HeapPageStat{Blkno: 7, FreeBytes: 100})
	row := stripANSI(renderHeapPageRow(it, it.data.(pg.HeapPageStat), 10, false, false))
	if !strings.Contains(row, "↵ page #0000007") {
		t.Errorf("heap page row lacks the mark: %q", row)
	}
	hdr := stripANSI(renderHeapPagesHeader(sortByBlkno, false, 10, false))
	if colOf(hdr, "page") != colOf(row, "↵ page")+colMark {
		t.Errorf("page header not above the name:\n%q\n%q", hdr, row)
	}
}
