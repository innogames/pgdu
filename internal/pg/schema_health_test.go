package pg

import (
	"context"
	"strings"
	"testing"

	"pgdu/internal/cli"
)

func TestDiagNumAndSum(t *testing.T) {
	res := &DiagResult{Columns: []DiagColumn{{Name: "a"}, {Name: "b"}}}
	if got := res.ColIdx("b"); got != 1 {
		t.Errorf("ColIdx(b) = %d, want 1", got)
	}
	if got := res.ColIdx("missing"); got != -1 {
		t.Errorf("ColIdx(missing) = %d, want -1", got)
	}
	row := []DiagCell{{Display: "x"}, {Num: 42, HasNum: true}}
	if v, ok := diagNum(row, 1); !ok || v != 42 {
		t.Errorf("diagNum(1) = %v,%v, want 42,true", v, ok)
	}
	if _, ok := diagNum(row, 0); ok {
		t.Error("diagNum on text cell should be false")
	}
	if _, ok := diagNum(row, -1); ok {
		t.Error("diagNum(-1) should be false")
	}
	res.Rows = [][]DiagCell{row, {{Display: "y"}, {Num: 8, HasNum: true}}, {{Display: "z"}, {}}}
	if got := diagSum(res, "b"); got != 50 {
		t.Errorf("diagSum = %v, want 50", got)
	}
	if got := diagMax(res, "b"); got != 42 {
		t.Errorf("diagMax = %v, want 42", got)
	}
	if got := diagSum(res, "a"); got != 0 {
		t.Errorf("diagSum over text = %v, want 0", got)
	}
}

// Every sweep check names a registered, per-database diagnostic, and the
// columns it folds exist in that diagnostic's SQL.
func TestSchemaCheckDefsRegistered(t *testing.T) {
	for _, def := range schemaCheckDefs {
		d, ok := DiagnosticByKey(def.key)
		if !ok {
			t.Errorf("%s: not in the Diagnostics registry", def.key)
			continue
		}
		if !d.PerDB {
			t.Errorf("%s: sweep checks must be per-database diagnostics", def.key)
		}
		for _, col := range append([]string{def.bytesCol, def.pctCol}, def.nameCols...) {
			if col != "" && !strings.Contains(d.SQL, col) {
				t.Errorf("%s: column %q not found in its SQL", def.key, col)
			}
		}
	}
}

// With no server reachable every check must land its own error; the sweep
// itself never fails.
func TestSchemaHealthDegradesPerCheck(t *testing.T) {
	c := New(cli.Config{Database: "nope"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := c.SchemaHealth(ctx, "nope")
	if h.DB != "nope" || h.SampledAt.IsZero() {
		t.Errorf("sweep header = %+v", h)
	}
	checks := h.Checks()
	if len(checks) != len(schemaCheckDefs) {
		t.Fatalf("Checks() lists %d checks, want %d", len(checks), len(schemaCheckDefs))
	}
	for name, chk := range checks {
		if chk.Err == nil {
			t.Errorf("%s: expected an error with a cancelled context", name)
		}
	}
	// Errors keep the schema rules silent instead of reporting zero findings.
	if got := MaintAdvice(healthyInfo(), h); len(got) != 0 {
		t.Errorf("failed sweep produced advice: %+v", got)
	}
}

func TestSchemaCheckTopNames(t *testing.T) {
	c := SchemaCheck{Rows: 5, Top: []string{"public.a", "public.b", "public.c"}}
	if got := c.topNames(); got != "public.a, public.b, public.c, …" {
		t.Errorf("topNames = %q", got)
	}
	c.Rows = 3
	if got := c.topNames(); got != "public.a, public.b, public.c" {
		t.Errorf("topNames = %q", got)
	}
}

func TestSchemaAdvice(t *testing.T) {
	h := &SchemaHealth{
		DB:               "shop",
		Sequences:        SchemaCheck{Rows: 1, MaxPct: 97.2, Top: []string{"public.orders_id_seq"}},
		StaleStats:       SchemaCheck{Rows: 12, Top: []string{"public.events", "public.orders", "public.items"}},
		FKMissingIndex:   SchemaCheck{Rows: 2, Top: []string{"public.lines", "public.notes"}},
		TableBloat:       SchemaCheck{Rows: 1, Bytes: 3 * gib, Top: []string{"public.events"}},
		IndexBloat:       SchemaCheck{Rows: 4, Bytes: 800 * mib, Top: []string{"public.events_ts_idx"}},
		InvalidIndexes:   SchemaCheck{Rows: 1, Top: []string{"public.orders_ccnew"}},
		DuplicateIndexes: SchemaCheck{Rows: 1, Bytes: 120 * mib, Top: []string{"public.orders_dup"}},
	}
	got := MaintAdvice(healthyInfo(), h)
	want := map[string]struct {
		level AdviceLevel
		diag  string
	}{
		"schema_sequences":       {AdviceCrit, "sequences"},
		"schema_stale_stats":     {AdviceWarn, "stale_statistics"},
		"schema_fk_index":        {AdviceWarn, "fk_missing_index"},
		"schema_bloat_table":     {AdviceWarn, "bloat_table"},
		"schema_bloat_index":     {AdviceWarn, "bloat_index"},
		"schema_index_invalid":   {AdviceWarn, "index_invalid"},
		"schema_index_duplicate": {AdviceWarn, "index_show_duplicate"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d advice, want %d: %+v", len(got), len(want), got)
	}
	for key, w := range want {
		a := got.Find(key)
		if a == nil {
			t.Fatalf("missing %s", key)
		}
		if a.Level != w.level || a.Target != AdviceTargetDiagnostic || a.DiagKey != w.diag || a.DB != "shop" {
			t.Errorf("%s = %+v", key, *a)
		}
		if !strings.Contains(a.Reason, "(in shop)") || !strings.Contains(a.Reason, a.Key[len("schema_"):len("schema_")+2]) && a.Reason == "" {
			t.Errorf("%s: reason %q must name the database", key, a.Reason)
		}
	}
	if a := got.Find("schema_stale_stats"); !strings.Contains(a.Reason, "public.events, public.orders, public.items, …") {
		t.Errorf("stale stats reason = %q, want the top names with an ellipsis", a.Reason)
	}
	if a := got.Find("schema_sequences"); !strings.HasPrefix(a.Reason, "public.orders_id_seq at 97.2%") {
		t.Errorf("sequence reason = %q", a.Reason)
	}

	// A sequence below the warn floor and empty checks stay silent.
	quiet := &SchemaHealth{DB: "shop", Sequences: SchemaCheck{Rows: 1, MaxPct: 45}}
	if got := MaintAdvice(healthyInfo(), quiet); len(got) != 0 {
		t.Errorf("quiet sweep produced advice: %+v", got)
	}
	if got := MaintAdvice(healthyInfo(), nil); len(got) != 0 {
		t.Errorf("nil sweep produced advice: %+v", got)
	}
}
