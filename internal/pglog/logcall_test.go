package pglog

import (
	"strings"
	"testing"
	"time"
)

// A cluster-wide log: the same call shape from two databases, a truncated
// parameter list, an incomplete one, a simple-protocol statement with inlined
// values, a duration-only line and a log_statement execute line.
const boundCallLog = `2026-09-07 06:54:59 UTC [1-1] u@shop LOG:  duration: 107.000 ms  execute <unnamed>: /* Repo.find */ SELECT * FROM t WHERE a = $1 AND b = 'x' LIMIT 10
2026-09-07 06:54:59 UTC [1-2] u@shop DETAIL:  Parameters: $1 = '5'
2026-09-07 06:55:00 UTC [2-1] u@shop LOG:  duration: 199.000 ms  execute <unnamed>: /* Repo.find */ SELECT * FROM t WHERE a = $1 AND b = 'y' LIMIT 10
2026-09-07 06:55:00 UTC [2-2] u@shop DETAIL:  Parameters: $1 = '7'
2026-09-07 06:55:01 UTC [3-1] u@other LOG:  duration: 300.000 ms  execute <unnamed>: /* Repo.find */ SELECT * FROM t WHERE a = $1 AND b = 'z' LIMIT 10
2026-09-07 06:55:01 UTC [3-2] u@other DETAIL:  Parameters: $1 = '9'
2026-09-07 06:55:02 UTC [4-1] u@shop LOG:  duration: 400.000 ms  execute <unnamed>: /* Repo.find */ SELECT * FROM t WHERE a = $1 AND b = 'w' LIMIT 10
2026-09-07 06:55:02 UTC [4-2] u@shop DETAIL:  Parameters: $1 = 'abcdefghij...
2026-09-07 06:55:03 UTC [5-1] u@shop LOG:  duration: 500.000 ms  execute <unnamed>: SELECT * FROM t2 WHERE a = $1 AND b = $2
2026-09-07 06:55:03 UTC [5-2] u@shop DETAIL:  Parameters: $1 = '1'
2026-09-07 06:55:04 UTC [6-1] u@shop LOG:  duration: 600.000 ms  statement: SELECT * FROM t3 WHERE a = 5 AND b = 'x' LIMIT 10
2026-09-07 06:55:05 UTC [7-1] u@shop LOG:  duration: 5.000 ms
2026-09-07 06:55:06 UTC [8-1] u@shop LOG:  execute <unnamed>: SELECT * FROM t4 WHERE a = $1
2026-09-07 06:55:06 UTC [8-2] u@shop DETAIL:  Parameters: $1 = '3'
`

func loadBoundCallLog(t *testing.T, text, prefix string) *Report {
	t.Helper()
	r, err := Load(t.Context(), &memSource{data: []byte(text)}, prefix, time.UTC, 0, AggOptions{MaxSamples: 1})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLatestBoundCallByText(t *testing.T) {
	r := loadBoundCallLog(t, boundCallLog, "%t [%p-%l] %q%u@%d ")
	// pg_stat_statements numbers the inlined constants after the bound $1 and
	// carries a different call site's comment; the newest complete call from the
	// same database wins — not the truncated one after it, not the other db's.
	pgss := "/* Other.site */ SELECT * FROM t WHERE a = $1 AND b = $2 LIMIT $3"
	bc, ok := r.LatestBoundCall(0, pgss, "shop")
	if !ok || len(bc.Values) != 1 || bc.Values[0] != "'7'" {
		t.Fatalf("shop: ok=%v %+v", ok, bc.Values)
	}
	if got := bc.Call(); got != "/* Repo.find */ SELECT * FROM t WHERE a = '7' AND b = 'y' LIMIT 10" {
		t.Errorf("Call() = %s", got)
	}
	if bc.Entry.DurationMs != 199 || bc.Entry.Time.Format("15:04:05") != "06:55:00" {
		t.Errorf("entry = %+v", bc.Entry)
	}
	// Without a database to hold it to (prefix lacks %d, or caller has none) the
	// other database's newer call is fair game.
	if bc, ok := r.LatestBoundCall(0, pgss, ""); !ok || bc.Values[0] != "'9'" {
		t.Errorf("any db: ok=%v %+v", ok, bc.Values)
	}
	if bc, ok := r.LatestBoundCall(0, pgss, "other"); !ok || bc.Values[0] != "'9'" {
		t.Errorf("other db: ok=%v %+v", ok, bc.Values)
	}
	// A parameter list shorter than the placeholders is not a call.
	if _, ok := r.LatestBoundCall(0, "SELECT * FROM t2 WHERE a = $1 AND b = $2", "shop"); ok {
		t.Error("incomplete parameter list accepted")
	}
	// A simple-protocol statement with every value inlined is complete as it is.
	bc, ok = r.LatestBoundCall(0, "SELECT * FROM t3 WHERE a = $1 AND b = $2 LIMIT $3", "shop")
	if !ok || bc.Values != nil || bc.Call() != "SELECT * FROM t3 WHERE a = 5 AND b = 'x' LIMIT 10" {
		t.Errorf("inlined: ok=%v %+v call=%q", ok, bc.Values, bc.Call())
	}
	// log_statement lines carry executed text too.
	if bc, ok := r.LatestBoundCall(0, "SELECT * FROM t4 WHERE a = $1", "shop"); !ok || bc.Entry.Category != CatStatement || bc.Call() != "SELECT * FROM t4 WHERE a = '3'" {
		t.Errorf("log_statement: ok=%v %+v", ok, bc)
	}
	if _, ok := r.LatestBoundCall(0, "SELECT 42", "shop"); ok {
		t.Error("unrelated query matched")
	}
	if _, ok := r.LatestBoundCall(0, "", "shop"); ok {
		t.Error("empty query matched")
	}
	var nilReport *Report
	if _, ok := nilReport.LatestBoundCall(0, "SELECT 1", ""); ok {
		t.Error("nil report matched")
	}
	if _, ok := (&Report{}).LatestBoundCall(0, "SELECT 1", ""); ok {
		t.Error("empty report matched")
	}
}

const queryIDLog = `2026-09-07 06:54:59.000 UTC [1] u@shop 42 LOG:  duration: 107.000 ms  execute <unnamed>: SELECT x FROM a WHERE id = $1
2026-09-07 06:54:59.000 UTC [1] u@shop 42 DETAIL:  Parameters: $1 = '1'
2026-09-07 06:55:00.000 UTC [2] u@shop 43 LOG:  duration: 107.000 ms  execute <unnamed>: SELECT x FROM a WHERE id = $1
2026-09-07 06:55:00.000 UTC [2] u@shop 43 DETAIL:  Parameters: $1 = '2'
`

func TestLatestBoundCallByQueryID(t *testing.T) {
	r := loadBoundCallLog(t, queryIDLog, "%m [%p] %q%u@%d %Q ")
	for i := range r.Entries {
		if r.Entries[i].QueryID == 0 {
			t.Fatalf("entry %d has no query id: %+v", i, r.Entries[i])
		}
	}
	q := "SELECT x FROM a WHERE id = $1"
	// An id match wins over a newer text match, and needs no text agreement.
	for _, query := range []string{q, "SELECT something_else FROM b"} {
		if bc, ok := r.LatestBoundCall(42, query, "shop"); !ok || bc.Values[0] != "'1'" {
			t.Errorf("id 42 with %q: ok=%v %+v", query, ok, bc.Values)
		}
	}
	// No id on the pgss side, or an id the log never saw: newest text match.
	for _, id := range []int64{0, 99} {
		if bc, ok := r.LatestBoundCall(id, q, "shop"); !ok || bc.Values[0] != "'2'" {
			t.Errorf("id %d: ok=%v %+v", id, ok, bc.Values)
		}
	}
	if bc, ok := r.LatestBoundCall(43, q, "shop"); !ok || bc.Values[0] != "'2'" {
		t.Errorf("id 43: ok=%v %+v", ok, bc.Values)
	}
	if _, ok := r.LatestBoundCall(99, "SELECT nothing", "shop"); ok {
		t.Error("unknown id and text matched")
	}
	if !strings.Contains(r.Prefix, "%Q") {
		t.Errorf("prefix = %q", r.Prefix)
	}
}
