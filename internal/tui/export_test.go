package tui

import (
	"testing"

	"pgdu/internal/pg"
)

// newModel builds a bare Model for screenCSV tests — only the screen passed in
// matters, but screenCSV is a method so we need a receiver.
func newModel() *Model { return &Model{} }

func TestScreenCSVTyped(t *testing.T) {
	m := newModel()
	s := &screen{
		level: levelTables,
		items: []item{
			{name: "users", data: pg.Table{Schema: "public", Name: "users", OID: 100, HeapBytes: 2048, IndexesBytes: 512, ToastBytes: 0, TotalBytes: 2560, EstRows: 42}},
			{name: "orders", data: pg.Table{Schema: "public", Name: "orders", OID: 101, HeapBytes: 4096, TotalBytes: 4096, EstRows: 7}},
		},
	}

	header, rows, ok := m.screenCSV(s)
	if !ok {
		t.Fatal("screenCSV returned ok=false for levelTables")
	}
	if header[0] != "schema" || header[1] != "name" {
		t.Fatalf("unexpected header: %v", header)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	// total_bytes is the 7th column (index 6); raw integer, not humanized.
	if rows[0][6] != "2560" {
		t.Errorf("want total_bytes 2560, got %q", rows[0][6])
	}
	if rows[1][1] != "orders" {
		t.Errorf("want name orders, got %q", rows[1][1])
	}
}

func TestScreenCSVGenericRawNumbers(t *testing.T) {
	m := newModel()
	s := &screen{
		level: levelDiagnosticResult,
		diagCols: []pg.DiagColumn{
			{Name: "table", Kind: pg.DiagText},
			{Name: "size", Kind: pg.DiagBytes},
		},
		items: []item{
			{name: "big 12 MB", data: []pg.DiagCell{
				{Display: "big"},
				{Display: "12 MB", Num: 12582912, HasNum: true},
			}},
		},
	}

	header, rows, ok := m.screenCSV(s)
	if !ok {
		t.Fatal("screenCSV returned ok=false for diagnostic result")
	}
	if header[1] != "size" {
		t.Fatalf("unexpected header: %v", header)
	}
	// Numeric cell exports the raw Num, not the humanized Display.
	if rows[0][1] != "12582912" {
		t.Errorf("want raw 12582912, got %q", rows[0][1])
	}
	// Text cell exports its Display.
	if rows[0][0] != "big" {
		t.Errorf("want text 'big', got %q", rows[0][0])
	}
}

func TestScreenCSVRespectsFilter(t *testing.T) {
	m := newModel()
	s := &screen{
		level:  levelTables,
		filter: "ord",
		items: []item{
			{name: "users", data: pg.Table{Schema: "public", Name: "users"}},
			{name: "orders", data: pg.Table{Schema: "public", Name: "orders"}},
		},
	}

	_, rows, ok := m.screenCSV(s)
	if !ok {
		t.Fatal("screenCSV returned ok=false")
	}
	if len(rows) != 1 {
		t.Fatalf("filter should leave 1 row, got %d", len(rows))
	}
	if rows[0][1] != "orders" {
		t.Errorf("want filtered row 'orders', got %q", rows[0][1])
	}
}

func TestScreenCSVUnexportable(t *testing.T) {
	m := newModel()
	for _, l := range []level{levelTools, levelDiagnostics, levelStatementDetail, levelDescribe} {
		s := &screen{level: l}
		if _, _, ok := m.screenCSV(s); ok {
			t.Errorf("level %v should not be exportable", l)
		}
	}
}
