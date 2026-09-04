package tui

import (
	"slices"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// colDesc describes one column of a registry-backed table (top queries,
// activity, table overview, log timeline): its stable id (the C-picker key and
// the sort-column memory across rebuilds — it must never change even if the
// header label or position does), header label, render kind, whether it's shown
// by default, whether it can be hidden at all, a one-line description for the
// picker, an optional availability gate over the per-build context, and the
// cell builder. Adding a metric column is one entry in the table's registry.
type colDesc[ID ~string, Row, Ctx any] struct {
	id        ID
	name      string
	kind      pg.DiagColumnKind
	defaultOn bool
	mandatory bool
	desc      string
	available func(Ctx) bool // nil = always available
	cell      func(Row, Ctx) pg.DiagCell
}

// colSpec is the static half of a registry-backed table: where its columns come
// from, where the user's selection persists, which column the sort falls back
// to when the remembered one is hidden, and how the picker introduces itself.
// The state half is colTable; keeping them apart lets a zero-value Model (as
// the tests build) still find its registries.
type colSpec[ID ~string, Row, Ctx any] struct {
	registry    func() []colDesc[ID, Row, Ctx]
	prefsKey    string
	defaultSort ID     // falls back to this (descending) when the sort column is hidden
	title       string // picker subtitle
	unavailNote string // shown next to columns whose available() gate is off
}

// colTable is a table's picker state on the Model: the per-column visibility
// set (nil = registry defaults, lazily materialised), the active sort column by
// stable id (the projected index screen.diagSortCol is recomputed each
// rebuild), and the modal C-picker overlay flag + cursor.
type colTable[ID ~string] struct {
	visible   map[ID]bool
	sortColID ID
	showCfg   bool
	cfgCursor int
}

// enabled reports whether column id should be shown. With no explicit entry in
// the visibility set it falls back to def (the registry default), so a fresh
// Model with a nil set renders exactly the default columns.
func (t *colTable[ID]) enabled(id ID, def bool) bool {
	if v, ok := t.visible[id]; ok {
		return v
	}
	return def
}

// ensureInit lazily materialises the visibility set from the registry defaults,
// so the picker shows concrete checkbox state and toggling one column doesn't
// implicitly pin the defaults of every other.
func (sp colSpec[ID, R, C]) ensureInit(t *colTable[ID]) {
	if t.visible != nil {
		return
	}
	t.visible = make(map[ID]bool)
	for _, d := range sp.registry() {
		t.visible[d.id] = d.defaultOn || d.mandatory
	}
}

// visibleCols projects the registry to the columns that are both available for
// ctx and enabled by the user, in registry order. Mandatory columns are always
// kept regardless of the visibility set.
func (sp colSpec[ID, R, C]) visibleCols(t *colTable[ID], ctx C) []colDesc[ID, R, C] {
	var out []colDesc[ID, R, C]
	for _, d := range sp.registry() {
		if d.available != nil && !d.available(ctx) {
			continue
		}
		if d.mandatory || t.enabled(d.id, d.defaultOn) {
			out = append(out, d)
		}
	}
	return out
}

// syncSort maps the remembered sort column onto the projected descs. When it
// was hidden the sort falls back to sp.defaultSort descending, then to the first
// column, and the resolved id is written back so the next rebuild is stable.
func (sp colSpec[ID, R, C]) syncSort(t *colTable[ID], s *screen, descs []colDesc[ID, R, C]) {
	if i := indexOfCol(descs, t.sortColID); i >= 0 {
		s.diagSortCol = i
		return
	}
	if i := indexOfCol(descs, sp.defaultSort); i >= 0 {
		s.diagSortCol = i
		s.sortDesc = true
		t.sortColID = sp.defaultSort
		return
	}
	s.diagSortCol = 0
	if len(descs) > 0 {
		t.sortColID = descs[0].id
	}
}

// open shows the picker overlay with the cursor on the first row.
func (sp colSpec[ID, R, C]) open(m *Model, t *colTable[ID]) {
	sp.ensureInit(t)
	m.showInfo = false
	t.showCfg = true
	t.cfgCursor = 0
}

// handleKey drives the modal picker over sp's registry: Up/Down/Top/Bottom move
// the cursor, space/Enter toggle the highlighted column, r resets to the
// registry defaults, C/esc close. Mandatory columns and columns whose
// availability gate is off for ctx can't be toggled. rebuild re-projects the
// screen after a change; the selection persists to prefs under sp.prefsKey.
func (sp colSpec[ID, R, C]) handleKey(m *Model, t *colTable[ID], msg tea.KeyMsg, ctx C, rebuild func()) tea.Cmd {
	reg := sp.registry()
	save := func() { m.saveColPrefs(sp.prefsKey, colVisToStrings(t.visible)) }
	return m.handleColCfgKey(msg, colCfgSpec{
		n:      len(reg),
		cursor: &t.cfgCursor,
		close:  func() { t.showCfg = false },
		reset: func() {
			t.visible = nil
			sp.ensureInit(t)
			rebuild()
			save()
		},
		toggle: func(i int) {
			d := reg[i]
			if d.mandatory || (d.available != nil && !d.available(ctx)) {
				return
			}
			sp.ensureInit(t)
			t.visible[d.id] = !t.enabled(d.id, d.defaultOn)
			rebuild()
			save()
		},
	})
}

// renderConfig draws the htop-style picker: one checkbox row per registry
// column with the cursor highlighted; mandatory and unavailable columns are
// shown but marked as not toggleable.
func (sp colSpec[ID, R, C]) renderConfig(m *Model, t *colTable[ID], ctx C, height int) string {
	sp.ensureInit(t)
	reg := sp.registry()
	rows := make([]colCfgRow, len(reg))
	for i, d := range reg {
		rows[i] = colCfgRow{
			name:        d.name,
			desc:        d.desc,
			on:          d.mandatory || t.enabled(d.id, d.defaultOn),
			mandatory:   d.mandatory,
			unavailable: d.available != nil && !d.available(ctx),
			note:        sp.unavailNote,
		}
	}
	return m.renderColCfgOverlay(sp.title, rows, t.cfgCursor, height)
}

// indexOfCol returns the position of id within descs, or -1 when absent.
func indexOfCol[ID ~string, R, C any](descs []colDesc[ID, R, C], id ID) int {
	return slices.IndexFunc(descs, func(d colDesc[ID, R, C]) bool { return d.id == id })
}

// diagColumnsFrom maps projected descriptors to the renderer's column schema.
func diagColumnsFrom[ID ~string, R, C any](descs []colDesc[ID, R, C]) []pg.DiagColumn {
	cols := make([]pg.DiagColumn, len(descs))
	for i, d := range descs {
		cols[i] = pg.DiagColumn{Name: d.name, Kind: d.kind}
	}
	return cols
}

// cellsFor builds one row's cells over the already-projected descriptors, so the
// cells stay parallel to diagColumnsFrom(descs) by construction — there is no
// index arithmetic to keep in sync.
func cellsFor[ID ~string, R, C any](descs []colDesc[ID, R, C], row R, ctx C) []pg.DiagCell {
	cells := make([]pg.DiagCell, len(descs))
	for i, d := range descs {
		cells[i] = d.cell(row, ctx)
	}
	return cells
}

// colCfgSpec describes one column-config overlay to handleColCfgKey: the row
// count, the cursor to move, and the close/reset/toggle actions. The behavioral
// differences between the pickers (availability gates, last-visible-column
// guard, conditional rebuilds) live inside each spec's closures.
type colCfgSpec struct {
	n      int
	cursor *int
	close  func()
	reset  func()
	toggle func(i int)
}

// handleColCfgKey drives a modal column-config overlay: Up/Down/Top/Bottom move
// the cursor over the column set, space/Enter toggle the highlighted column's
// visibility, r resets to defaults, and C/esc close it. Quit still quits.
func (m *Model) handleColCfgKey(msg tea.KeyMsg, sp colCfgSpec) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit
	case key.Matches(msg, m.keys.Columns), msg.Type == tea.KeyEsc:
		sp.close()
	case key.Matches(msg, m.keys.Up):
		if *sp.cursor > 0 {
			*sp.cursor--
		}
	case key.Matches(msg, m.keys.Down):
		if *sp.cursor < sp.n-1 {
			*sp.cursor++
		}
	case key.Matches(msg, m.keys.Top):
		*sp.cursor = 0
	case key.Matches(msg, m.keys.Bottom):
		*sp.cursor = sp.n - 1
	case key.Matches(msg, m.keys.ResetCols):
		sp.reset()
	case key.Matches(msg, m.keys.Refresh), key.Matches(msg, m.keys.Enter):
		// Refresh is space — the natural htop toggle; Enter also toggles.
		if i := *sp.cursor; i >= 0 && i < sp.n {
			sp.toggle(i)
		}
	}
	return nil
}
