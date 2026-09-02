package pg

import (
	"reflect"
	"testing"
)

func TestSplitSQLStatements(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"VACUUM (VERBOSE) public.t;", []string{"VACUUM (VERBOSE) public.t"}},
		// advice comments vanish, statements keep their order
		{"-- database: x\nSET lock_timeout = '3s';\nALTER TABLE t SET (fillfactor = 72);\n-- note; with semicolon",
			[]string{"SET lock_timeout = '3s'", "ALTER TABLE t SET (fillfactor = 72)"}},
		// semicolons inside quoted identifiers and literals are content
		{`REINDEX INDEX CONCURRENTLY "a;b";`, []string{`REINDEX INDEX CONCURRENTLY "a;b"`}},
		{`SELECT 'it''s; fine';`, []string{`SELECT 'it''s; fine'`}},
		// comment-only or empty scripts yield nothing
		{"-- nothing to do\n\n;;", nil},
		// a missing trailing semicolon still yields the statement
		{"ANALYZE t", []string{"ANALYZE t"}},
	}
	for _, c := range cases {
		if got := SplitSQLStatements(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("SplitSQLStatements(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}
