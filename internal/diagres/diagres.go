// Package diagres is the generic column/row result shape shared by every
// tabular SQL surface in pgdu: diagnostic queries, EXPLAIN output and the
// pgbouncer console. It knows how to drain pgx rows into display-ready cells
// and how to classify columns so the renderer can align, scale and colour
// them — nothing about where the rows came from.
package diagres

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"pgdu/internal/humanize"
)

// Kind classifies a result column so the renderer knows how to
// display and scale it.
type Kind int

const (
	KindText          Kind = iota // text: left-aligned, no bar
	KindInt                       // integer count: right-aligned, bar if it is the headline col
	KindFloat                     // floating-point number: right-aligned
	KindPercent                   // 0–100 %: bar scaled 0–100, coloured by percentStyle when it is the bar col
	KindBytes                     // byte count: rendered via humanize.Bytes when it is the bar col
	KindPercentGraded             // 0–100 % where higher is better: cell text graded green→red (e.g. cache hit ratio)
	KindCostGraded                // numeric, lower is better: 0 = green, nonzero graded green→red relative to the per-column window max
	KindCmdType                   // statement command-type tag (QueryKind): green for read-only S, red for writing/locking ones
	KindDuration                  // elapsed time in ms (Num): right-aligned, coloured by absolute magnitude band (ms→green, s→yellow, min→red)
	KindBackendState              // pg_stat_activity state: coloured per value (active→green, idle-in-xact→yellow, aborted→red, idle→muted)
	KindPercentBad                // 0–100 % where higher is worse: cell text graded green→red on an absolute scale (e.g. dead-tuple %, seq-scan %)
	KindCount                     // large cumulative counter: rendered humanized (1.2k/3.4M/5.1G) from Num; sorts and sums on the raw value
	KindLogSeverity               // server-log severity tag: coloured per value (ERROR/FATAL red, WARNING yellow, LOG muted); Num carries the ordinal for sorting
)

// Column describes one column of a result set.
type Column struct {
	Name string
	Kind Kind
}

// Cell is one cell in a result row.
type Cell struct {
	Display string  // formatted text for the table cell
	Num     float64 // numeric value used for sorting and bar scaling; valid only when HasNum is true
	HasNum  bool
}

// Result is one complete tabular result.
type Result struct {
	Columns []Column
	Rows    [][]Cell
	BarCol  int // index of the headline column rendered as a bar, or -1
	SortCol int // index of the default (descending) sort column, or -1
}

// Scan drains rows into generic column/cell form: column metadata comes
// from the server's field descriptions (kind inferred by name, promoted to
// numeric on the first numeric value seen), each value goes through
// FormatValue. When maxRows > 0 it stops after that many rows and reports
// truncated=true if more were waiting. The caller owns rows.Close().
func Scan(rows pgx.Rows, maxRows int) (cols []Column, out [][]Cell, truncated bool, err error) {
	fds := rows.FieldDescriptions()
	cols = make([]Column, len(fds))
	for i, fd := range fds {
		cols[i] = Column{
			Name: fd.Name,
			Kind: KindFromName(fd.Name),
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
		cells := make([]Cell, len(vals))
		for i, v := range vals {
			k := KindText
			if i < len(cols) {
				k = cols[i].Kind
			}
			cells[i] = FormatValue(v, k)
			// Promote column kind from Text to a numeric kind once we have an
			// actual numeric value, so the renderer can right-align and the
			// bar scaling works on numeric-typed columns that weren't caught
			// by the column-name heuristic.
			if cells[i].HasNum && cols[i].Kind == KindText {
				cols[i].Kind = PromotedNumericKind(v)
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

// KindFromName derives a column kind from naming conventions so the
// renderer knows how to draw bars and format numbers without per-query config.
func KindFromName(name string) Kind {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "pct") || strings.Contains(lower, "percent") ||
		strings.HasSuffix(lower, "ratio") || strings.HasSuffix(lower, "_pct"):
		return KindPercent
	// Only a "bytes" suffix maps to KindBytes — KindBytes humanizes the numeric
	// value as *raw bytes*, so a column already scaled to MB (an "_mb" name)
	// would be mis-rendered by a factor of 1024². Keep size columns in bytes.
	case strings.HasSuffix(lower, "bytes"):
		return KindBytes
	}
	return KindText
}

// PromotedNumericKind picks the kind for a column first seen to carry a numeric
// value in a cell that the column-name heuristic left as KindText. A value that
// parsed out of pg_size_pretty text (e.g. "306 MB") is a byte quantity, so the
// column humanizes exactly like a raw "*_bytes" KindBytes column — same units in
// the cells, the sum footer and any bar. Everything else is a plain integer.
// Without this, size columns pre-formatted by pg_size_pretty fell to KindInt and
// their Σ footer printed a bare byte count next to humanized rows.
func PromotedNumericKind(v any) Kind {
	if s, ok := v.(string); ok {
		if _, isSize := humanize.ParseSizePretty(s); isSize {
			return KindBytes
		}
	}
	return KindInt
}

// FormatValue converts a single value returned by pgx rows.Values() into a
// Cell. The type switch covers the standard pgx/v5 decoded types; the
// default branch uses fmt.Sprintf so an unrecognised type never panics — the
// cell just shows a raw representation.
func FormatValue(v any, hint Kind) Cell {
	if v == nil {
		return Cell{Display: "—"}
	}
	switch t := v.(type) {
	case bool:
		if t {
			return Cell{Display: "t"}
		}
		return Cell{Display: "f"}

	case int16:
		return Cell{Display: strconv.FormatInt(int64(t), 10), Num: float64(t), HasNum: true}

	case int32:
		return Cell{Display: strconv.FormatInt(int64(t), 10), Num: float64(t), HasNum: true}

	case int64:
		return Cell{Display: strconv.FormatInt(t, 10), Num: float64(t), HasNum: true}

	case float32:
		return Cell{Display: formatFloat(float64(t)), Num: float64(t), HasNum: true}

	case float64:
		return Cell{Display: formatFloat(t), Num: t, HasNum: true}

	case string:
		// Several diagnostic queries pre-format sizes with pg_size_pretty for
		// display. Parse the magnitude back out so the column sorts by bytes
		// instead of by the leading digits of the string ("97 MB" vs "9832 kB").
		if n, ok := humanize.ParseSizePretty(t); ok {
			return Cell{Display: t, Num: n, HasNum: true}
		}
		return Cell{Display: t}

	case []byte:
		return Cell{Display: string(t)}

	case time.Time:
		if t.IsZero() {
			return Cell{Display: "—"}
		}
		return Cell{Display: t.Local().Format("2006-01-02 15:04:05")}

	case time.Duration:
		return Cell{Display: t.Round(time.Second).String()}

	case pgtype.Numeric:
		if !t.Valid {
			return Cell{Display: "—"}
		}
		if t.NaN {
			return Cell{Display: "NaN"}
		}
		if t.Int == nil {
			return Cell{Display: "0", Num: 0, HasNum: true}
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
		return Cell{Display: formatFloat(f), Num: f, HasNum: true}

	case pgtype.Interval:
		if !t.Valid {
			return Cell{Display: "—"}
		}
		return Cell{Display: formatInterval(t)}

	case uint32:
		// OID-typed values (pure oid type, not regclass which arrives as string).
		return Cell{Display: strconv.FormatUint(uint64(t), 10), Num: float64(t), HasNum: true}

	case []string:
		return Cell{Display: strings.Join(t, ", ")}

	case []int64:
		ss := make([]string, len(t))
		for i, n := range t {
			ss[i] = strconv.FormatInt(n, 10)
		}
		return Cell{Display: strings.Join(ss, ", ")}

	default:
		return Cell{Display: fmt.Sprintf("%v", v)}
	}
}

// formatFloat renders f with up to 2 decimal places, stripping trailing
// zeros so "12.00" becomes "12" and "3.10" becomes "3.1".
func formatFloat(f float64) string { return humanize.Float(f, 2) }

// formatInterval renders a pgtype.Interval as a human-readable string
// similar to PostgreSQL's interval output but condensed for table cells.
func formatInterval(iv pgtype.Interval) string {
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

// ColIndex finds a column by name, -1 when absent.
func ColIndex(cols []Column, name string) int {
	for i, c := range cols {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// ColIdx is ColIndex over the result's own columns.
func (r *Result) ColIdx(name string) int { return ColIndex(r.Columns, name) }

// Num reads the numeric value of row[idx], false when the column is missing
// or the cell carries no number (NULL, text).
func Num(row []Cell, idx int) (float64, bool) {
	if idx < 0 || idx >= len(row) || !row[idx].HasNum {
		return 0, false
	}
	return row[idx].Num, true
}
