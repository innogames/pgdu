package pg

import (
	"strings"
	"testing"
)

// Bar, Sort, Kinds and DefaultHidden all address result columns by name, and
// those names are the SQL's own aliases — a rename on one side silently
// disables the bar, the sort or a kind override. TestIntegration_AllDiagnostics
// catches that against a live server; this is the same guard without one, so a
// typo fails on a laptop with no PGDU_TEST_DSN.
func TestDiagnosticColumnNamesAppearInSQL(t *testing.T) {
	for _, d := range Diagnostics {
		t.Run(d.Key, func(t *testing.T) {
			named := map[string][]string{
				"Bar":  {d.Bar},
				"Sort": {d.Sort},
			}
			for name := range d.Kinds {
				named["Kinds"] = append(named["Kinds"], name)
			}
			named["DefaultHidden"] = d.DefaultHidden

			for field, cols := range named {
				for _, col := range cols {
					// The all-databases sweep prepends this column in Go; it is
					// deliberately absent from every diagnostic's own SQL.
					if col == "" || col == "database" {
						continue
					}
					if !strings.Contains(d.SQL, col) {
						t.Errorf("%s names column %q, which its SQL never produces", field, col)
					}
				}
			}
		})
	}
}

// Note is rendered in the single legend line under a result table, so it has to
// stay one short line; anything longer belongs in Help.
func TestDiagnosticNotesAreOneLine(t *testing.T) {
	const maxNote = 160
	for _, d := range Diagnostics {
		if d.Note == "" {
			continue
		}
		if strings.ContainsAny(d.Note, "\n\t") {
			t.Errorf("%s: Note must be a single unindented line: %q", d.Key, d.Note)
		}
		if n := len([]rune(d.Note)); n > maxNote {
			t.Errorf("%s: Note is %d chars, over the %d-column line budget", d.Key, n, maxNote)
		}
	}
}
