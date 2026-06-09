package pg

import "testing"

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
		"SET search_path = $1":                 "",
		"":                                     "",
		// Leading ORM comments must be skipped, not parsed as the statement.
		"/* TechnologyRepository.findAllByPlayerId */ SELECT * FROM technology WHERE id = $1": "technology",
		"/* update for com.example.Battle */update battle set modified = $1 where id = $2":    "battle",
		"-- a note\nUPDATE worker SET x = $1":                                                 "worker",
		"/* multi\nline\ncomment */ SELECT * FROM hero":                                       "hero",
		"/* outer /* nested */ still comment */ DELETE FROM session WHERE id = $1":            "session",
	}
	for q, want := range cases {
		if got := MainTable(q); got != want {
			t.Errorf("MainTable(%q) = %q, want %q", q, got, want)
		}
	}
}
