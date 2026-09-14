package tui

import (
	"encoding/binary"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pageinspect"
	"pgdu/internal/pg"
)

// varlena4B wraps a payload in a 4-byte uncompressed varlena header, the way an
// inline value longer than 126 B sits on the page.
func varlena4B(payload []byte) []byte {
	v := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(v, uint32(len(v))<<2)
	copy(v[4:], payload)
	return v
}

// Enter on a column segment opens the value pane — the legend row only has
// room for a prefix — and the pane spells the value out whole, with the stored
// bytes dumped under it. Esc goes back to the legend; Enter on a header field,
// which has nothing more to show, still dismisses the overlay.
func TestTupleLayoutEnterOpensValuePane(t *testing.T) {
	note := strings.Repeat("lorem ipsum ", 30) // 360 B, many legend rows' worth
	val := varlena4B([]byte(note))
	hoff := int32(24)
	tup := pg.HeapTuple{
		LP: 1, LPFlags: pg.LPNormal, LPLen: hoff + int32(len(val)),
		Infomask2: 1, Infomask: pg.HeapHasVarWidth, Hoff: &hoff, Data: val}

	m := &Model{width: 120, height: 40, keys: defaultKeys()}
	m.showTupleLayout = true
	s := &screen{level: levelHeapTuples, items: []item{{data: tup}}}
	s.pages.tupleAttrsLP = 1
	s.pages.tupleAttrs = []pg.TupleAttr{{
		Attnum: 1, Name: "note", TypeName: "text", Len: -1, Align: "i", Stored: true,
		TypName: "text", TypCategory: "S", Value: val}}

	// Park the legend cursor on the one column row (the header fields and the
	// pad before it are in physical order, but don't hardcode how many).
	col := -1
	for rank := range 16 {
		m.tupleLayoutCursor = rank
		if sg, ok := m.tupleLayoutSegUnderCursor(s); ok && sg.Kind == pageinspect.SegColumn {
			col = rank
			break
		}
	}
	if col < 0 {
		t.Fatal("no column segment in the layout")
	}
	if !m.tupleLayoutValueUnderCursor(s) {
		t.Fatal("a column holding bytes must offer the value pane")
	}
	if got := stripANSI(m.renderTupleLayout(s, 30)); !strings.Contains(got, "↵ → value") {
		t.Error("the layout title must advertise Enter on a column row")
	}

	m.handleTupleLayoutKey(s, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.showTupleValue || m.tupleValueOffset != 0 {
		t.Fatalf("enter on the column: pane=%v offset=%d, want it open at the top", m.showTupleValue, m.tupleValueOffset)
	}

	body := stripANSI(m.renderTupleValue(s))
	decoded, dump, split := strings.Cut(body, " stored bytes ")
	if !split {
		t.Fatalf("pane has no stored-bytes dump:\n%s", body)
	}
	if n := strings.Count(decoded, "lorem"); n != 30 {
		t.Errorf("decoded section shows %d of 30 occurrences — the value is not spelled out whole", n)
	}
	if !strings.Contains(body, "note") || !strings.Contains(body, "text") {
		t.Error("pane must name the column and its type")
	}
	if !strings.Contains(dump, "0000  ") || !strings.Contains(dump, "|lorem ipsum lore|") {
		t.Errorf("pane must dump the stored bytes:\n%s", dump)
	}

	// Esc returns to the legend, leaving the layout itself open.
	m.handleTupleLayoutKey(s, tea.KeyMsg{Type: tea.KeyEsc})
	if m.showTupleValue || !m.showTupleLayout {
		t.Fatalf("esc: pane=%v layout=%v, want back on the legend", m.showTupleValue, m.showTupleLayout)
	}

	m.tupleLayoutCursor = 0 // t_xmin — fully spelled out in place
	if m.tupleLayoutValueUnderCursor(s) {
		t.Error("a header field has no value pane")
	}
	m.handleTupleLayoutKey(s, tea.KeyMsg{Type: tea.KeyEnter})
	if m.showTupleLayout || m.showTupleValue {
		t.Errorf("enter on a header field: layout=%v pane=%v, want the overlay dismissed", m.showTupleLayout, m.showTupleValue)
	}
}

// The pane re-indents a value that is a JSON document (jsonb decodes to one
// canonical line) and leaves everything else exactly as decoded.
func TestReindentJSON(t *testing.T) {
	got := reindentJSON(`{"hero": {"level": 20}}`)
	if strings.Count(got, "\n") == 0 || !strings.Contains(got, `"level": 20`) {
		t.Errorf("json value = %q, want it indented over several lines", got)
	}
	for _, plain := range []string{`\xdeadbeef`, "→ toast chunk 42 · 1.00 MB", "compressed · 4.48 KB raw", ""} {
		if got := reindentJSON(plain); got != plain {
			t.Errorf("reindentJSON(%q) = %q, want it untouched", plain, got)
		}
	}
}

// The legend paints the ↵ drill mark in front of exactly the rows Enter acts
// on — columns holding bytes — and leaves the slot blank on header fields, so
// the affordance reads the same as every other list.
func TestTupleLayoutDrillMark(t *testing.T) {
	val := varlena4B([]byte("hello, world — long enough to be a real value"))
	hoff := int32(24)
	tup := pg.HeapTuple{
		LP: 1, LPFlags: pg.LPNormal, LPLen: hoff + int32(len(val)),
		Infomask2: 1, Infomask: pg.HeapHasVarWidth, Hoff: &hoff, Data: val}
	m := &Model{width: 120, height: 40, keys: defaultKeys()}
	m.showTupleLayout = true
	s := &screen{level: levelHeapTuples, items: []item{{data: tup}}}
	s.pages.tupleAttrsLP = 1
	s.pages.tupleAttrs = []pg.TupleAttr{{
		Attnum: 1, Name: "note", TypeName: "text", Len: -1, Align: "i", Stored: true,
		TypName: "text", TypCategory: "S", Value: val}}

	got := stripANSI(m.renderTupleLayout(s, 30))
	var marked, plain []string
	for _, ln := range strings.Split(got, "\n") {
		switch {
		case strings.Contains(ln, "↵ note"):
			marked = append(marked, ln)
		case strings.Contains(ln, "t_xmin"):
			plain = append(plain, ln)
		}
	}
	if len(marked) != 1 {
		t.Errorf("want the column row marked ↵ once, got %d in:\n%s", len(marked), got)
	}
	if len(plain) != 1 || strings.Contains(plain[0], "↵") {
		t.Errorf("header field row must not carry a drill mark: %q", plain)
	}
}
