package pg

import "strings"

// This file holds the Diagnostic.Fix builders: per-row remediation SQL the
// TUI shows when Enter is pressed on a diagnostic result and runs (via
// Client.RunFix) only after an explicit y confirm. Statements stay lock-safe —
// CREATE/REINDEX/DROP INDEX CONCURRENTLY, ANALYZE, plain VACUUM; anything that takes a
// long exclusive lock (VACUUM FULL, CLUSTER) is only ever suggested inside a
// SQL comment. The exceptions are the fillfactor and CLUSTER ON ALTERs, whose
// brief ACCESS EXCLUSIVE lock is defused with an explicit lock_timeout line so
// a run behind a long transaction fails fast instead of queueing every writer.

// DiagRowGetter returns a by-name accessor over one full (unprojected) result
// row: column names match case-insensitively, and a missing or blank cell
// reports ok=false. The TUI hands this to Diagnostic.Fix so builders never
// depend on column order or on the C-picker's visible subset.
func DiagRowGetter(cols []DiagColumn, row []DiagCell) func(string) (string, bool) {
	return func(name string) (string, bool) {
		for i := range cols {
			if i < len(row) && strings.EqualFold(cols[i].Name, name) {
				if row[i].Display == "" {
					return "", false
				}
				return row[i].Display, true
			}
		}
		return "", false
	}
}

// fixQualify builds the relation reference for a fix statement. A value
// already containing a dot came from a ::regclass cast — the server quoted it
// as needed — and passes through verbatim. Otherwise schema.name (or the bare
// name, left to search_path, when the schema column is absent) goes through
// fixIdent, so the common all-lowercase case reads public.orders rather than
// "public"."orders".
func fixQualify(get func(string) (string, bool), schemaCol, nameCol string) (string, bool) {
	name, ok := get(nameCol)
	if !ok {
		return "", false
	}
	if strings.Contains(name, ".") {
		return name, true
	}
	if schema, ok := get(schemaCol); ok {
		return fixIdent(schema) + "." + fixIdent(name), true
	}
	return fixIdent(name), true
}

// fixIdent quotes an identifier only when the server would need it to — the
// same rule as quote_ident(): anything but [a-z_][a-z0-9_]* is quoted, and so
// is any keyword outside the unreserved class (those can't stand bare where a
// relation name goes). Fix statements are meant to be read and pasted, so the
// noise of quoting every name is worth avoiding; the generated DDL elsewhere
// keeps quoteIdent's always-quote rule.
func fixIdent(s string) string {
	if !fixIdentSafe(s) || sqlQuotedKeywords[strings.ToUpper(s)] {
		return quoteIdent(s)
	}
	return s
}

func fixIdentSafe(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// sqlQuotedKeywords is every PostgreSQL keyword quote_ident() quotes: the
// reserved, type/function-name and column-name classes (src/include/parser/
// kwlist.h). Unreserved keywords are omitted — they're legal bare identifiers.
var sqlQuotedKeywords = func() map[string]bool {
	m := map[string]bool{}
	for w := range strings.FieldsSeq(`
		ALL ANALYSE ANALYZE AND ANY ARRAY AS ASC ASYMMETRIC AUTHORIZATION BETWEEN
		BIGINT BINARY BIT BOOLEAN BOTH CASE CAST CHAR CHARACTER CHECK COALESCE
		COLLATE COLLATION COLUMN CONCURRENTLY CONSTRAINT CREATE CROSS
		CURRENT_CATALOG CURRENT_DATE CURRENT_ROLE CURRENT_SCHEMA CURRENT_TIME
		CURRENT_TIMESTAMP CURRENT_USER DEC DECIMAL DEFAULT DEFERRABLE DESC
		DISTINCT DO ELSE END EXCEPT EXISTS EXTRACT FALSE FETCH FLOAT FOR FOREIGN
		FREEZE FROM FULL GRANT GREATEST GROUP GROUPING HAVING ILIKE IN INITIALLY
		INNER INOUT INT INTEGER INTERSECT INTERVAL INTO IS ISNULL JOIN JSON
		JSON_ARRAY JSON_ARRAYAGG JSON_EXISTS JSON_OBJECT JSON_OBJECTAGG JSON_QUERY
		JSON_SCALAR JSON_SERIALIZE JSON_TABLE JSON_VALUE LATERAL LEADING LEAST
		LEFT LIKE LIMIT LOCALTIME LOCALTIMESTAMP MERGE_ACTION NATIONAL NATURAL
		NCHAR NONE NORMALIZE NOT NOTNULL NULL NULLIF NUMERIC OFFSET ON ONLY OR
		ORDER OUT OUTER OVERLAPS OVERLAY PLACING POSITION PRECISION PRIMARY REAL
		REFERENCES RETURNING RIGHT ROW SELECT SESSION_USER SETOF SIMILAR SMALLINT
		SOME SUBSTRING SYMMETRIC SYSTEM_USER TABLE TABLESAMPLE THEN TIME
		TIMESTAMP TO TRAILING TREAT TRIM TRUE UNION UNIQUE USER USING VALUES
		VARCHAR VARIADIC VERBOSE WHEN WHERE WINDOW WITH XMLATTRIBUTES XMLCONCAT
		XMLELEMENT XMLEXISTS XMLFOREST XMLNAMESPACES XMLPARSE XMLPI XMLROOT
		XMLSERIALIZE XMLTABLE`) {
		m[w] = true
	}
	return m
}()

// fixLockTimeout precedes the ALTER TABLE fixes: their ACCESS EXCLUSIVE lock
// is held only for a catalog update, but merely waiting for it blocks every
// later query on the table, so give up quickly rather than queue behind a
// long-running transaction.
const fixLockTimeout = "SET lock_timeout = '3s';"

// fixReindex builds a REINDEX INDEX CONCURRENTLY fix for diagnostics that name
// an index in indexCol (schema in schemaCol); extra lines are appended verbatim.
func fixReindex(schemaCol, indexCol string, extra ...string) func(func(string) (string, bool)) (string, bool) {
	return func(get func(string) (string, bool)) (string, bool) {
		idx, ok := fixQualify(get, schemaCol, indexCol)
		if !ok {
			return "", false
		}
		lines := append([]string{"REINDEX INDEX CONCURRENTLY " + idx + ";"}, extra...)
		return strings.Join(lines, "\n"), true
	}
}

// fixDropIndex builds a DROP INDEX CONCURRENTLY fix; extra lines are appended.
func fixDropIndex(schemaCol, indexCol string, extra ...string) func(func(string) (string, bool)) (string, bool) {
	return func(get func(string) (string, bool)) (string, bool) {
		idx, ok := fixQualify(get, schemaCol, indexCol)
		if !ok {
			return "", false
		}
		lines := append([]string{"DROP INDEX CONCURRENTLY " + idx + ";"}, extra...)
		return strings.Join(lines, "\n"), true
	}
}

// fixTableStmt builds a single-table maintenance statement ("ANALYZE",
// "VACUUM (VERBOSE)", …) for diagnostics that name a table; extra lines are
// appended verbatim.
func fixTableStmt(verb, schemaCol, tableCol string, extra ...string) func(func(string) (string, bool)) (string, bool) {
	return func(get func(string) (string, bool)) (string, bool) {
		tbl, ok := fixQualify(get, schemaCol, tableCol)
		if !ok {
			return "", false
		}
		lines := append([]string{verb + " " + tbl + ";"}, extra...)
		return strings.Join(lines, "\n"), true
	}
}

// fixTableBloat is the bloat_table fix. Plain VACUUM is the only statement
// that runs: it only marks dead space reusable, so the reclaim option stays a
// comment. pg_repack is spelled out as a ready shell line (with -d, since the
// diagnostic is PerDB and the row names its database) because it is the one
// online path that returns space without losing rows; destructive options
// such as TRUNCATE are deliberately not suggested.
func fixTableBloat(get func(string) (string, bool)) (string, bool) {
	tbl, ok := fixQualify(get, "schemaname", "tablename")
	if !ok {
		return "", false
	}
	repack := "pg_repack -t " + tbl
	if db, ok := get("databasename"); ok {
		repack = "pg_repack -d " + db + " -t " + tbl
	}
	return strings.Join([]string{
		"VACUUM (VERBOSE) " + tbl + ";",
		"-- " + repack,
	}, "\n"), true
}

// fixTableFillfactor builds the ALTER for the fillfactor advisor from the
// row's suggested_fill cell (skipped when suggestion == current — nothing to
// change).
func fixTableFillfactor(get func(string) (string, bool)) (string, bool) {
	tbl, ok := fixQualify(get, "schema", "relname")
	if !ok {
		return "", false
	}
	sugg, ok := get("suggested_fill")
	if !ok {
		return "", false
	}
	cur, hasCur := get("current_fill")
	if hasCur && cur == sugg {
		return "", false
	}
	head := "-- apply suggested fillfactor"
	if hasCur {
		head += " (current " + cur + " → " + sugg + ")"
	}
	return strings.Join([]string{
		head,
		fixLockTimeout,
		"ALTER TABLE " + tbl + " SET (fillfactor = " + sugg + ");",
		"-- affects only newly written pages; VACUUM FULL or pg_repack rewrites now",
	}, "\n"), true
}

// fixClusterOn marks the row's index as the table's CLUSTER index (a quick
// catalog-only ALTER) for an index_cluster_candidates row. The actual rewrite
// — CLUSTER's exclusive lock or pg_repack — stays a comment, per this file's
// lock-safety rule. Skipped when the table is already clustered on this index
// (clustered = t).
func fixClusterOn(get func(string) (string, bool)) (string, bool) {
	tbl, ok := fixQualify(get, "schema", "table_name")
	if !ok {
		return "", false
	}
	idx, ok := get("index_name")
	if !ok {
		return "", false
	}
	if clustered, ok := get("clustered"); ok && clustered == "t" {
		return "", false
	}
	return strings.Join([]string{
		fixLockTimeout,
		"ALTER TABLE " + tbl + " CLUSTER ON " + fixIdent(idx) + ";",
		"-- marks the index for CLUSTER (catalog-only; brief ACCESS EXCLUSIVE lock).",
		"-- the rewrite itself still has to run: CLUSTER " + tbl + "; locks the table",
		"-- exclusively for the duration — run off-peak, or use pg_repack instead.",
		"-- rows scatter again as writes continue; re-cluster periodically.",
	}, "\n"), true
}

// fixCreateBrin builds the BRIN index an index_brin_candidates row proposes:
// CREATE INDEX CONCURRENTLY on the correlated column alone, named
// <table>_<column>_brin so it sits next to the btree it is meant to replace.
// Only the build runs — whether the plans still prune is for the user to
// verify, so dropping the btree stays a comment. When the btree keys on more
// columns than the flagged one (index_columns), the drop advice is replaced
// by a warning: a BRIN on one column does not replace lookups on the others.
func fixCreateBrin(get func(string) (string, bool)) (string, bool) {
	tbl, ok := fixQualify(get, "schema", "table_name")
	if !ok {
		return "", false
	}
	tblName, _ := get("table_name")
	col, ok := get("column_name")
	if !ok {
		return "", false
	}
	lines := []string{
		"CREATE INDEX CONCURRENTLY " + fixIdent(tblName+"_"+col+"_brin") +
			" ON " + tbl + " USING brin (" + fixIdent(col) + ");",
		"-- pages_per_range defaults to 128 heap pages per summary; a smaller value prunes",
		"-- finer at the cost of a larger index. A cancelled build leaves an INVALID index.",
		"-- BRIN serves range scans, not point lookups: EXPLAIN the workload's queries on",
		"-- " + fixIdent(col) + " and confirm the new index prunes before touching the btree.",
	}
	idx, hasIdx := fixQualify(get, "schema", "index_name")
	// pg_get_indexdef spells a single plain column as quote_identifier(attname),
	// i.e. either the bare name or the always-quoted form — anything else means
	// the btree has further key columns or expressions.
	cols, hasCols := get("index_columns")
	multi := hasCols && cols != col && cols != quoteIdent(col)
	switch {
	case hasIdx && multi:
		lines = append(lines,
			"-- "+idx+" keys on ("+cols+"): it still serves lookups on the other",
			"-- columns, so the BRIN complements it rather than replacing it — do not drop it.")
	case hasIdx:
		lines = append(lines, "-- then: DROP INDEX CONCURRENTLY "+idx+";")
	}
	return strings.Join(lines, "\n"), true
}

// fixDropDuplicateIndex drops idx2 of an index_show_duplicate row, keeping
// idx1. Both cells are ::regclass output, i.e. already schema-qualified and
// quoted where necessary.
func fixDropDuplicateIndex(get func(string) (string, bool)) (string, bool) {
	idx2, ok := get("idx2")
	if !ok {
		return "", false
	}
	lines := []string{"DROP INDEX CONCURRENTLY " + idx2 + ";"}
	if idx1, ok := get("idx1"); ok {
		lines = append(lines, "-- keeps the identical "+idx1)
	}
	lines = append(lines, "-- verify neither index backs a constraint or replica identity first")
	return strings.Join(lines, "\n"), true
}

// fixDropRedundantIndex drops the redundant_index of an index_redundant_prefix
// row, noting which wider index covers it.
func fixDropRedundantIndex(get func(string) (string, bool)) (string, bool) {
	idx, ok := fixQualify(get, "schema", "redundant_index")
	if !ok {
		return "", false
	}
	lines := []string{"DROP INDEX CONCURRENTLY " + idx + ";"}
	if covered, ok := get("covered_by"); ok {
		lines = append(lines, "-- its key columns are a prefix of "+covered)
	}
	lines = append(lines, "-- verify no query relies on it specifically (index size vs cache) first")
	return strings.Join(lines, "\n"), true
}
