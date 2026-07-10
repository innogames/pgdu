package tui

import (
	"strconv"
	"strings"

	"pgdu/internal/pg"
)

// diagPrefsKey namespaces a diagnostic's column-visibility selection in the
// prefs file so each diagnostic remembers its own picker independently.
func diagPrefsKey(key string) string {
	return "diag/" + key
}

// diagColOn reports whether a diagnostic column is visible under vis. Missing
// entries default to visible, so columns a query grows in a later build (and
// the leading "database" column of an all-DBs run) appear without migration.
func diagColOn(vis map[string]bool, name string) bool {
	if vis == nil {
		return true
	}
	v, ok := vis[name]
	return !ok || v
}

// diagVis returns the column-visibility map for a diagnostic key, lazily
// seeding it from the persisted prefs (or the diagnostic's DefaultHidden set
// when there are none) on first use. nil means "all visible".
func (m *Model) diagVis(key string) map[string]bool {
	if vis, ok := m.diagColsVisible[key]; ok {
		return vis
	}
	var vis map[string]bool
	if m.colPrefs != nil {
		if v := m.colPrefs.Columns(diagPrefsKey(key)); len(v) > 0 {
			vis = v
		}
	}
	if vis == nil {
		vis = defaultDiagVis(key)
	}
	if m.diagColsVisible == nil {
		m.diagColsVisible = map[string]map[string]bool{}
	}
	m.diagColsVisible[key] = vis
	return vis
}

// defaultDiagVis builds the seed visibility map from a diagnostic's
// DefaultHidden list: named columns start hidden, everything else stays visible
// (absent → visible, per diagColOn). Returns nil — read as "all visible" — when
// the diagnostic hides nothing.
func defaultDiagVis(key string) map[string]bool {
	d, ok := pg.DiagnosticByKey(key)
	if !ok || len(d.DefaultHidden) == 0 {
		return nil
	}
	vis := make(map[string]bool, len(d.DefaultHidden))
	for _, name := range d.DefaultHidden {
		vis[name] = false
	}
	return vis
}

// rebuildDiagItems re-projects the retained full result (s.diagResult) to the
// currently visible column subset: columns, bar/sort indices, items and the Σ
// footer all derive from the same projection so they stay parallel by
// construction. Call it after a fresh load or any column toggle.
func (m *Model) rebuildDiagItems(s *screen) {
	res := s.diagResult
	if res == nil || s.diag == nil {
		return
	}
	vis := m.diagVis(s.diag.Key)
	idxs := make([]int, 0, len(res.Columns))
	for i := range res.Columns {
		if diagColOn(vis, res.Columns[i].Name) {
			idxs = append(idxs, i)
		}
	}
	if len(idxs) == 0 {
		// Never project every column away — the toggle handler prevents this,
		// but a stale prefs file could; fall back to the first column.
		idxs = append(idxs, 0)
	}

	cols := make([]pg.DiagColumn, len(idxs))
	for j, i := range idxs {
		cols[j] = res.Columns[i]
	}
	s.diagCols = cols

	s.diagBarCol = -1
	for j, i := range idxs {
		if i == res.BarCol {
			s.diagBarCol = j
		}
	}

	// item.name is the space-joined cell display so the fuzzy filter can match
	// any (visible) column value.
	s.items = s.items[:0]
	for _, row := range res.Rows {
		cells := make([]pg.DiagCell, len(idxs))
		parts := make([]string, len(idxs))
		for j, i := range idxs {
			if i < len(row) {
				cells[j] = row[i]
			}
			parts[j] = cells[j].Display
		}
		s.items = append(s.items, item{name: strings.Join(parts, " "), data: cells})
	}
	s.diagTotalRow = diagFooterCells(cols, s.items)

	// Resolve the sort column: the remembered name if still visible, else the
	// diagnostic's default sort (descending), else column 0 ascending.
	sortIdx := -1
	if s.diagSortName != "" {
		for j, c := range cols {
			if c.Name == s.diagSortName {
				sortIdx = j
				break
			}
		}
	}
	if sortIdx < 0 {
		if res.SortCol >= 0 && res.SortCol < len(res.Columns) {
			want := res.Columns[res.SortCol].Name
			for j, c := range cols {
				if c.Name == want {
					sortIdx = j
					break
				}
			}
		}
		if sortIdx >= 0 {
			s.sortDesc = true
		} else {
			sortIdx = 0
			s.sortDesc = false
		}
		s.diagSortName = cols[sortIdx].Name
	}
	s.diagSortCol = sortIdx

	s.diagMetricsDirty = true
	m.applySort(s)
}

// diagFooterCells builds the pinned Σ footer for a generic diagnostic result.
// Only additive columns sum meaningfully: counts (DiagInt) and sizes
// (DiagBytes). Percents, grades, floats and text stay blank, as do
// identifier-shaped numeric columns (pid/oid) whose sum is nonsense. The
// row-count label lands in the first text column. Returns nil when no column
// summed — a footer of blanks would just eat a row.
func diagFooterCells(cols []pg.DiagColumn, items []item) []pg.DiagCell {
	if len(items) == 0 {
		return nil
	}
	total := make([]pg.DiagCell, len(cols))
	summed := false
	for j, c := range cols {
		if c.Kind != pg.DiagInt && c.Kind != pg.DiagBytes {
			continue
		}
		lower := strings.ToLower(c.Name)
		if lower == "pid" || strings.HasSuffix(lower, "_pid") || strings.HasSuffix(lower, "oid") {
			continue
		}
		var sum float64
		n := 0
		for _, it := range items {
			row, ok := it.data.([]pg.DiagCell)
			if !ok || j >= len(row) || !row[j].HasNum {
				continue
			}
			sum += row[j].Num
			n++
		}
		if n == 0 {
			continue
		}
		display := strconv.FormatInt(int64(sum), 10)
		if sum != float64(int64(sum)) {
			display = diagFormatFloatTUI(sum)
		}
		total[j] = pg.DiagCell{Display: display, Num: sum, HasNum: true}
		summed = true
	}
	if !summed {
		return nil
	}
	for j, c := range cols {
		if c.Kind == pg.DiagText && !total[j].HasNum {
			total[j] = pg.DiagCell{Display: "Σ " + strconv.Itoa(len(items)) + " rows"}
			break
		}
	}
	return total
}

// diagFormatFloatTUI renders a summed float with up to 2 decimals, trailing
// zeros stripped (mirrors pg's diagFormatFloat, which isn't exported).
func diagFormatFloatTUI(f float64) string {
	s := strconv.FormatFloat(f, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}
