package pg

import "slices"

// Diagnostic describes one entry in the diagnostics tool list: a name, a
// category (used as a label in the list view), a short description, the SQL
// to run, and the name of the headline column to render as a bar chart (or
// "" for all-text queries where no bar is meaningful).
type Diagnostic struct {
	Key         string // stable identifier (matches the psql-helper filename stem)
	Title       string // short display name shown in the list
	Category    string // "index" | "table" | "vacuum" | "activity" | "wal" | "server"
	Description string // one-line explanation shown as detail in the list
	SQL         string // the query to run (no parameters)
	Bar         string // headline column name rendered as a bar, or ""
	Sort        string // default sort column name (descending); "" falls back to Bar, then column 0 ascending
	PerDB       bool   // true = query reads only the connected database; the TUI prompts for which database to run against (or all)

	// Kinds overrides the name-heuristic column kind (colKindFromName) per
	// column, so a diagnostic can opt into graded rendering the suffix rules
	// can't infer — e.g. hit ratios as DiagPercentGraded (higher is better) or
	// dead-tuple % as DiagPercentBad (higher is worse). Keys are column names.
	Kinds map[string]DiagColumnKind

	// DefaultHidden lists columns hidden on first view, one keystroke from
	// being shown via the C column picker. Unlike dropping them from the SQL,
	// the data is still fetched — this just declutters the default table for a
	// wide result. Empty = every column shown.
	DefaultHidden []string

	// Fix builds a copy-pasteable remediation statement for one result row —
	// shown by the TUI on Enter and run (Client.RunFix) only after an explicit
	// y confirm. get returns a column's
	// Display value by (case-insensitive) name from the full, unprojected row;
	// ok=false when the row can't produce a fix. Builders live in
	// diagnostic_fixes.go and must stay lock-safe (CONCURRENTLY, ANALYZE,
	// plain VACUUM) — heavier remedies only as SQL comments.
	Fix func(get func(col string) (string, bool)) (sql string, ok bool)

	// Help is the long-form explanation shown in the ? reference overlay:
	// what the diagnostic is for and how to interpret its result (which
	// columns matter, what good/bad looks like, what action a bad row
	// suggests). Free prose — whitespace is collapsed and the text re-wrapped
	// to the terminal width at render time.
	Help string
}

// DiagnosticByKey looks a diagnostic up in the registry by its stable key.
func DiagnosticByKey(key string) (Diagnostic, bool) {
	for _, d := range Diagnostics {
		if d.Key == key {
			return d, true
		}
	}
	return Diagnostic{}, false
}

// Diagnostics is the ordered registry of all built-in diagnostic queries.
// Queries are grouped by category (one diag_defs_<category>.go each) and sorted
// alphabetically within each group; the TUI list renders them in this order.
var Diagnostics = slices.Concat(diagIndex, diagTable, diagVacuum, diagActivity, diagWal, diagServer)
