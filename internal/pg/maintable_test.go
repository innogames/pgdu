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
		// Quoted / schema-qualified identifiers survive intact.
		`SELECT 1 FROM "MixedCase"`:        `"MixedCase"`,
		"SELECT 1 FROM myschema.\"Tbl\" x": `myschema."Tbl"`,
		// No useful table to point at.
		"VALUES ($1), ($2)":                    "",
		"SELECT 1":                             "",
		"SELECT * FROM (SELECT 1) sub":         "",
		"WITH c AS (SELECT 1) SELECT * FROM c": "c",
		"SET search_path = $1":                 "",
		"":                                     "",
	}
	for q, want := range cases {
		if got := MainTable(q); got != want {
			t.Errorf("MainTable(%q) = %q, want %q", q, got, want)
		}
	}
}
