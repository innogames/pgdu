package pg

import (
	"context"
	"fmt"

	"pgdu/internal/diagres"
)

// The generic result shape lives in diagres so the pgbouncer console (which pg
// itself depends on for triage) can produce it without importing pg. The
// aliases keep pg's public surface unchanged.
type (
	DiagColumnKind = diagres.Kind
	DiagColumn     = diagres.Column
	DiagCell       = diagres.Cell
	DiagResult     = diagres.Result
)

const (
	DiagText          = diagres.KindText
	DiagInt           = diagres.KindInt
	DiagFloat         = diagres.KindFloat
	DiagPercent       = diagres.KindPercent
	DiagBytes         = diagres.KindBytes
	DiagPercentGraded = diagres.KindPercentGraded
	DiagCostGraded    = diagres.KindCostGraded
	DiagCmdType       = diagres.KindCmdType
	DiagDuration      = diagres.KindDuration
	DiagBackendState  = diagres.KindBackendState
	DiagPercentBad    = diagres.KindPercentBad
	DiagCount         = diagres.KindCount
	DiagLogSeverity   = diagres.KindLogSeverity
)

// RunDiagnostic executes d.SQL against db (or the default database when db is
// empty) and returns the result in a generic column/row form suitable for the
// TUI renderer. The 30-second query timeout is enforced by the caller (the
// query() tea.Cmd wrapper in cmds.go).
func (c *Client) RunDiagnostic(ctx context.Context, db string, d Diagnostic) (*DiagResult, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, d.SQL)
	if err != nil {
		return nil, fmt.Errorf("run diagnostic %q: %w", d.Key, err)
	}
	defer rows.Close()

	cols, resultRows, _, err := diagres.Scan(rows, 0)
	if err != nil {
		return nil, fmt.Errorf("run diagnostic %q: %w", d.Key, err)
	}

	applyKindOverrides(cols, d.Kinds)
	barCol, sortCol := resolveBarSort(cols, d)
	return &DiagResult{Columns: cols, Rows: resultRows, BarCol: barCol, SortCol: sortCol}, nil
}

// resolveBarSort maps the Diagnostic's Bar/Sort column names onto indices in
// cols. Bar is -1 when unset or not found; Sort falls back to the bar column
// when unset. Resolving by name (rather than a fixed index) means callers that
// prepend columns — e.g. RunDiagnosticAllDBs' leading "database" column — get
// the right index for free.
func resolveBarSort(cols []DiagColumn, d Diagnostic) (barCol, sortCol int) {
	barCol = -1
	if d.Bar != "" {
		barCol = colIndex(cols, d.Bar)
	}

	if d.Sort != "" {
		sortCol = colIndex(cols, d.Sort)
	} else {
		sortCol = barCol
	}
	return barCol, sortCol
}

func colIndex(cols []DiagColumn, name string) int { return diagres.ColIndex(cols, name) }

// RunDiagnosticAllDBs runs a per-database diagnostic against every database the
// current user can connect to and merges the results into one table with a
// leading "database" column identifying each row's origin. It backs the "all
// databases" choice offered when a per-database diagnostic is selected. A
// database that fails to connect or query is skipped, its error remembered and
// surfaced only if no database yields a result. The whole sweep runs under the
// caller's single 30-second query() budget.
func (c *Client) RunDiagnosticAllDBs(ctx context.Context, d Diagnostic) (*DiagResult, error) {
	dbs, err := c.ListDatabases(ctx)
	if err != nil {
		return nil, err
	}

	const dbColName = "database"
	var (
		cols     []DiagColumn // merged columns, led by the "database" column
		rows     [][]DiagCell
		firstErr error
		ran      int
	)
	for _, db := range dbs {
		pool, perr := c.PoolFor(ctx, db.Name)
		if perr != nil {
			if firstErr == nil {
				firstErr = perr
			}
			continue
		}
		rs, qerr := pool.Query(ctx, d.SQL)
		if qerr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("run diagnostic %q in %q: %w", d.Key, db.Name, qerr)
			}
			continue
		}
		dcols, drows, _, serr := diagres.Scan(rs, 0)
		rs.Close()
		if serr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("run diagnostic %q in %q: %w", d.Key, db.Name, serr)
			}
			continue
		}
		ran++

		if cols == nil {
			// Establish the merged column set from the first database that
			// answered: the leading text column, then this query's columns.
			cols = make([]DiagColumn, 0, len(dcols)+1)
			cols = append(cols, DiagColumn{Name: dbColName, Kind: DiagText})
			cols = append(cols, dcols...)
		} else {
			// Identical SQL everywhere, so columns line up by position. Promote
			// a merged column's kind when a later database sees a numeric value
			// where earlier ones had only text/NULL (mirrors diagres.Scan).
			for i, dc := range dcols {
				mc := i + 1 // offset past the leading "database" column
				if mc < len(cols) && cols[mc].Kind == DiagText && dc.Kind != DiagText {
					cols[mc].Kind = dc.Kind
				}
			}
		}

		for _, row := range drows {
			merged := make([]DiagCell, 0, len(row)+1)
			merged = append(merged, DiagCell{Display: db.Name})
			merged = append(merged, row...)
			rows = append(rows, merged)
		}
	}

	if ran == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		return &DiagResult{Columns: []DiagColumn{{Name: dbColName, Kind: DiagText}}, BarCol: -1, SortCol: -1}, nil
	}

	applyKindOverrides(cols, d.Kinds)
	barCol, sortCol := resolveBarSort(cols, d)
	return &DiagResult{Columns: cols, Rows: rows, BarCol: barCol, SortCol: sortCol}, nil
}

// applyKindOverrides replaces inferred column kinds with the diagnostic's
// declared ones (Diagnostic.Kinds). Applied after scanning: Kind only drives
// rendering, so a post-scan overwrite is safe and also wins over the
// text→numeric promotion done while scanning.
func applyKindOverrides(cols []DiagColumn, kinds map[string]DiagColumnKind) {
	if len(kinds) == 0 {
		return
	}
	for i, c := range cols {
		if k, ok := kinds[c.Name]; ok {
			cols[i].Kind = k
		}
	}
}
