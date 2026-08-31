package pg

import "strings"

// This file holds the Diagnostic.Fix builders: per-row remediation SQL the
// TUI shows (never executes) when Enter is pressed on a diagnostic result.
// Statements stay lock-safe — REINDEX/DROP INDEX CONCURRENTLY, ANALYZE, plain
// VACUUM; anything that takes a long exclusive lock (VACUUM FULL, CLUSTER) is
// only ever suggested inside a SQL comment. The one exception is the
// fillfactor ALTER, whose brief ACCESS EXCLUSIVE lock is defused with an
// explicit lock_timeout line.

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

// fixQualify builds the quoted relation reference for a fix statement. A value
// already containing a dot came from a ::regclass cast — the server quoted it
// as needed — and passes through verbatim. Otherwise schema.name (or the bare
// name, left to search_path, when the schema column is absent) is quoted via
// quoteIdent, same as every other DDL site.
func fixQualify(get func(string) (string, bool), schemaCol, nameCol string) (string, bool) {
	name, ok := get(nameCol)
	if !ok {
		return "", false
	}
	if strings.Contains(name, ".") {
		return name, true
	}
	if schema, ok := get(schemaCol); ok {
		return qualifiedIdent(schema, name), true
	}
	return quoteIdent(name), true
}

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
		"ALTER TABLE " + tbl + " CLUSTER ON " + quoteIdent(idx) + ";",
		"-- marks the index for CLUSTER (catalog-only; brief ACCESS EXCLUSIVE lock).",
		"-- the rewrite itself still has to run: CLUSTER " + tbl + "; locks the table",
		"-- exclusively for the duration — run off-peak, or use pg_repack instead.",
		"-- rows scatter again as writes continue; re-cluster periodically.",
	}, "\n"), true
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
