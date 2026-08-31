package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestTokenizeSQL(t *testing.T) {
	type tok struct {
		kind sqlTokenKind
		text string
	}
	cases := []struct {
		name string
		in   string
		want []tok
	}{
		{"basic select", "SELECT * FROM t WHERE id = $1", []tok{
			{tokKeyword, "SELECT"}, {tokOp, "*"}, {tokKeyword, "FROM"}, {tokIdent, "t"},
			{tokKeyword, "WHERE"}, {tokIdent, "id"}, {tokOp, "="}, {tokParam, "$1"},
		}},
		{"lowercase keyword", "select 1", []tok{
			{tokKeyword, "select"}, {tokNumber, "1"},
		}},
		{"param cast", "$1::timestamptz", []tok{
			{tokParam, "$1"}, {tokOp, "::"}, {tokIdent, "timestamptz"},
		}},
		{"string with escape", "'it''s'", []tok{{tokString, "'it''s'"}}},
		{"quoted ident with escape", `"weird ""col"""`, []tok{{tokQuotedIdent, `"weird ""col"""`}}},
		{"dollar quoted", "$$body$$", []tok{{tokString, "$$body$$"}}},
		{"tagged dollar quoted", "$fn$ x $fn$", []tok{{tokString, "$fn$ x $fn$"}}},
		{"param not dollar quote", "$2", []tok{{tokParam, "$2"}}},
		{"exponent number", "1.5e-3", []tok{{tokNumber, "1.5e-3"}}},
		{"line comment ends at newline", "-- c\nSELECT", []tok{
			{tokComment, "-- c"}, {tokKeyword, "SELECT"},
		}},
		{"block comment", "/* tag */ SELECT 1", []tok{
			{tokComment, "/* tag */"}, {tokKeyword, "SELECT"}, {tokNumber, "1"},
		}},
		{"unterminated string", "'abc", []tok{{tokString, "'abc"}}},
		{"unterminated quoted ident", `"abc`, []tok{{tokQuotedIdent, `"abc`}}},
		{"unterminated dollar quote", "$$abc", []tok{{tokString, "$$abc"}}},
		{"multichar operators", "a ->> b @> c <= d", []tok{
			{tokIdent, "a"}, {tokOp, "->>"}, {tokIdent, "b"}, {tokOp, "@>"},
			{tokIdent, "c"}, {tokOp, "<="}, {tokIdent, "d"},
		}},
		{"dotted path", `"t"."col"`, []tok{
			{tokQuotedIdent, `"t"`}, {tokOp, "."}, {tokQuotedIdent, `"col"`},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tokenizeSQL(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d tokens, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				if got[i].kind != w.kind || got[i].text != w.text {
					t.Errorf("token %d: got (%d %q), want (%d %q)", i, got[i].kind, got[i].text, w.kind, w.text)
				}
			}
		})
	}
}

func TestTokenizeSQLDepth(t *testing.T) {
	toks := tokenizeSQL("a IN (SELECT b FROM c)")
	for _, tk := range toks {
		switch tk.text {
		case "a", "IN":
			if tk.depth != 0 {
				t.Errorf("%q depth = %d, want 0", tk.text, tk.depth)
			}
		case "SELECT", "FROM":
			if tk.depth != 1 {
				t.Errorf("%q depth = %d, want 1", tk.text, tk.depth)
			}
		}
	}
}

// plainLines strips styling so clause-layout assertions read the raw text.
func plainLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}
	return out
}

func TestHighlightSQLClauseBreaks(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"select join where order limit",
			"SELECT a, b FROM t LEFT OUTER JOIN u ON u.id = t.id WHERE x = $1 AND y IN ($2, $3) ORDER BY a DESC LIMIT $4",
			[]string{
				"SELECT a, b",
				"FROM t",
				"LEFT OUTER JOIN u ON u.id = t.id",
				"WHERE x = $1",
				"  AND y IN ($2, $3)",
				"ORDER BY a DESC",
				"LIMIT $4",
			}},
		{"no breaks inside subquery",
			"SELECT a FROM t WHERE id IN (SELECT id FROM u WHERE v = 1)",
			[]string{
				"SELECT a",
				"FROM t",
				"WHERE id IN (SELECT id FROM u WHERE v = 1)",
			}},
		{"leading SET gets an indented body line",
			"SET search_path TO public",
			[]string{"SET", "  search_path TO public"}},
		{"update set one assignment per line",
			"update production set modified_at=$1, started_at=coalesce($2, now()) where id=$3 and modified_at=$1",
			[]string{
				"update production",
				"set",
				"  modified_at=$1,",
				"  started_at=coalesce($2, now())",
				"where id=$3",
				"  and modified_at=$1",
			}},
		{"where breaks only at top-level and/or",
			"SELECT a FROM t WHERE a = 1 AND (b = 2 OR c = 3) AND d = 4",
			[]string{
				"SELECT a",
				"FROM t",
				"WHERE a = 1",
				"  AND (b = 2 OR c = 3)",
				"  AND d = 4",
			}},
		{"on conflict do update set",
			"INSERT INTO t (a) VALUES ($1) ON CONFLICT (a) DO UPDATE SET a = $1, b = $2",
			[]string{
				"INSERT INTO t (a)",
				"VALUES ($1)",
				"ON CONFLICT (a) DO UPDATE",
				"SET",
				"  a = $1,",
				"  b = $2",
			}},
		{"on conflict breaks, bare on does not",
			"INSERT INTO t (a) VALUES ($1) ON CONFLICT (a) DO NOTHING",
			[]string{
				"INSERT INTO t (a)",
				"VALUES ($1)",
				"ON CONFLICT (a) DO NOTHING",
			}},
		{"leading comment tag on its own line",
			"/* Repo.findByKey */ select a from t where k = $1",
			[]string{
				"/* Repo.findByKey */",
				"select a",
				"from t",
				"where k = $1",
			}},
		{"union all rides with union",
			"SELECT a FROM t UNION ALL SELECT a FROM u",
			[]string{
				"SELECT a",
				"FROM t",
				"UNION ALL SELECT a",
				"FROM u",
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := plainLines(highlightSQL(tc.in, 500))
			if len(got) != len(tc.want) {
				t.Fatalf("got %d lines %q, want %d %q", len(got), got, len(tc.want), tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("line %d:\n got %q\nwant %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestHighlightSQLWrapInvariants(t *testing.T) {
	queries := []string{
		"SELECT \"oauth_accesstoken\".\"id\", \"oauth_accesstoken\".\"token\" FROM \"oauth_accesstoken\" LEFT OUTER JOIN \"auth_user\" ON (\"oauth_accesstoken\".\"user_id\" = \"auth_user\".\"id\") WHERE \"oauth_accesstoken\".\"token_checksum\" = $1 LIMIT $2",
		"SELECT " + strings.Repeat("x", 200) + " FROM t",
		"UPDATE t SET a = $1, b = 'text with spaces' WHERE id = $2 RETURNING *",
	}
	stripSpace := func(s string) string {
		return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }), "")
	}
	for _, q := range queries {
		for _, width := range []int{8, 20, 40, 80} {
			lines := highlightSQL(q, width)
			if len(lines) == 0 {
				t.Fatalf("width %d: no lines", width)
			}
			var joined strings.Builder
			for i, l := range lines {
				plain := ansi.Strip(l)
				if w := lipgloss.Width(l); w > width {
					t.Errorf("width %d line %d overflows (%d): %q", width, i, w, plain)
				}
				if strings.TrimSpace(plain) == "" {
					t.Errorf("width %d line %d is blank", width, i)
				}
				joined.WriteString(plain)
				joined.WriteByte(' ')
			}
			// No characters may be lost or duplicated by splitting/wrapping.
			if got, want := stripSpace(joined.String()), stripSpace(q); got != want {
				t.Errorf("width %d: content mismatch\n got %q\nwant %q", width, got, want)
			}
		}
	}
}

func TestStyleSQLText(t *testing.T) {
	if got, want := styleSQLText(tokKeyword, "SELECT"), styleSQLKeyword.Render("SELECT"); got != want {
		t.Errorf("keyword: got %q want %q", got, want)
	}
	if got, want := styleSQLText(tokParam, "$1"), styleSQLLiteral.Render("$1"); got != want {
		t.Errorf("param: got %q want %q", got, want)
	}
	if got, want := styleSQLText(tokString, "'x'"), styleSQLLiteral.Render("'x'"); got != want {
		t.Errorf("string: got %q want %q", got, want)
	}
	wantQuoted := styleMuted.Render(`"`) + "name" + styleMuted.Render(`"`)
	if got := styleSQLText(tokQuotedIdent, `"name"`); got != wantQuoted {
		t.Errorf("quoted ident: got %q want %q", got, wantQuoted)
	}
	if got := styleSQLText(tokIdent, "t"); got != "t" {
		t.Errorf("ident must stay unstyled, got %q", got)
	}
	// End-to-end: the emitted line carries the bolded keyword verbatim.
	lines := highlightSQL("SELECT 1", 100)
	if len(lines) != 1 || !strings.Contains(lines[0], styleSQLKeyword.Render("SELECT")) {
		t.Errorf("highlighted line missing bold SELECT: %q", lines)
	}
}
