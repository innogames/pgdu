package pg

import (
	"slices"
	"testing"
)

func TestMainTable(t *testing.T) {
	cases := map[string]string{
		// SELECT shapes — the first FROM table is the answer.
		"SELECT * FROM game_battle WHERE player_1_id = $1 ORDER BY id DESC LIMIT $2":   "game_battle",
		"select resource, amount from game_bag_resource where bag_id = $1 for update":  "game_bag_resource",
		"SELECT a FROM public.game_battle b JOIN other o ON o.id = b.id":               "public.game_battle",
		"SELECT e.id FROM game_citymap_entity e JOIN states st ON st.entity_id = e.id": "game_citymap_entity",
		// DML.
		"UPDATE game_player SET x = $1 WHERE id = $2":       "game_player",
		"DELETE FROM game_session WHERE id = $1":            "game_session",
		"INSERT INTO game_event (a, b) VALUES ($1, $2)":     "game_event",
		"insert into game_event values ($1)":                "game_event",
		"MERGE INTO inventory t USING src s ON t.id = s.id": "inventory",
		"TABLE game_config":                                 "game_config",
		// ONLY is a no-inherit modifier, not the relation (FK/PK check queries).
		`SELECT 1 FROM ONLY "public"."game_player" x WHERE "id" OPERATOR(pg_catalog.=) $1 FOR KEY SHARE OF x`: `public.game_player`,
		"UPDATE ONLY parts SET a = $1 WHERE id = $2":                                                          "parts",
		"DELETE FROM ONLY parts WHERE id = $1":                                                                "parts",
		// Quoted identifiers are unwrapped: the bare name is what the label and
		// to_regclass want, not the quoted literal.
		`SELECT 1 FROM "MixedCase"`:        `MixedCase`,
		"SELECT 1 FROM myschema.\"Tbl\" x": `myschema.Tbl`,
		// No useful table to point at.
		"VALUES ($1), ($2)":                    "",
		"SELECT 1":                             "",
		"SELECT * FROM (SELECT 1) sub":         "",
		"WITH c AS (SELECT 1) SELECT * FROM c": "c",
		// A main FROM that names a CTE resolves through the CTE to the base relation
		// it reads from — the CTE name is only a stand-in for the real table. The
		// CTE body here is also riddled with `extract($n FROM col)`, whose FROM must
		// not be mistaken for the body's own FROM clause.
		"WITH data AS (SELECT extract($1 from date_start) AS d, queue_id = (SELECT min(queue_id) FROM tw_task_queue WHERE player_id = q.player_id) AS ready FROM tw_task_queue q WHERE task_type = $2) SELECT * FROM data WHERE ready ORDER BY queue_id": "tw_task_queue",
		// Chained CTE references resolve to the ultimate base relation.
		"WITH a AS (SELECT id FROM real_t), b AS (SELECT id FROM a) SELECT * FROM b": "real_t",
		// A RECURSIVE CTE that references itself must not loop forever.
		"WITH RECURSIVE t AS (SELECT 1 UNION ALL SELECT n FROM t) SELECT * FROM t": "t",
		// extract(epoch FROM ts) in a plain SELECT list must not be read as the FROM.
		"SELECT extract(epoch from created_at) FROM events WHERE id = $1": "events",
		// WITH wrapping a DML statement: the subject is the UPDATE/DELETE/INSERT
		// target, not the first FROM (which references the CTE). The CTE body's own
		// keywords are at paren depth > 0 and must be skipped.
		"WITH units_to_delete (player_id, unit_id, count) AS (VALUES ($1,$2,$3)) UPDATE game_army_units u SET count = u.count - to_delete.count FROM units_to_delete to_delete WHERE u.player_id = to_delete.player_id RETURNING u.*": "game_army_units",
		"WITH moved AS (DELETE FROM staging WHERE id = $1 RETURNING *) INSERT INTO archive SELECT * FROM moved":                                                                                                                       "archive",
		"WITH d AS (SELECT id FROM tmp) DELETE FROM events WHERE id IN (SELECT id FROM d)":                                                                                                                                            "events",
		// Count/paginate wrapper: descend into the subquery's own FROM relation.
		"SELECT COUNT(*) FROM (SELECT DISTINCT c.* FROM game_conversation c JOIN game_message m ON c.id = m.conversation_id) AS c": "game_conversation",
		// FROM unnest(…)/generate_series(…) is a set-returning function, not a
		// base relation: skip it to the real table in the EXISTS/JOIN subquery.
		"SELECT wanted.id FROM unnest('{}'::integer[]::int[]) AS wanted(id) WHERE EXISTS ( SELECT 'sample'::text FROM game_great_buildings_construction g WHERE g.owner_player_id = wanted.id )": "game_great_buildings_construction",
		"SELECT * FROM generate_series(1, 10)": "",
		// Leading subquery with no FROM of its own (SELECT generate_series(…)): the
		// real relation is buried in a function-arg scalar subquery. Resolve to it,
		// bounded to the subquery so the outer NOT IN probe doesn't win by accident.
		"SELECT unit_id FROM ( SELECT generate_series( $1, ( SELECT max(unit_id) FROM game_army_units WHERE player_id = $2 ) + $3 ) AS unit_id ) AS series WHERE series.unit_id NOT IN(SELECT unit_id FROM game_army_units WHERE player_id = $4) LIMIT $5": "game_army_units",
		"SET search_path = $1": "",
		"":                     "",
		// DDL from log_statement: the relation the command acts on.
		"ALTER TABLE\n    rift_promotion ADD COLUMN highlight_new boolean DEFAULT false": "rift_promotion",
		"ALTER TABLE IF EXISTS ONLY public.player RENAME TO player_old":                  "public.player",
		"CREATE TABLE player_search (\n id int)":                                         "player_search",
		"CREATE UNLOGGED TABLE IF NOT EXISTS tmp_import (id int)":                        "tmp_import",
		"DROP TABLE IF EXISTS tutorial_state":                                            "tutorial_state",
		"TRUNCATE TABLE ONLY status_indicators":                                          "status_indicators",
		"TRUNCATE a, b":                                                                  "a",
		"REINDEX (VERBOSE) TABLE CONCURRENTLY player":                                    "player",
		"CLUSTER VERBOSE player USING player_pkey":                                       "player",
		"DROP INDEX IF EXISTS player_search__nickname__idx_old":                          "",
		"ALTER INDEX IF EXISTS x RENAME TO y":                                            "",
		"ALTER SEQUENCE s AS bigint":                                                     "",
		"CREATE SEQUENCE IF NOT EXISTS s AS integer":                                     "",
		// CREATE INDEX targets the ON relation — the shape a pg_repack rebuild or a
		// manual build shows in pg_stat_activity, often truncated mid-column-list.
		"CREATE INDEX index_1839593 ON repack.table_19180 USING btree (player_i":              "repack.table_19180",
		"CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ix_p ON ONLY public.game_player (id)": "public.game_player",
		`CREATE INDEX ON "MySchema"."Tbl" (id)`:                                               "MySchema.Tbl",
		// CREATE TABLE names the table being made (it exists once logged); a
		// truncation that cuts CREATE INDEX off before ON must not mislabel.
		"CREATE TABLE t AS SELECT * FROM src":  "t",
		"CREATE INDEX CONCURRENTLY ix_long_na": "",
		// Leading ORM comments must be skipped, not parsed as the statement.
		"/* TechnologyRepository.findAllByPlayerId */ SELECT * FROM technology WHERE id = $1": "technology",
		"/* update for com.example.Battle */update battle set modified = $1 where id = $2":    "battle",
		"-- a note\nUPDATE worker SET x = $1":                                                 "worker",
		"/* multi\nline\ncomment */ SELECT * FROM hero":                                       "hero",
		"/* outer /* nested */ still comment */ DELETE FROM session WHERE id = $1":            "session",
		// Autovacuum worker status lines from pg_stat_activity — point at the table.
		"autovacuum: VACUUM public.game_battle":                         "public.game_battle",
		"autovacuum: VACUUM ANALYZE public.game_battle":                 "public.game_battle",
		"autovacuum: VACUUM public.game_battle (to prevent wraparound)": "public.game_battle",
		"autovacuum: ANALYZE public.game_battle":                        "public.game_battle",
		// Manual VACUUM/ANALYZE commands, including option forms.
		"VACUUM game_battle": "game_battle",
		"VACUUM (VERBOSE, ANALYZE, SKIP_LOCKED) public.parts": "public.parts",
		"VACUUM FULL FREEZE VERBOSE game_battle":              "game_battle",
		"ANALYZE public.game_battle":                          "public.game_battle",
		// A whole-database VACUUM names no relation.
		"VACUUM": "",
		// LOCK: the TABLE noise word and ONLY are optional; the mode clause follows.
		"LOCK TABLE public.battle IN SHARE UPDATE EXCLUSIVE MODE": "public.battle",
		"LOCK TABLE ONLY battle IN ACCESS EXCLUSIVE MODE NOWAIT":  "battle",
		"LOCK battle": "battle",
	}
	for q, want := range cases {
		if got := MainTable(q); got != want {
			t.Errorf("MainTable(%q) = %q, want %q", q, got, want)
		}
	}
}

func TestMainIndex(t *testing.T) {
	cases := map[string]string{
		"DROP INDEX IF EXISTS player_search__nickname__idx_old":              "player_search__nickname__idx_old",
		"DROP INDEX CONCURRENTLY public.idx_a":                               "public.idx_a",
		"ALTER INDEX IF EXISTS player_search__nickname__idx RENAME TO x_old": "player_search__nickname__idx",
		"REINDEX (VERBOSE) INDEX CONCURRENTLY idx_b":                         "idx_b",
		"CREATE INDEX idx ON t (a)":                                          "",
		"DROP TABLE t":                                                       "",
		"SELECT 1 FROM t":                                                    "",
	}
	for q, want := range cases {
		if got := MainIndex(q); got != want {
			t.Errorf("MainIndex(%q) = %q, want %q", q, got, want)
		}
	}
}

func TestJoinedTables(t *testing.T) {
	cases := map[string][]string{
		// Explicit JOINs — every spelling reaches the same keyword.
		"SELECT a FROM public.game_battle b JOIN other o ON o.id = b.id":               {"other"},
		"SELECT e.id FROM game_citymap_entity e JOIN states st ON st.entity_id = e.id": {"states"},
		"SELECT 1 FROM a INNER JOIN b ON a.id=b.id LEFT OUTER JOIN c ON c.id=a.id":     {"b", "c"},
		"SELECT 1 FROM a NATURAL JOIN b":                                               {"b"},
		// Schema qualification survives (to_regclass resolves it as-is); quoting
		// does not, exactly as MainTable does it.
		"SELECT 1 FROM public.a JOIN other.b ON a.id=b.id":           {"other.b"},
		`SELECT 1 FROM "A" JOIN "MySchema"."Tbl" x ON x.id = "A".id`: {"MySchema.Tbl"},
		// Comma FROM lists — the whole reason sqlWords emits ",". A bare word after
		// a relation is its alias, not a second table.
		"SELECT 1 FROM a, b WHERE a.id = b.id":           {"b"},
		"SELECT 1 FROM a x, b y, c z WHERE x.id = $1":    {"b", "c"},
		"SELECT 1 FROM a b WHERE b.id = $1":              nil,
		"SELECT 1 FROM a AS x, public.b AS y ON true":    {"public.b"},
		"SELECT 1 FROM a, b JOIN c ON b.id = c.id":       {"b", "c"},
		"SELECT 1 FROM ONLY a, ONLY b WHERE a.id = b.id": {"b"},
		// The DML join shapes: the second relation is introduced by FROM or USING,
		// never by the statement keyword.
		"UPDATE game_player p SET x = $1 FROM game_session s WHERE s.player_id = p.id":           {"game_session"},
		"DELETE FROM game_session s USING game_player p WHERE s.player_id = p.id":                {"game_player"},
		"MERGE INTO inventory t USING src s ON t.id = s.id WHEN MATCHED THEN UPDATE SET n = s.n": {"src"},
		"INSERT INTO archive SELECT * FROM staging s JOIN players p ON p.id = s.id":              {"staging", "players"},
		// An INSERT column list and a VALUES tuple are expressions, not relations.
		"INSERT INTO game_event (a, b) VALUES ($1, $2)": nil,
		// USING after JOIN is a column list; only a DELETE/MERGE USING names a table.
		"SELECT 1 FROM a JOIN b USING (id, tenant_id)": {"b"},
		// Subqueries anywhere: a semi-join probe reads a table just as a JOIN does.
		"SELECT 1 FROM a WHERE EXISTS (SELECT 1 FROM b WHERE b.a_id = a.id)":             {"b"},
		"SELECT 1 FROM a WHERE id IN (SELECT id FROM b WHERE x IN (SELECT y FROM c))":    {"b", "c"},
		"SELECT x, (SELECT max(id) FROM b) FROM a":                                       {"b"},
		"SELECT 1 FROM a JOIN (SELECT id FROM b JOIN c ON c.id = b.id) s ON s.id = a.id": {"b", "c"},
		// The group isn't a subquery itself, so it has to be walked as an
		// expression rather than skipped, or the buried SELECT is lost.
		"SELECT coalesce((SELECT max(id) FROM b), $1) FROM a": {"b"},
		// Every UNION arm is its own statement body, parenthesized or not.
		"SELECT a FROM t1 UNION ALL SELECT a FROM t2":                       {"t2"},
		"(SELECT a FROM t1) UNION (SELECT a FROM t2)":                       {"t1", "t2"},
		"SELECT 1 FROM a, LATERAL (SELECT id FROM b WHERE b.a_id = a.id) s": {"b"},
		// FROM-position keywords that are not relations: the operand of
		// extract/substring is a column, and an SRF in FROM is a call.
		"SELECT extract(epoch from created_at) FROM events JOIN e2 ON e2.id = events.id":           {"e2"},
		"SELECT substring(x FROM $1 FOR $2) FROM t, u":                                             {"u"},
		"SELECT wanted.id FROM unnest($1::int[]) AS wanted(id) JOIN players p ON p.id = wanted.id": {"players"},
		"SELECT 1 FROM generate_series($1, $2) g, events e WHERE e.id = g":                         {"events"},
		// CTE bodies are walked; the CTE names themselves are not relations.
		"WITH d AS (SELECT id FROM tmp) DELETE FROM events WHERE id IN (SELECT id FROM d)":                              {"tmp"},
		"WITH a AS (SELECT id FROM real_t), b AS (SELECT id FROM a) SELECT * FROM b":                                    nil,
		"WITH x AS (SELECT id FROM a JOIN b ON b.id=a.id), y AS (SELECT id FROM c) SELECT * FROM x JOIN y ON y.id=x.id": {"b", "c"},
		// A data-modifying CTE body must still be recognised as a query body.
		"WITH moved AS (DELETE FROM staging WHERE id = $1 RETURNING *) INSERT INTO archive SELECT * FROM moved": {"staging"},
		// A CTE column list is not a relation list, and a VALUES body has no tables.
		"WITH units_to_delete (player_id, unit_id, count) AS (VALUES ($1,$2,$3)) UPDATE game_army_units u SET count = u.count - d.count FROM units_to_delete d WHERE u.player_id = d.player_id": nil,
		// A RECURSIVE self-reference can't loop: the walk is structural and never
		// follows a name (unlike MainTable's resolveThroughCTEs).
		"WITH RECURSIVE t AS (SELECT $1 UNION ALL SELECT n FROM t JOIN edges e ON e.src = t.n) SELECT * FROM t": {"edges"},
		// Dedup is case-insensitive and keeps first appearance, not sort order; a
		// self-join adds nothing the table row doesn't already say.
		"SELECT 1 FROM a x JOIN a y ON y.parent = x.id":           nil,
		"SELECT 1 FROM a JOIN B ON B.id=a.id JOIN b ON b.id=a.id": {"B"},
		"SELECT 1 FROM a JOIN c ON c.id=a.id JOIN b ON b.id=a.id": {"c", "b"},
		// Gated out: no join list to find, and their USING/ON operands are indexes,
		// access methods and files rather than tables.
		"CREATE INDEX idx ON t USING btree (a)":        nil,
		"CLUSTER VERBOSE player USING player_pkey":     nil,
		"VACUUM (VERBOSE, ANALYZE) public.game_battle": nil,
		"TRUNCATE a, b": nil,
		"LOCK TABLE public.battle IN SHARE UPDATE EXCLUSIVE MODE": nil,
		"COPY t FROM stdin":                             nil,
		"autovacuum: VACUUM ANALYZE public.game_battle": nil,
		"SET search_path = $1":                          nil,
		"SELECT 1":                                      nil,
		"":                                              nil,
		// Leading ORM tags are stripped before the statement gate, like MainTable.
		"/* Repo.find */ SELECT 1 FROM a JOIN b ON b.id = a.id": {"b"},
	}
	for q, want := range cases {
		if got := JoinedTables(q); !slices.Equal(got, want) {
			t.Errorf("JoinedTables(%q) = %q, want %q", q, got, want)
		}
	}
}
