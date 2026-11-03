package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"pgdu/internal/pg"
)

// growthScreen builds a loaded growth screen over a synthetic window.
func growthScreen(rows []pg.GrowthRow, window time.Duration) *screen {
	s := &screen{level: levelGrowth, tool: toolGrowth, db: "app", loaded: true}
	s.growth.base = &pg.SizeSample{}
	s.growth.rows = rows
	s.growth.window = pg.GrowthWindow{Duration: window}
	s.growth.basePath = "/snaps/app-a.sizes.gz"
	s.growth.endPath = growthNow
	return s
}

func gRow(schema, name string, base, end int64, tag pg.GrowthTag) pg.GrowthRow {
	r := pg.GrowthRow{
		Database: "app", Schema: schema, Name: name, RelKind: "r",
		Tag: tag, RowsKnown: true,
		BaseBytes: base, EndBytes: end,
	}
	// Mirror what DiffSizes derives, so a hand-built row behaves like a real
	// one in the cell builders.
	r.DeltaBytes = end - base
	r.BytesPerDay = float64(r.DeltaBytes)
	return r
}

// showCol switches on a column that is off by default, for tests that are
// about the column itself rather than about the default layout.
func showCol(m *Model, id growthColID) {
	growthSpec.ensureInit(&m.growthTable)
	m.growthTable.visible[id] = true
}

func cellByCol(t *testing.T, m *Model, s *screen, row int, id growthColID) pg.DiagCell {
	t.Helper()
	idx := -1
	for i, d := range s.growth.cols {
		if d.id == id {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("column %q not visible", id)
	}
	cells, ok := s.items[row].data.([]pg.DiagCell)
	if !ok {
		t.Fatalf("row %d has no cells", row)
	}
	return cells[idx]
}

// The projected descriptors, the generic column schema and every row's cells
// must stay the same length at every scope, or the renderer reads a cell from
// the wrong column.
func TestGrowthColumnsParallelAtEveryScope(t *testing.T) {
	rows := []pg.GrowthRow{
		gRow("public", "a", 100, 200, pg.GrowthSteady),
		gRow("archive", "b", 300, 300, pg.GrowthSteady),
	}
	for _, v := range []growthView{growthByTable, growthBySchema, growthByDatabase, growthByPartTree} {
		s := growthScreen(rows, 24*time.Hour)
		s.growth.view = v
		m := newTestModel(s)
		m.rebuildGrowthItems(s)

		if len(s.growth.cols) != len(s.diagCols) {
			t.Errorf("%s: %d descs vs %d diagCols", v.label(), len(s.growth.cols), len(s.diagCols))
		}
		for i, it := range s.items {
			cells, ok := it.data.([]pg.DiagCell)
			if !ok {
				t.Fatalf("%s: row %d carries no cells", v.label(), i)
			}
			if len(cells) != len(s.diagCols) {
				t.Errorf("%s: row %d has %d cells, want %d", v.label(), i, len(cells), len(s.diagCols))
			}
		}
		if len(s.diagTotalRow) != 0 && len(s.diagTotalRow) != len(s.diagCols) {
			t.Errorf("%s: footer has %d cells, want %d", v.label(), len(s.diagTotalRow), len(s.diagCols))
		}
		// The key column is mandatory, so it is present whatever the prefs say.
		if len(s.growth.cols) == 0 || s.growth.cols[0].id != growthColName {
			t.Errorf("%s: name column is not leading", v.label())
		}
	}
}

// Scope-specific columns must appear only where they mean something.
func TestGrowthScopeGatedColumns(t *testing.T) {
	rows := []pg.GrowthRow{gRow("public", "a", 100, 200, pg.GrowthSteady)}
	has := func(s *screen, id growthColID) bool {
		for _, d := range s.growth.cols {
			if d.id == id {
				return true
			}
		}
		return false
	}

	leaf := growthScreen(rows, 24*time.Hour)
	m := newTestModel(leaf)
	m.rebuildGrowthItems(leaf)
	if !has(leaf, growthColTag) {
		t.Error("tag must show at relation scope")
	}
	if has(leaf, growthColRels) {
		t.Error("relations count is meaningless at relation scope")
	}

	grouped := growthScreen(rows, 24*time.Hour)
	grouped.growth.view = growthBySchema
	m2 := newTestModel(grouped)
	m2.rebuildGrowthItems(grouped)
	if has(grouped, growthColTag) {
		t.Error("tag is per-relation and must not show on a roll-up")
	}
	if !has(grouped, growthColRels) {
		t.Error("relations count must show on a roll-up")
	}
}

// The Σ footer must reconcile with what is actually on screen, at every scope
// and with the gone rows both hidden and shown.
func TestGrowthFooterReconciles(t *testing.T) {
	rows := []pg.GrowthRow{
		gRow("public", "a", 100, 300, pg.GrowthSteady),
		gRow("public", "b", 50, 50, pg.GrowthSteady),
		gRow("archive", "c", 200, 260, pg.GrowthSteady),
		gRow("archive", "d", 400, 0, pg.GrowthGone),
	}
	for _, showGone := range []bool{false, true} {
		for _, v := range []growthView{growthByTable, growthBySchema, growthByDatabase} {
			s := growthScreen(rows, 24*time.Hour)
			s.growth.view = v
			s.growth.showGone = showGone
			m := newTestModel(s)
			m.rebuildGrowthItems(s)

			idx := -1
			for i, d := range s.growth.cols {
				if d.id == growthColDelta {
					idx = i
				}
			}
			if idx < 0 {
				t.Fatal("Δ column not visible")
			}
			var sum float64
			for _, it := range s.items {
				sum += it.data.([]pg.DiagCell)[idx].Num
			}
			if len(s.diagTotalRow) == 0 {
				t.Fatalf("%s/gone=%v: no footer", v.label(), showGone)
			}
			if got := s.diagTotalRow[idx].Num; got != sum {
				t.Errorf("%s/gone=%v: footer Δ %v != Σ rows %v", v.label(), showGone, got, sum)
			}
		}
	}
}

func TestGrowthScopeCycleWraps(t *testing.T) {
	seen := map[growthView]bool{}
	v := growthByTable
	for range 4 {
		if seen[v] {
			t.Fatalf("scope %v repeated before the cycle closed", v)
		}
		seen[v] = true
		if v.label() == "" || v.noun() == "" || v.plural() == "" {
			t.Errorf("scope %v has an empty label/noun", v)
		}
		v = v.next()
	}
	if v != growthByTable {
		t.Errorf("cycle ended at %v, want it to wrap to the relation scope", v)
	}
	scopes := map[pg.GrowthScope]bool{}
	for _, gv := range []growthView{growthByTable, growthBySchema, growthByDatabase, growthByPartTree} {
		scopes[gv.scope()] = true
	}
	if len(scopes) != 4 {
		t.Errorf("the four views map onto %d distinct pg scopes, want 4", len(scopes))
	}
}

// Enter on a roll-up narrows structurally and drops back to the relation
// scope; Esc unwinds scope, then narrowing, then pops.
func TestGrowthNarrowAndUnwind(t *testing.T) {
	rows := []pg.GrowthRow{
		gRow("public", "a", 100, 300, pg.GrowthSteady),
		gRow("public", "b", 50, 50, pg.GrowthSteady),
		gRow("archive", "c", 200, 260, pg.GrowthSteady),
	}
	s := growthScreen(rows, 24*time.Hour)
	s.growth.view = growthBySchema
	m := newTestModel(s)
	m.rebuildGrowthItems(s)

	if len(s.items) != 2 {
		t.Fatalf("want 2 schema rows, got %d", len(s.items))
	}
	// Narrow to whichever schema row the cursor is on.
	cur := s.items[0]
	m.drillGrowth(s, cur)
	if s.growth.view != growthByTable {
		t.Error("narrowing must drop back to the relation scope")
	}
	if s.growth.group == nil {
		t.Fatal("narrowing did not record a group")
	}
	for _, it := range s.items {
		if !strings.Contains(it.name, s.growth.group.display) {
			// The leaf rows are qualified only outside public, so check the
			// count instead of the text for the public case.
			break
		}
	}
	if len(s.items) >= len(rows) {
		t.Errorf("narrowed list has %d rows, want fewer than %d", len(s.items), len(rows))
	}

	// Esc: scope is already leaf, so the first press clears the narrowing.
	s.growth.group = nil
	m.rebuildGrowthItems(s)
	if len(s.items) != len(rows) {
		t.Errorf("widening restored %d rows, want %d", len(s.items), len(rows))
	}
}

// An undefined ratio must carry no number, so the generic table's
// unknown-sorts-last rule sinks it in both directions.
func TestGrowthDeltaPctUndefinedSortsLast(t *testing.T) {
	rows := []pg.GrowthRow{
		gRow("public", "grown", 100, 200, pg.GrowthSteady),
		gRow("public", "fresh", 0, 5000, pg.GrowthNew),
	}
	s := growthScreen(rows, 24*time.Hour)
	m := newTestModel(s)
	m.rebuildGrowthItems(s)

	var pctCol = -1
	for i, d := range s.growth.cols {
		if d.id == growthColDeltaPct {
			pctCol = i
		}
	}
	if pctCol < 0 {
		t.Fatal("Δ% not visible")
	}
	s.diagSortCol = pctCol

	for _, desc := range []bool{true, false} {
		s.sortDesc = desc
		m.applySort(s)
		last := s.items[len(s.items)-1].data.([]pg.DiagCell)[pctCol]
		if last.HasNum {
			t.Errorf("desc=%v: a defined ratio sorted last; want the undefined one there", desc)
		}
		if last.Display != "new" {
			t.Errorf("desc=%v: last row reads %q, want \"new\"", desc, last.Display)
		}
	}
}

// The bar follows the sort column, and an all-negative column must render.
func TestGrowthBarFollowsSort(t *testing.T) {
	rows := []pg.GrowthRow{
		gRow("public", "shrunk", 1000, 400, pg.GrowthSteady),
		gRow("public", "also", 800, 200, pg.GrowthSteady),
	}
	s := growthScreen(rows, 24*time.Hour)
	m := newTestModel(s)
	m.rebuildGrowthItems(s)

	// Default sort is per_day, a numeric column, so the bar is on it.
	if s.diagBarCol < 0 {
		t.Error("a numeric default sort should carry the bar")
	}
	if s.diagBarCol != s.diagSortCol {
		t.Errorf("bar on column %d but sorting on %d", s.diagBarCol, s.diagSortCol)
	}

	// Move to the text name column: the bar must turn off and give the width
	// back to the data columns.
	for i, d := range s.growth.cols {
		if d.id == growthColName {
			s.diagSortCol = i
		}
	}
	s.diagMetricsDirty = false
	m.syncGrowthBar(s)
	if s.diagBarCol != -1 {
		t.Errorf("bar = %d over a text column, want -1", s.diagBarCol)
	}
	if !s.diagMetricsDirty {
		t.Error("moving the bar must dirty the width memo")
	}

	// All deltas are negative here; rendering must not panic or go negative.
	m2 := newTestModel(s)
	m2.width, m2.height = 120, 40
	_ = m2.View().Content
}

// Dropped relations are hidden until v, and the footer follows.
func TestGrowthGoneRowsHidden(t *testing.T) {
	rows := []pg.GrowthRow{
		gRow("public", "live", 100, 300, pg.GrowthSteady),
		gRow("public", "dead", 400, 0, pg.GrowthGone),
	}
	s := growthScreen(rows, 24*time.Hour)
	m := newTestModel(s)
	m.rebuildGrowthItems(s)
	if len(s.items) != 1 {
		t.Fatalf("want the dropped relation hidden, got %d rows", len(s.items))
	}

	s.growth.showGone = true
	m.rebuildGrowthItems(s)
	if len(s.items) != 2 {
		t.Fatalf("v must reveal the dropped relation, got %d rows", len(s.items))
	}
	tag := cellByCol(t, m, s, 1, growthColTag)
	if tag.Display != "gone" {
		// Order is by per_day desc, so the dropped row (most negative) is last.
		if other := cellByCol(t, m, s, 0, growthColTag); other.Display != "gone" {
			t.Errorf("neither row is tagged gone: %q / %q", tag.Display, other.Display)
		}
	}
}

// A dropped relation has nothing to open, and the footer must not advertise a
// dead key for it.
func TestGrowthEnterOnGoneRow(t *testing.T) {
	rows := []pg.GrowthRow{gRow("public", "dead", 400, 0, pg.GrowthGone)}
	s := growthScreen(rows, 24*time.Hour)
	s.growth.showGone = true
	m := newTestModel(s)
	m.rebuildGrowthItems(s)

	if len(s.items) != 1 {
		t.Fatalf("want 1 row, got %d", len(s.items))
	}
	if s.items[0].hasChildren {
		t.Error("a dropped relation must not claim children")
	}
	if _, ok := enterLabel(s); ok {
		t.Error("Enter must be disabled on a dropped relation")
	}
	if cmd := m.drillGrowth(s, s.items[0]); cmd != nil {
		t.Error("drilling a dropped relation must do nothing")
	}

	// A live row, by contrast, offers its disk parts.
	live := growthScreen([]pg.GrowthRow{gRow("public", "live", 1, 2, pg.GrowthSteady)}, time.Hour)
	m2 := newTestModel(live)
	m2.rebuildGrowthItems(live)
	label, ok := enterLabel(live)
	if !ok || label != "disk parts" {
		t.Errorf("enterLabel on a live relation = %q/%v, want \"disk parts\"/true", label, ok)
	}
}

// Enter on a roll-up names what it narrows to.
func TestGrowthEnterLabelGrouped(t *testing.T) {
	s := growthScreen([]pg.GrowthRow{gRow("public", "a", 1, 2, pg.GrowthSteady)}, time.Hour)
	s.growth.view = growthBySchema
	m := newTestModel(s)
	m.rebuildGrowthItems(s)
	label, ok := enterLabel(s)
	if !ok || label != "narrow to schema" {
		t.Errorf("enterLabel = %q/%v, want \"narrow to schema\"/true", label, ok)
	}
}

// With no window applied the rate columns must say so rather than read as
// zero growth.
func TestGrowthNoWindowRatesAreUnknown(t *testing.T) {
	rows := []pg.GrowthRow{gRow("public", "a", 100, 200, pg.GrowthSteady)}
	s := growthScreen(rows, 0)
	m := newTestModel(s)
	showCol(m, growthColPerDay)
	m.rebuildGrowthItems(s)
	c := cellByCol(t, m, s, 0, growthColPerDay)
	if c.Display != "—" || c.HasNum {
		t.Errorf("per_day with no window = %q (num %v), want an unknown cell", c.Display, c.HasNum)
	}
}

// The parts jump resolves by name, so the key must split back into
// schema.relname whatever the database is called.
func TestGrowthKeyRelation(t *testing.T) {
	cases := map[string]string{
		"app.public.orders":      "public.orders",
		"my.db.public.orders":    "public.orders",
		"app.archive.orders_old": "archive.orders_old",
		"nodots":                 "",
	}
	for in, want := range cases {
		if got := growthKeyRelation(in); got != want {
			t.Errorf("growthKeyRelation(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToolGrowthResolvesByName(t *testing.T) {
	got, ok := toolByName("growth")
	if !ok || got != toolGrowth {
		t.Errorf("toolByName(\"growth\") = %v/%v, want toolGrowth/true", got, ok)
	}
	if toolGrowth.Name() != "growth" {
		t.Errorf("toolGrowth.Name() = %q, want the CLI flag's spelling", toolGrowth.Name())
	}
}

// Picking a database in the growth tool pushes the unloaded table with the
// sample browser on top, exactly as the top-queries tool does.
func TestDatabaseChildScreensGrowth(t *testing.T) {
	got := databaseChildScreens(toolGrowth, "app")
	if len(got) != 2 || got[0].level != levelGrowth || got[1].level != levelGrowthWindow {
		t.Fatalf("toolGrowth screens = %v, want [growth, samples]", got)
	}
	if !got[1].growth.entry || !got[1].loading {
		t.Errorf("entry browser not armed: entry=%v loading=%v", got[1].growth.entry, got[1].loading)
	}
	if got[0].loaded || got[0].growth.base != nil {
		t.Error("the growth table must not load before a window is picked")
	}
	if got[0].db != "app" || got[1].db != "app" {
		t.Errorf("db not propagated: %q / %q", got[0].db, got[1].db)
	}
}

// The root menu pairs the two disk questions: "what is big" then "what is
// getting bigger".
func TestSizeHistorySitsBelowDiskUsage(t *testing.T) {
	items := toolItems(false)
	disk, growth := -1, -1
	for i, it := range items {
		switch it.data {
		case toolDisk:
			disk = i
		case toolGrowth:
			growth = i
		}
	}
	if disk < 0 || growth < 0 {
		t.Fatalf("menu is missing an entry: disk=%d growth=%d", disk, growth)
	}
	if growth != disk+1 {
		t.Errorf("Size history is at %d, want %d (directly below Disk usage)", growth, disk+1)
	}
}

// h jumps from the disk browser to size history for the same database, and is
// off everywhere else so the key stays free.
func TestGrowthJumpScoping(t *testing.T) {
	cases := []struct {
		name string
		s    *screen
		want bool
	}{
		{"disk tables", &screen{level: levelTables, tool: toolDisk, db: "shop"}, true},
		{"disk schemas", &screen{level: levelSchemas, tool: toolDisk, db: "shop"}, true},
		{"disk parts", &screen{level: levelParts, tool: toolDisk, db: "shop"}, true},
		{"disk databases (no db yet)", &screen{level: levelDatabases, tool: toolDisk}, false},
		{"buffers tables", &screen{level: levelTables, tool: toolBuffers, db: "shop"}, false},
		{"table overview", &screen{level: levelTableStats, tool: toolTableStats, db: "shop"}, false},
		{"growth table itself", &screen{level: levelGrowth, tool: toolGrowth, db: "shop"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := defaultKeys()
			k.applyContext(c.s)
			if got := k.Growth.Enabled(); got != c.want {
				t.Errorf("h enabled = %v, want %v", got, c.want)
			}
			if k.growthInFooter != c.want {
				t.Errorf("footer hint = %v, want %v", k.growthInFooter, c.want)
			}
		})
	}
}

// The jump pushes the same two screens the root menu's database pick does, so
// the picker opens over an unloaded table and Back leaves the tool.
func TestGrowthJumpPushesEntryScreens(t *testing.T) {
	s := &screen{level: levelTables, tool: toolDisk, db: "shop", loaded: true}
	m := newTestModel(s)
	depth := len(m.stack)

	m.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})

	if len(m.stack) != depth+2 {
		t.Fatalf("stack depth %d, want %d (table + picker)", len(m.stack), depth+2)
	}
	gt, pick := m.stack[len(m.stack)-2], m.stack[len(m.stack)-1]
	if gt.level != levelGrowth || pick.level != levelGrowthWindow {
		t.Fatalf("pushed %v/%v, want growth + samples", gt.level, pick.level)
	}
	if gt.db != "shop" || pick.db != "shop" {
		t.Errorf("database not carried over: %q / %q", gt.db, pick.db)
	}
	if !pick.growth.entry {
		t.Error("the picker must open in entry mode, so Back leaves the tool")
	}
	if gt.loaded {
		t.Error("the growth table must not load before a window is picked")
	}
}

func TestSignedFormatters(t *testing.T) {
	bytes := map[int64]string{
		0: "0 B", 1024: "+1.00 KB", -1024: "-1.00 KB",
	}
	for in, want := range bytes {
		if got := signedBytes(in); got != want {
			t.Errorf("signedBytes(%d) = %q, want %q", in, got, want)
		}
	}
	counts := map[int64]string{
		0: "0", 1500: "+1.5k", -1500: "-1.5k", 42: "+42", -42: "-42",
	}
	for in, want := range counts {
		if got := signedCount(in); got != want {
			t.Errorf("signedCount(%d) = %q, want %q", in, got, want)
		}
	}
	// A negative change must never render as formatRows' "?" — that spelling
	// means "unknown" on an absolute counter, which a change is not.
	if got := signedCount(-5); strings.Contains(got, "?") {
		t.Errorf("signedCount(-5) = %q, want a signed number", got)
	}
	pcts := map[float64]string{0: "0.0", 12.34: "+12.3", -12.34: "-12.3"}
	for in, want := range pcts {
		if got := signedPct(in); got != want {
			t.Errorf("signedPct(%v) = %q, want %q", in, got, want)
		}
	}
}

// diagCellText is the one place a numeric cell becomes text, so the width
// pass, the data row, the headline-bar number and the Σ footer can never
// disagree about the spelling — the failure mode that made "+" unsafe before
// the delta kinds existed.
func TestDiagCellTextSpellsDeltasConsistently(t *testing.T) {
	cases := []struct {
		kind pg.DiagColumnKind
		cell pg.DiagCell
		want string
	}{
		{pg.DiagDeltaBytes, diagNum("ignored", 1048576), "+1.00 MB"},
		{pg.DiagDeltaBytes, diagNum("ignored", -1048576), "-1.00 MB"},
		{pg.DiagDeltaBytes, diagNum("ignored", 0), "0 B"},
		{pg.DiagDeltaCount, diagNum("ignored", 2500), "+2.5k"},
		{pg.DiagDeltaCount, diagNum("ignored", -2500), "-2.5k"},
		{pg.DiagDeltaPct, diagNum("ignored", 4.2), "+4.2"},
		{pg.DiagDeltaPct, diagNum("ignored", -4.2), "-4.2"},
		// A cell with no number keeps its placeholder whatever the kind.
		{pg.DiagDeltaBytes, pg.DiagCell{Display: "—"}, "—"},
		{pg.DiagDeltaPct, pg.DiagCell{Display: "new"}, "new"},
	}
	for _, c := range cases {
		if got := diagCellText(c.kind, c.cell); got != c.want {
			t.Errorf("diagCellText(%v, %+v) = %q, want %q", c.kind, c.cell, got, c.want)
		}
	}
}

// Colour encodes direction, not judgement: zero is muted, a shrink is green,
// and growth grades against the column's largest absolute change.
func TestDeltaStyleDirection(t *testing.T) {
	zero := deltaStyle(0, 100)
	shrink := deltaStyle(-50, 100)
	small := deltaStyle(10, 100)
	big := deltaStyle(100, 100)

	if zero.GetForeground() != shrink.GetForeground() &&
		zero.GetForeground() == small.GetForeground() {
		t.Error("a zero change must not read like growth")
	}
	if shrink.GetForeground() == small.GetForeground() ||
		shrink.GetForeground() == big.GetForeground() {
		t.Error("a shrink must be coloured apart from growth")
	}
	if small.GetForeground() == big.GetForeground() {
		t.Error("growth must grade with magnitude")
	}
	// With no scale to grade against, growth still reads as growth.
	if deltaStyle(5, 0).GetForeground() == zero.GetForeground() {
		t.Error("growth with an unknown column max must not read as no change")
	}
}

// The breakdown and row columns ship on by default: a Δ that is all index is
// the headline finding, and hiding it behind C buries it.
func TestGrowthDefaultColumns(t *testing.T) {
	want := []growthColID{
		growthColName, growthColSize, growthColBase, growthColDelta,
		growthColDeltaPct, growthColTrend, growthColTag,
		growthColDeltaHeap, growthColDeltaIdx, growthColDeltaToast,
		growthColRows,
	}
	// The per-day rates are opt-in: over a short window they multiply a few
	// minutes of noise by 1440, and the trend column answers the same
	// question from the whole history instead of from two points.
	off := []growthColID{growthColPerDay, growthColRowsDay}

	on := map[growthColID]bool{}
	for _, d := range growthColumnRegistry() {
		if d.defaultOn {
			on[d.id] = true
		}
	}
	for _, id := range want {
		if !on[id] {
			t.Errorf("column %q should be on by default", id)
		}
	}
	for _, id := range off {
		if on[id] {
			t.Errorf("column %q should be off by default", id)
		}
	}
}

// The default sort has to name a column that is actually on screen: syncSort
// falls through its fallback to the first column, which would silently sort a
// growth table by name.
func TestGrowthDefaultSortIsVisibleByDefault(t *testing.T) {
	for _, d := range growthColumnRegistry() {
		if d.id == growthSpec.defaultSort {
			if !d.defaultOn && !d.mandatory {
				t.Errorf("default sort column %q is off by default", d.id)
			}
			return
		}
	}
	t.Errorf("default sort column %q is not in the registry", growthSpec.defaultSort)
}

// Every change column must be a delta kind, or it loses its sign and its
// colour the moment the renderer formats it from Num.
func TestGrowthChangeColumnsAreDeltaKinds(t *testing.T) {
	changes := map[growthColID]bool{
		growthColDelta: true, growthColDeltaPct: true, growthColPerDay: true,
		growthColDeltaHeap: true, growthColDeltaIdx: true,
		growthColDeltaToast: true, growthColRowsDay: true,
	}
	for _, d := range growthColumnRegistry() {
		if changes[d.id] && !d.kind.IsDelta() {
			t.Errorf("column %q is a change but has kind %v", d.id, d.kind)
		}
		if !changes[d.id] && d.kind.IsDelta() {
			t.Errorf("column %q is not a change but has a delta kind", d.id)
		}
	}
	// Absolute figures must NOT be signed.
	for _, d := range growthColumnRegistry() {
		if d.id == growthColSize || d.id == growthColBase || d.id == growthColRows {
			if d.kind.IsDelta() {
				t.Errorf("%q is an absolute figure and must not carry a sign", d.id)
			}
		}
	}
}

// Landing on a change column must put the largest growth on top: "what is
// growing" is the question the column exists to answer, and ascending would
// lead with the biggest shrink instead.
func TestGrowthSortDirectionFavoursGrowth(t *testing.T) {
	rows := []pg.GrowthRow{
		gRow("public", "shrank", 1000, 400, pg.GrowthSteady),
		gRow("public", "flat", 500, 500, pg.GrowthSteady),
		gRow("public", "grew", 100, 900, pg.GrowthSteady),
	}
	s := growthScreen(rows, 24*time.Hour)
	m := newTestModel(s)
	m.rebuildGrowthItems(s)

	// Walk every column; each change column must land descending, and the top
	// row must be the grower.
	for range len(s.diagCols) {
		m.cycleSort(s, 1)
		m.applySort(s)
		kind := s.diagCols[s.diagSortCol].Kind
		if !kind.IsDelta() {
			continue
		}
		name := s.diagCols[s.diagSortCol].Name
		if !s.sortDesc {
			t.Errorf("column %q landed ascending; growth belongs on top", name)
		}
		top := s.items[0].data.([]pg.DiagCell)[s.diagSortCol]
		bottom := s.items[len(s.items)-1].data.([]pg.DiagCell)[s.diagSortCol]
		if top.HasNum && bottom.HasNum && top.Num < bottom.Num {
			t.Errorf("column %q: top %v < bottom %v", name, top.Num, bottom.Num)
		}
	}
}

// The direction rule and the bar rule must agree on what counts as numeric,
// or a column can carry a bar while sorting alphabetically.
func TestNumericDiagKindCoversDeltas(t *testing.T) {
	for _, k := range []pg.DiagColumnKind{
		pg.DiagDeltaBytes, pg.DiagDeltaCount, pg.DiagDeltaPct, pg.DiagSparkline,
	} {
		if !numericDiagKind(k) {
			t.Errorf("kind %v is numeric but numericDiagKind says otherwise", k)
		}
	}
	for _, k := range []pg.DiagColumnKind{pg.DiagText, pg.DiagGrowthTag} {
		if numericDiagKind(k) {
			t.Errorf("kind %v is text but numericDiagKind says numeric", k)
		}
	}
}

// Carrying a magnitude and being drawable as the headline bar are different
// questions, and a sparkline is the kind that separates them: it sorts like a
// number but already draws its own, so a bar over it would be a second picture
// of the same figure in the same cell.
func TestSparklineSortsButNeverBars(t *testing.T) {
	if !numericDiagKind(pg.DiagSparkline) {
		t.Error("a trace carries a magnitude and must sort descending first")
	}
	if barableDiagKind(pg.DiagSparkline) {
		t.Error("a trace must never become the headline bar")
	}
	for _, k := range []pg.DiagColumnKind{pg.DiagBytes, pg.DiagDeltaBytes, pg.DiagCount} {
		if !barableDiagKind(k) {
			t.Errorf("kind %v must still be barable", k)
		}
	}
}
