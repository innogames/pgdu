package pg

import (
	"context"
	"strings"
	"sync"
	"time"

	"pgdu/internal/diagres"
)

// SchemaHealth is the system overview's per-database catalog sweep: the seven
// diagnostics whose mere row count is a finding (a sequence near its ceiling, a
// table the planner has stale statistics for, a bloated or invalid index …),
// boiled down to counts, wasted bytes and a few names. It is loaded separately
// from MaintenanceInfo — the bloat estimates take seconds on a big catalog —
// and only on demand, never on the overview's auto-refresh tick.
type SchemaHealth struct {
	DB        string
	SampledAt time.Time

	Sequences        SchemaCheck
	StaleStats       SchemaCheck
	FKMissingIndex   SchemaCheck
	TableBloat       SchemaCheck
	IndexBloat       SchemaCheck
	InvalidIndexes   SchemaCheck
	DuplicateIndexes SchemaCheck
}

// SchemaCheck is one sweep result. Every row the diagnostic returned is already
// past that diagnostic's own server-side filter, so Rows is the finding count.
type SchemaCheck struct {
	Rows   int
	Bytes  int64    // Σ of the wasted-bytes column, where the diagnostic has one
	MaxPct float64  // sequences: the most-consumed consumed_pct
	Top    []string // up to schemaTopN object names, worst first
	Err    error    // the check could not be evaluated (timeout, privilege)
}

// topNames lists the worst offenders, with an ellipsis when the sweep saw
// more than it names.
func (c SchemaCheck) topNames() string {
	s := strings.Join(c.Top, ", ")
	if c.Rows > len(c.Top) {
		s += ", …"
	}
	return s
}

// Checks returns the sweep results keyed by a short label, in display order.
func (h *SchemaHealth) Checks() map[string]SchemaCheck {
	return map[string]SchemaCheck{
		"sequences":         h.Sequences,
		"stale statistics":  h.StaleStats,
		"fk without index":  h.FKMissingIndex,
		"table bloat":       h.TableBloat,
		"index bloat":       h.IndexBloat,
		"invalid indexes":   h.InvalidIndexes,
		"duplicate indexes": h.DuplicateIndexes,
	}
}

const (
	// schemaFanout is how many catalog scans run at once: enough to finish fast
	// without stampeding a server that is already unwell. Each check has its own
	// budget so one slow bloat estimate degrades to "could not evaluate" instead
	// of eating the whole sweep's time.
	schemaFanout       = 3
	schemaCheckTimeout = 15 * time.Second
	schemaTopN         = 3
)

// schemaCheckDef says how one diagnostic's result folds into a SchemaCheck:
// which column is summed into Bytes, which is maxed into MaxPct, and which
// columns name an object for Top. All seven are PerDB registry diagnostics, so
// the SQL and column definitions stay single-sourced in diag_defs_*.go.
type schemaCheckDef struct {
	key      string
	bytesCol string
	pctCol   string
	nameCols []string
	dst      func(*SchemaHealth) *SchemaCheck
}

var schemaCheckDefs = []schemaCheckDef{
	{key: "sequences", pctCol: "consumed_pct", nameCols: []string{"schema", "sequence"},
		dst: func(h *SchemaHealth) *SchemaCheck { return &h.Sequences }},
	{key: "stale_statistics", nameCols: []string{"schema", "table_name"},
		dst: func(h *SchemaHealth) *SchemaCheck { return &h.StaleStats }},
	{key: "fk_missing_index", nameCols: []string{"schema", "table_name"},
		dst: func(h *SchemaHealth) *SchemaCheck { return &h.FKMissingIndex }},
	{key: "bloat_table", bytesCol: "bloat_bytes", nameCols: []string{"schemaname", "tablename"},
		dst: func(h *SchemaHealth) *SchemaCheck { return &h.TableBloat }},
	{key: "bloat_index", bytesCol: "bloat_bytes", nameCols: []string{"schema_name", "index_name"},
		dst: func(h *SchemaHealth) *SchemaCheck { return &h.IndexBloat }},
	{key: "index_invalid", nameCols: []string{"schema", "index_name"},
		dst: func(h *SchemaHealth) *SchemaCheck { return &h.InvalidIndexes }},
	// idx2 is already a schema-qualified regclass text.
	{key: "index_show_duplicate", bytesCol: "wasted_bytes", nameCols: []string{"idx2"},
		dst: func(h *SchemaHealth) *SchemaCheck { return &h.DuplicateIndexes }},
}

// SchemaHealth runs the catalog sweep against db. It never fails as a whole:
// each check's error lands in its own Err.
func (c *Client) SchemaHealth(ctx context.Context, db string) *SchemaHealth {
	h := &SchemaHealth{DB: db, SampledAt: time.Now()}
	sem := make(chan struct{}, schemaFanout)
	var wg sync.WaitGroup
	for _, def := range schemaCheckDefs {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, schemaCheckTimeout)
			defer cancel()
			*def.dst(h) = c.runSchemaCheck(cctx, db, def)
		})
	}
	wg.Wait()
	return h
}

func (c *Client) runSchemaCheck(ctx context.Context, db string, def schemaCheckDef) SchemaCheck {
	d, ok := DiagnosticByKey(def.key)
	if !ok {
		return SchemaCheck{Err: errUnknownDiagnostic(def.key)}
	}
	res, err := c.RunDiagnostic(ctx, db, d)
	if err != nil {
		return SchemaCheck{Err: err}
	}
	chk := SchemaCheck{Rows: len(res.Rows)}
	if def.bytesCol != "" {
		chk.Bytes = int64(diagSum(res, def.bytesCol))
	}
	if def.pctCol != "" {
		chk.MaxPct = diagMax(res, def.pctCol)
	}
	idx := make([]int, 0, len(def.nameCols))
	for _, col := range def.nameCols {
		if i := res.ColIdx(col); i >= 0 {
			idx = append(idx, i)
		}
	}
	for _, row := range res.Rows[:min(len(res.Rows), schemaTopN)] {
		parts := make([]string, 0, len(idx))
		for _, i := range idx {
			parts = append(parts, row[i].Display)
		}
		chk.Top = append(chk.Top, strings.Join(parts, "."))
	}
	return chk
}

type errUnknownDiagnostic string

func (e errUnknownDiagnostic) Error() string { return "unknown diagnostic " + string(e) }

// diagNum reads the numeric value of row[idx], false when the column is
// missing or the cell carries no number (NULL, text).
func diagNum(row []DiagCell, idx int) (float64, bool) { return diagres.Num(row, idx) }

// diagSum totals a named column over every row; cells without a number
// (missing column, NULL, text) contribute nothing.
func diagSum(res *DiagResult, col string) float64 {
	idx := res.ColIdx(col)
	var sum float64
	for _, row := range res.Rows {
		if v, ok := diagNum(row, idx); ok {
			sum += v
		}
	}
	return sum
}

// diagMax is diagSum's maximum sibling; with no numeric cells it returns 0.
func diagMax(res *DiagResult, col string) float64 {
	idx := res.ColIdx(col)
	var maxV float64
	for _, row := range res.Rows {
		if v, ok := diagNum(row, idx); ok && v > maxV {
			maxV = v
		}
	}
	return maxV
}
