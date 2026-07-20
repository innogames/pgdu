package pg

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// DiagColumnKind classifies a result column so the renderer knows how to
// display and scale it.
type DiagColumnKind int

const (
	DiagText          DiagColumnKind = iota // text: left-aligned, no bar
	DiagInt                                 // integer count: right-aligned, bar if it is the headline col
	DiagFloat                               // floating-point number: right-aligned
	DiagPercent                             // 0–100 %: bar scaled 0–100, coloured by percentStyle when it is the bar col
	DiagBytes                               // byte count: rendered via humanize.Bytes when it is the bar col
	DiagPercentGraded                       // 0–100 % where higher is better: cell text graded green→red (e.g. cache hit ratio)
	DiagCostGraded                          // numeric, lower is better: 0 = green, nonzero graded green→red relative to the per-column window max
	DiagCmdType                             // statement command-type tag (QueryKind): green for read-only S, red for writing/locking ones
	DiagDuration                            // elapsed time in ms (Num): right-aligned, coloured by absolute magnitude band (ms→green, s→yellow, min→red)
	DiagBackendState                        // pg_stat_activity state: coloured per value (active→green, idle-in-xact→yellow, aborted→red, idle→muted)
	DiagPercentBad                          // 0–100 % where higher is worse: cell text graded green→red on an absolute scale (e.g. dead-tuple %, seq-scan %)
)

// DiagColumn describes one column of a diagnostic result set.
type DiagColumn struct {
	Name string
	Kind DiagColumnKind
}

// DiagCell is one cell in a diagnostic result row.
type DiagCell struct {
	Display string  // formatted text for the table cell
	Num     float64 // numeric value used for sorting and bar scaling; valid only when HasNum is true
	HasNum  bool
}

// DiagResult is the complete result of running one diagnostic query.
type DiagResult struct {
	Columns []DiagColumn
	Rows    [][]DiagCell
	BarCol  int // index of the headline column rendered as a bar, or -1
	SortCol int // index of the default (descending) sort column, or -1
}

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

	cols, resultRows, _, err := scanDiagRows(rows, 0)
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
		for i, c := range cols {
			if c.Name == d.Bar {
				barCol = i
				break
			}
		}
	}

	sortCol = -1
	if d.Sort != "" {
		for i, c := range cols {
			if c.Name == d.Sort {
				sortCol = i
				break
			}
		}
	} else {
		sortCol = barCol
	}
	return barCol, sortCol
}

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
		dcols, drows, _, serr := scanDiagRows(rs, 0)
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
			// where earlier ones had only text/NULL (mirrors scanDiagRows).
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

// scanDiagRows drains rows into generic column/cell form: column metadata comes
// from the server's field descriptions (kind inferred by name, promoted to
// numeric on the first numeric value seen), each value goes through
// formatDiagValue. When maxRows > 0 it stops after that many rows and reports
// truncated=true if more were waiting. The caller owns rows.Close().
func scanDiagRows(rows pgx.Rows, maxRows int) (cols []DiagColumn, out [][]DiagCell, truncated bool, err error) {
	fds := rows.FieldDescriptions()
	cols = make([]DiagColumn, len(fds))
	for i, fd := range fds {
		cols[i] = DiagColumn{
			Name: fd.Name,
			Kind: colKindFromName(fd.Name),
		}
	}

	for rows.Next() {
		if maxRows > 0 && len(out) >= maxRows {
			truncated = true
			break
		}
		vals, verr := rows.Values()
		if verr != nil {
			return nil, nil, false, fmt.Errorf("scan row: %w", verr)
		}
		cells := make([]DiagCell, len(vals))
		for i, v := range vals {
			k := DiagText
			if i < len(cols) {
				k = cols[i].Kind
			}
			cells[i] = formatDiagValue(v, k)
			// Promote column kind from Text to a numeric kind once we have an
			// actual numeric value, so the renderer can right-align and the
			// bar scaling works on numeric-typed columns that weren't caught
			// by the column-name heuristic.
			if cells[i].HasNum && cols[i].Kind == DiagText {
				cols[i].Kind = promotedNumericKind(v)
			}
		}
		out = append(out, cells)
	}
	// Only surface iteration errors when we drained the cursor; an early break
	// for maxRows leaves rows.Err() unset, which is the truncated case.
	if !truncated {
		if err := rows.Err(); err != nil {
			return nil, nil, false, err
		}
	}
	return cols, out, truncated, nil
}

// colKindFromName derives a column kind from naming conventions so the
// renderer knows how to draw bars and format numbers without per-query config.
func colKindFromName(name string) DiagColumnKind {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "pct") || strings.Contains(lower, "percent") ||
		strings.HasSuffix(lower, "ratio") || strings.HasSuffix(lower, "_pct"):
		return DiagPercent
	// Only a "bytes" suffix maps to DiagBytes — DiagBytes humanizes the numeric
	// value as *raw bytes*, so a column already scaled to MB (an "_mb" name)
	// would be mis-rendered by a factor of 1024². Keep size columns in bytes.
	case strings.HasSuffix(lower, "bytes"):
		return DiagBytes
	}
	return DiagText
}

// promotedNumericKind picks the kind for a column first seen to carry a numeric
// value in a cell that the column-name heuristic left as DiagText. A value that
// parsed out of pg_size_pretty text (e.g. "306 MB") is a byte quantity, so the
// column humanizes exactly like a raw "*_bytes" DiagBytes column — same units in
// the cells, the sum footer and any bar. Everything else is a plain integer.
// Without this, size columns pre-formatted by pg_size_pretty fell to DiagInt and
// their Σ footer printed a bare byte count next to humanized rows.
func promotedNumericKind(v any) DiagColumnKind {
	if s, ok := v.(string); ok {
		if _, isSize := parseSizePretty(s); isSize {
			return DiagBytes
		}
	}
	return DiagInt
}

// formatDiagValue converts a single value returned by pgx rows.Values() into a
// DiagCell. The type switch covers the standard pgx/v5 decoded types; the
// default branch uses fmt.Sprintf so an unrecognised type never panics — the
// cell just shows a raw representation.
func formatDiagValue(v any, hint DiagColumnKind) DiagCell {
	if v == nil {
		return DiagCell{Display: "—"}
	}
	switch t := v.(type) {
	case bool:
		if t {
			return DiagCell{Display: "t"}
		}
		return DiagCell{Display: "f"}

	case int16:
		return DiagCell{Display: strconv.FormatInt(int64(t), 10), Num: float64(t), HasNum: true}

	case int32:
		return DiagCell{Display: strconv.FormatInt(int64(t), 10), Num: float64(t), HasNum: true}

	case int64:
		return DiagCell{Display: strconv.FormatInt(t, 10), Num: float64(t), HasNum: true}

	case float32:
		return DiagCell{Display: diagFormatFloat(float64(t)), Num: float64(t), HasNum: true}

	case float64:
		return DiagCell{Display: diagFormatFloat(t), Num: t, HasNum: true}

	case string:
		// Several diagnostic queries pre-format sizes with pg_size_pretty for
		// display. Parse the magnitude back out so the column sorts by bytes
		// instead of by the leading digits of the string ("97 MB" vs "9832 kB").
		if n, ok := parseSizePretty(t); ok {
			return DiagCell{Display: t, Num: n, HasNum: true}
		}
		return DiagCell{Display: t}

	case []byte:
		return DiagCell{Display: string(t)}

	case time.Time:
		if t.IsZero() {
			return DiagCell{Display: "—"}
		}
		return DiagCell{Display: t.Local().Format("2006-01-02 15:04:05")}

	case time.Duration:
		return DiagCell{Display: t.Round(time.Second).String()}

	case pgtype.Numeric:
		if !t.Valid {
			return DiagCell{Display: "—"}
		}
		if t.NaN {
			return DiagCell{Display: "NaN"}
		}
		if t.Int == nil {
			return DiagCell{Display: "0", Num: 0, HasNum: true}
		}
		// value = Int × 10^Exp
		rat := new(big.Rat).SetInt(t.Int)
		if t.Exp != 0 {
			absExp := t.Exp
			if absExp < 0 {
				absExp = -absExp
			}
			pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(absExp)), nil)
			if t.Exp > 0 {
				rat.Mul(rat, new(big.Rat).SetInt(pow))
			} else {
				rat.Quo(rat, new(big.Rat).SetInt(pow))
			}
		}
		f, _ := rat.Float64()
		return DiagCell{Display: diagFormatFloat(f), Num: f, HasNum: true}

	case pgtype.Interval:
		if !t.Valid {
			return DiagCell{Display: "—"}
		}
		return DiagCell{Display: formatDiagInterval(t)}

	case uint32:
		// OID-typed values (pure oid type, not regclass which arrives as string).
		return DiagCell{Display: strconv.FormatUint(uint64(t), 10), Num: float64(t), HasNum: true}

	case []string:
		return DiagCell{Display: strings.Join(t, ", ")}

	case []int64:
		ss := make([]string, len(t))
		for i, n := range t {
			ss[i] = strconv.FormatInt(n, 10)
		}
		return DiagCell{Display: strings.Join(ss, ", ")}

	default:
		return DiagCell{Display: fmt.Sprintf("%v", v)}
	}
}

// sizePrettyUnits maps the unit suffixes emitted by PostgreSQL's
// pg_size_pretty() to their byte multipliers. pg_size_pretty is 1024-based, so
// these mirror the server's own thresholds.
var sizePrettyUnits = map[string]float64{
	"bytes": 1,
	"kB":    1 << 10,
	"MB":    1 << 20,
	"GB":    1 << 30,
	"TB":    1 << 40,
	"PB":    1 << 50,
}

// parseSizePretty parses a string in the exact "<number> <unit>" form produced
// by pg_size_pretty() (e.g. "9832 kB", "97 MB", "0 bytes") into a byte count.
// The match is deliberately strict — number, single space, known unit — so a
// genuine text column is never mistaken for a size and given a bogus sort key.
func parseSizePretty(s string) (float64, bool) {
	num, unit, ok := strings.Cut(s, " ")
	if !ok {
		return 0, false
	}
	mult, ok := sizePrettyUnits[unit]
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, false
	}
	return f * mult, true
}

// diagFormatFloat renders f with up to 2 decimal places, stripping trailing
// zeros so "12.00" becomes "12" and "3.10" becomes "3.1".
func diagFormatFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', 2, 64)
	if strings.ContainsRune(s, '.') {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}

// formatDiagInterval renders a pgtype.Interval as a human-readable string
// similar to PostgreSQL's interval output but condensed for table cells.
func formatDiagInterval(iv pgtype.Interval) string {
	var parts []string
	if iv.Months != 0 {
		if iv.Months%12 == 0 {
			parts = append(parts, fmt.Sprintf("%dy", iv.Months/12))
		} else {
			parts = append(parts, fmt.Sprintf("%dmo", iv.Months))
		}
	}
	if iv.Days != 0 {
		parts = append(parts, fmt.Sprintf("%dd", iv.Days))
	}
	if iv.Microseconds != 0 || (iv.Months == 0 && iv.Days == 0) {
		d := time.Duration(iv.Microseconds) * time.Microsecond
		s := d.Round(time.Second).String()
		// Don't append "0s" when months/days already fill the display.
		if s != "0s" || (iv.Months == 0 && iv.Days == 0) {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return "0s"
	}
	return strings.Join(parts, " ")
}
