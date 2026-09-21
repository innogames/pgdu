package pglog

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParams(t *testing.T) {
	cases := []struct {
		detail string
		want   []string
	}{
		{"Parameters: $1 = '522763'", []string{"'522763'"}},
		{"Parameters: $1 = '1000', $2 = '1000'", []string{"'1000'", "'1000'"}},
		{"Parameters: $1 = 'it''s, $2 = fake', $2 = NULL, $3 = ''", []string{"'it''s, $2 = fake'", "NULL", "''"}},
		{"Parameters: $1 = '{1,2}'", []string{"'{1,2}'"}},
	}
	for _, c := range cases {
		e := &Entry{Detail: []byte(c.detail)}
		got, ok := Params(e)
		if !ok || strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("Params(%q) = %q, %v; want %q", c.detail, got, ok, c.want)
		}
	}
	if _, ok := Params(&Entry{Detail: []byte("Key (id)=(1) already exists.")}); ok {
		t.Error("non-parameter DETAIL reported as parameters")
	}
	// No DETAIL: the literals inlined in the SQL stand in for bound parameters.
	inl := &Entry{SQL: []byte("SELECT pg_advisory_xact_lock('0', '17071')")}
	if got, ok := Params(inl); !ok || strings.Join(got, "|") != "'0'|'17071'" {
		t.Errorf("inline literals = %q, %v", got, ok)
	}
	if k := ParamKey(&Entry{SQL: []byte("SELECT now()")}, false); k != NoParams {
		t.Errorf("literal-free SQL key = %q", k)
	}
	if got := SQLLiterals("UPDATE t SET a = 1.5e3, b = E'x\\'y', c = $$dq$$ WHERE id = $1 AND n = 0x1f"); strings.Join(got, "|") != "1.5e3|'x\\'y'|$$dq$$|0" {
		t.Errorf("SQLLiterals = %q", got)
	}
	if k := ParamKey(&Entry{}, false); k != NoParams {
		t.Errorf("no-detail key = %q", k)
	}
	e := &Entry{Detail: []byte("Parameters: $1 = 'a', $2 = 'b'")}
	if k := ParamKey(e, true); k != "'a'" {
		t.Errorf("first-only key = %q", k)
	}
	if k := ParamKey(e, false); k != "'a', 'b'" {
		t.Errorf("full key = %q", k)
	}
}

func TestGroupParams(t *testing.T) {
	text := strings.Join([]string{
		`2026-09-07 06:54:59 UTC [1-1] u@h LOG:  duration: 107.000 ms  execute <unnamed>: SELECT pg_advisory_lock($1)`,
		`2026-09-07 06:54:59 UTC [1-2] u@h DETAIL:  Parameters: $1 = '849064364'`,
		`2026-09-07 06:55:00 UTC [2-1] u@h LOG:  duration: 199.000 ms  execute <unnamed>: SELECT pg_advisory_lock($1)`,
		`2026-09-07 06:55:00 UTC [2-2] u@h DETAIL:  Parameters: $1 = '849064364'`,
		`2026-09-07 06:57:44 UTC [3-1] u@h LOG:  duration: 19300.000 ms  execute <unnamed>: SELECT pg_advisory_lock($1)`,
		`2026-09-07 06:57:44 UTC [3-2] u@h DETAIL:  Parameters: $1 = '6296936'`,
		`2026-09-07 06:58:00 UTC [4-1] u@h LOG:  duration: 120.000 ms  execute <unnamed>: SELECT 1`,
		`2026-09-07 06:59:00 UTC [5-1] u@h ERROR:  boom`,
	}, "\n") + "\n"
	r, err := Load(t.Context(), &memSource{data: []byte(text)}, "%t [%p-%l] %q%u@%h ", time.UTC, 0, AggOptions{MaxSamples: 1})
	if err != nil {
		t.Fatal(err)
	}

	// Entry.Group must point at the sorted group of every entry.
	for i := range r.Entries {
		e := &r.Entries[i]
		key, _ := Fingerprint(e)
		if g := r.Groups[e.Group]; g.Key != key {
			t.Errorf("entry %d: Group → %q, fingerprint %q", i, g.Key, key)
		}
	}
	gi := -1
	for i := range r.Groups {
		if strings.Contains(r.Groups[i].Title, "pg_advisory_lock") {
			gi = i
		}
	}
	if gi < 0 {
		t.Fatal("advisory-lock group not found")
	}
	if n := len(r.Groups[gi].Samples); n != 1 {
		t.Fatalf("samples = %d, want the cap of 1", n)
	}
	rows := GroupParams(r, gi, false)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	// Count-descending: the repeated key first, aggregated over all members
	// even though Samples is capped at one.
	if rows[0].Key != "'849064364'" || rows[0].Count != 2 || rows[0].SumMs != 306 || rows[0].MaxMs != 199 || rows[0].AvgMs() != 153 {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[0].Last.Format("15:04:05") != "06:55:00" || rows[0].Sample != 1 {
		t.Errorf("row 0 last/sample = %v / %d", rows[0].Last, rows[0].Sample)
	}
	if rows[1].Key != "'6296936'" || rows[1].Count != 1 || rows[1].MaxMs != 19300 || !rows[1].HasDur {
		t.Errorf("row 1 = %+v", rows[1])
	}
	if got := GroupParams(r, 99, false); got != nil {
		t.Errorf("out-of-range group = %+v", got)
	}
}

func TestSubstituteParams(t *testing.T) {
	cases := []struct {
		sql    string
		vals   []string
		want   string
		filled int
	}{
		{
			"SELECT * FROM t WHERE id = $1 AND name = $2",
			[]string{"'1213929'", "NULL"},
			"SELECT * FROM t WHERE id = '1213929' AND name = NULL", 2,
		},
		// $1 repeated, and an ordinal past the logged list stays a placeholder.
		{
			"SELECT $1, $1, $3",
			[]string{"'a'", "'b'"},
			"SELECT 'a', 'a', $3", 2,
		},
		// Two-digit ordinals must not be clobbered by the one-digit ones.
		{
			"SELECT $1, $10, $11",
			[]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"},
			"SELECT 1, 10, 11", 3,
		},
		// Placeholders quoted, commented or dollar-quoted are text, not slots.
		{
			"SELECT '$1', \"$1\", $$body $1$$ -- $1\n/* $1 */ WHERE a = $1",
			[]string{"'x'"},
			"SELECT '$1', \"$1\", $$body $1$$ -- $1\n/* $1 */ WHERE a = 'x'", 1,
		},
		// Nothing to fill: the caller shows no sample call.
		{"SELECT now()", []string{"'x'"}, "SELECT now()", 0},
		{"SELECT $1", nil, "SELECT $1", 0},
		// A doubled quote inside a value keeps the literal closed.
		{"SELECT $1", []string{"'it''s'"}, "SELECT 'it''s'", 1},
		// Unterminated literal in the statement: copied through, no panic.
		{"SELECT 'oops $1", []string{"'x'"}, "SELECT 'oops $1", 0},
		{"DO $$ unterminated $1", []string{"'x'"}, "DO $$ unterminated $1", 0},
		// A stray $ that opens no dollar quote must not swallow later slots.
		{"SELECT $notclosed $1", []string{"'x'"}, "SELECT $notclosed 'x'", 1},
	}
	for _, c := range cases {
		got, filled := SubstituteParams(c.sql, c.vals)
		if got != c.want || filled != c.filled {
			t.Errorf("SubstituteParams(%q, %q) = %q, %d; want %q, %d", c.sql, c.vals, got, filled, c.want, c.filled)
		}
	}
}

func TestBoundParamsAndTruncation(t *testing.T) {
	// The literal fallback of Params must not reach a $n substitution.
	inl := &Entry{SQL: []byte("SELECT pg_advisory_xact_lock('0', '17071')")}
	if _, ok := BoundParams(inl); ok {
		t.Error("inlined literals reported as bound parameters")
	}
	e := &Entry{Detail: []byte("Parameters: $1 = 'abc"), SQL: []byte("SELECT $1")}
	vals, ok := BoundParams(e)
	if !ok || !ParamsTruncated(vals) {
		t.Errorf("truncated value not detected: %q, %v", vals, ok)
	}
	if ParamsTruncated([]string{"'a'", "NULL", "''"}) {
		t.Error("complete values reported as truncated")
	}
}

func TestUnfilledParams(t *testing.T) {
	cases := []struct {
		sql  string
		vals []string
		want []int
	}{
		// $2 sits inside a string literal and is not a placeholder.
		{"SELECT $1, $3 WHERE x = '$2'", []string{"'a'"}, []int{3}},
		{"SELECT $2, $2", []string{"'a'"}, []int{2}},
		{"SELECT $1", []string{"'a'"}, nil},
		{"SELECT now()", nil, nil},
		{"SELECT $$body $1$$, $2", []string{"'a'"}, []int{2}},
		{"SELECT $3, $2", nil, []int{2, 3}},
	}
	for _, c := range cases {
		if got := UnfilledParams(c.sql, c.vals); !reflect.DeepEqual(got, c.want) {
			t.Errorf("UnfilledParams(%q, %v) = %v, want %v", c.sql, c.vals, got, c.want)
		}
	}
}
