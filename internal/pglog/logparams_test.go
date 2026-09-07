package pglog

import (
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
