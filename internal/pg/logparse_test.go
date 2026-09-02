package pg

import (
	"os"
	"strings"
	"testing"
	"time"
)

// parseStderr is the test shorthand: Debian prefix, one Feed, classified.
func parseStderr(t *testing.T, text string) []LogEntry {
	t.Helper()
	m, err := CompilePrefix(debianPrefix, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	p := NewLogParser(LogFormatStderr, m, time.UTC)
	p.Feed([]byte(text), 0)
	es := p.Entries()
	Classify(es)
	return es
}

const sampleErrorBlock = `2026-09-02 00:15:25 UTC [872628-1] herocity0@2a00:1f78:fffd:4301::1221 ERROR:  duplicate key value violates unique constraint "channel_name_plugin_idx"
2026-09-02 00:15:25 UTC [872628-2] herocity0@2a00:1f78:fffd:4301::1221 DETAIL:  Key (name, plugin)=(player-to-player-723188-1448882, chat) already exists.
2026-09-02 00:15:25 UTC [872628-3] herocity0@2a00:1f78:fffd:4301::1221 STATEMENT:  INSERT INTO channel(plugin, name, ephemeral) VALUES ($1, $2, $3) RETURNING id
`

func TestParseErrorWithDetailAndStatement(t *testing.T) {
	es := parseStderr(t, sampleErrorBlock)
	if len(es) != 1 {
		t.Fatalf("got %d entries, want 1", len(es))
	}
	e := es[0]
	if e.Severity != SevError || e.Category != CatError {
		t.Errorf("sev/cat = %v/%v", e.Severity, e.Category)
	}
	if !strings.HasPrefix(string(e.Detail), "Key (name, plugin)=") {
		t.Errorf("detail = %q", e.Detail)
	}
	if !strings.HasPrefix(string(e.Statement), "INSERT INTO channel") {
		t.Errorf("statement = %q", e.Statement)
	}
}

func TestParseSlowQueryWithParameters(t *testing.T) {
	es := parseStderr(t, `2026-09-02 00:16:09 UTC [872039-3] herocity0@2a00:1f78:fffd:4301::1220 LOG:  duration: 248.569 ms  execute <unnamed>: DELETE FROM event_log WHERE event_log.id IN (SELECT tmp.id FROM x)
2026-09-02 00:16:09 UTC [872039-4] herocity0@2a00:1f78:fffd:4301::1220 DETAIL:  Parameters: $1 = '1000', $2 = '1000'
`)
	if len(es) != 1 {
		t.Fatalf("got %d entries, want 1", len(es))
	}
	e := es[0]
	if e.Category != CatSlowQuery || e.DurationMs != 248.569 {
		t.Errorf("cat=%v dur=%v", e.Category, e.DurationMs)
	}
	if !strings.HasPrefix(string(e.SQL), "DELETE FROM event_log") {
		t.Errorf("sql = %q", e.SQL)
	}
	if !strings.HasPrefix(string(e.Detail), "Parameters:") {
		t.Errorf("detail = %q", e.Detail)
	}
}

func TestParseMultiLineSQL(t *testing.T) {
	es := parseStderr(t, "2026-09-02 00:18:30 UTC [873148-1] herocity0@2a00:1f78:fffd:4301::121d LOG:  duration: 137.034 ms  execute <unnamed>/C_15795: /* BattleRepository.deleteUnreferencedPvpBattles */ DELETE FROM battle b\n"+
		"\t\tWHERE b.id = ANY($1)\n"+
		"\t\t\tAND NOT EXISTS (\n"+
		"\t\t\t\tSELECT 1 FROM pvp_participation p\n"+
		"\t\t\t)\n"+
		"\t\tRETURNING b.id\n"+
		"\t\n"+
		"2026-09-02 00:18:30 UTC [873148-2] herocity0@2a00:1f78:fffd:4301::121d DETAIL:  Parameters: $1 = '{1,2}'\n")
	if len(es) != 1 {
		t.Fatalf("got %d entries, want 1", len(es))
	}
	e := es[0]
	if !strings.Contains(string(e.SQL), "RETURNING b.id") || !strings.HasPrefix(string(e.SQL), "/* BattleRepository") {
		t.Errorf("sql = %q", e.SQL)
	}
	if strings.Contains(string(e.SQL), "Parameters") {
		t.Error("DETAIL leaked into the SQL")
	}
	if e.DurationMs != 137.034 {
		t.Errorf("dur = %v", e.DurationMs)
	}
}

func TestParseTempFileWithContext(t *testing.T) {
	es := parseStderr(t, "2026-09-02 02:22:52 UTC [3256335-843] LOG:  temporary file: path \"base/pgsql_tmp/pgsql_tmp3256335.420\", size 16408284\n"+
		"2026-09-02 02:22:52 UTC [3256335-844] CONTEXT:  SQL statement \"\n"+
		"\t            -- the various background processes report wait events\n"+
		"\t            SELECT now(), COALESCE(pgss.dbid, 0) AS dbid\n"+
		"\t            FROM public.pg_wait_sampling_profile s\"\n")
	if len(es) != 1 {
		t.Fatalf("got %d entries, want 1", len(es))
	}
	e := es[0]
	if e.Category != CatTempFile || e.TempBytes != 16408284 {
		t.Errorf("cat=%v bytes=%d", e.Category, e.TempBytes)
	}
	if !strings.Contains(string(e.Context), "pg_wait_sampling_profile") || strings.Count(string(e.Context), "\n") != 3 {
		t.Errorf("context = %q", e.Context)
	}
	if key, title := Fingerprint(&e); !strings.HasPrefix(key, "tmp|SELECT now()") || !strings.Contains(title, "temporary file ·") {
		t.Errorf("fingerprint = %q / %q", key, title)
	}
}

func TestParseEmptyStatementFirstLineAndOtherPid(t *testing.T) {
	es := parseStderr(t, "2026-09-02 06:43:56 UTC [1161124-6] matze@[local] ERROR:  column \"buffers_backend\" does not exist at character 18\n"+
		"2026-09-02 06:43:56 UTC [1161124-7] matze@[local] STATEMENT:  \n"+
		"\t\tSELECT buffers_backend\n"+
		"\t\tFROM pg_stat_bgwriter\n"+
		"2026-09-02 06:43:56 UTC [1165189-1] matze@[local] FATAL:  database \"pgbouncer\" does not exist\n")
	if len(es) != 2 {
		t.Fatalf("got %d entries, want 2", len(es))
	}
	if !strings.Contains(string(es[0].Statement), "SELECT buffers_backend") {
		t.Errorf("statement = %q", es[0].Statement)
	}
	if es[1].Severity != SevFatal || len(es[1].Statement) != 0 || es[1].PID != 1165189 {
		t.Errorf("second entry: %+v", es[1])
	}
}

func TestParseOrphanDetail(t *testing.T) {
	es := parseStderr(t, "2026-09-02 00:15:25 UTC [872628-2] herocity0@h DETAIL:  Key (name)=(x) already exists.\n"+
		"2026-09-02 00:15:25 UTC [872628-3] herocity0@h STATEMENT:  INSERT INTO channel VALUES (1)\n")
	if len(es) != 1 || !es[0].Orphan || es[0].Category != CatOther {
		t.Fatalf("entries = %+v", es)
	}
	if !strings.HasPrefix(string(es[0].Detail), "Key (name)") || !strings.HasPrefix(string(es[0].Statement), "INSERT") {
		t.Errorf("orphan fields: detail=%q statement=%q", es[0].Detail, es[0].Statement)
	}
}

func TestParseCheckpoint(t *testing.T) {
	es := parseStderr(t, "2026-09-02 00:17:05 UTC [3256329-604] LOG:  checkpoint complete: wrote 1271754 buffers (16.6%); 0 WAL file(s) added, 0 removed, 505 recycled; write=959.123 s, sync=0.080 s, total=959.437 s; sync files=967, longest=0.002 s, average=0.001 s; distance=8274796 kB, estimate=8274796 kB; lsn=3C4A7/20967C50, redo lsn=3C4A5/5A121328\n"+
		"2026-09-02 00:17:06 UTC [3256329-605] LOG:  checkpoint starting: wal\n")
	if len(es) != 2 {
		t.Fatalf("got %d entries", len(es))
	}
	cf := es[0].Checkpoint
	if es[0].Category != CatCheckpoint || cf == nil {
		t.Fatalf("not a checkpoint: %+v", es[0])
	}
	if cf.Buffers != 1271754 || cf.BuffersPct != 16.6 || cf.WALRecycle != 505 || cf.WriteSec != 959.123 || cf.SyncSec != 0.080 || cf.TotalSec != 959.437 || cf.DistanceKB != 8274796 {
		t.Errorf("checkpoint fields = %+v", *cf)
	}
	if !es[1].Checkpoint.Starting || es[1].Checkpoint.Reason != "wal" {
		t.Errorf("starting = %+v", *es[1].Checkpoint)
	}
	k0, _ := Fingerprint(&es[0])
	k1, _ := Fingerprint(&es[1])
	if k0 != "ckpt|checkpoint|complete" || k1 != "ckpt|checkpoint|starting|wal" {
		t.Errorf("keys = %q %q", k0, k1)
	}
}

func TestClassifyCanonicalLines(t *testing.T) {
	cases := []struct {
		line string
		cat  LogCategory
	}{
		{`LOG:  automatic vacuum of table "shop.public.orders": index scans: 1`, CatAutovacuum},
		{`LOG:  automatic analyze of table "shop.public.orders"`, CatAutovacuum},
		{`LOG:  process 1234 still waiting for ShareLock on transaction 5678 after 1000.123 ms`, CatLock},
		{`LOG:  process 1234 acquired ShareLock on transaction 5678 after 1200.000 ms`, CatLock},
		{`ERROR:  deadlock detected`, CatLock},
		{`LOG:  connection authorized: user=app database=shop`, CatConnection},
		{`LOG:  disconnection: session time: 0:00:01.234 user=app database=shop host=[local]`, CatConnection},
		{`LOG:  started streaming WAL from primary at 3C4/A0000000 on timeline 3`, CatReplication},
		{`WARNING:  there is no transaction in progress`, CatWarning},
		{`FATAL:  terminating connection due to administrator command`, CatError},
		{`LOG:  duration: 12.000 ms`, CatSlowQuery},
		{`LOG:  statement: SELECT 1`, CatStatement},
		{`LOG:  execute <unnamed>: CREATE SEQUENCE IF NOT EXISTS s AS integer`, CatStatement},
		{`LOG:  execute S_1/C_2: UPDATE t SET a = $1`, CatStatement},
		{`LOG:  something entirely different`, CatOther},
	}
	for _, c := range cases {
		es := parseStderr(t, "2026-09-02 00:00:00 UTC [1-1] app@h "+c.line+"\n")
		if len(es) != 1 {
			t.Fatalf("%q: %d entries", c.line, len(es))
		}
		if es[0].Category != c.cat {
			t.Errorf("%q: category %v, want %v", c.line, es[0].Category, c.cat)
		}
	}
	es := parseStderr(t, "2026-09-02 00:00:00 UTC [1-1] app@h LOG:  automatic vacuum of table \"shop.public.orders\": index scans: 1\n")
	if string(es[0].AVTable) != "shop.public.orders" {
		t.Errorf("AVTable = %q", es[0].AVTable)
	}
	es = parseStderr(t, "2026-09-02 00:00:00 UTC [1-1] app@h LOG:  process 1234 still waiting for ShareLock on transaction 5678 after 1000.123 ms\n")
	if es[0].LockWaitMs != 1000.123 {
		t.Errorf("LockWaitMs = %v", es[0].LockWaitMs)
	}
	es = parseStderr(t, "2026-09-02 00:00:00 UTC [1-1] app@h LOG:  duration: 12.000 ms\n")
	if es[0].DurationMs != 12 || len(es[0].SQL) != 0 {
		t.Errorf("bare duration: %v %q", es[0].DurationMs, es[0].SQL)
	}
	for line, want := range map[string]string{
		"statement: ALTER SEQUENCE s AS bigint;": "ALTER SEQUENCE s AS bigint;",
		"execute <unnamed>: SELECT nextval('s')": "SELECT nextval('s')",
		"execute S_1/C_2: UPDATE t SET a = $1":   "UPDATE t SET a = $1",
	} {
		es = parseStderr(t, "2026-09-02 00:00:00 UTC [1-1] app@h LOG:  "+line+"\n")
		if string(es[0].SQL) != want || es[0].DurationMs != 0 {
			t.Errorf("%q: SQL %q dur %v", line, es[0].SQL, es[0].DurationMs)
		}
	}
}

func TestParserResumeAcrossFeeds(t *testing.T) {
	m, _ := CompilePrefix(debianPrefix, time.UTC)
	p := NewLogParser(LogFormatStderr, m, time.UTC)
	first := "2026-09-02 00:18:30 UTC [1-1] u@h LOG:  duration: 1.000 ms  statement: SELECT 1\n" +
		"2026-09-02 00:18:31 UTC [2-1] u@h ERROR:  boom\n"
	p.Feed([]byte(first), 0)
	if p.LastEntryOff() != int64(len(first)-len("2026-09-02 00:18:31 UTC [2-1] u@h ERROR:  boom\n")) {
		t.Fatalf("LastEntryOff = %d", p.LastEntryOff())
	}
	// Refresh: the last entry is re-fed together with what arrived after it.
	off := p.LastEntryOff()
	p.TruncateLast()
	p.Feed([]byte("2026-09-02 00:18:31 UTC [2-1] u@h ERROR:  boom\n"+
		"2026-09-02 00:18:31 UTC [2-2] u@h DETAIL:  more\n"), off)
	es := p.Entries()
	if len(es) != 2 || string(es[1].Detail) != "more" || es[1].Off != off {
		t.Errorf("resumed entries = %+v", es)
	}
}

func TestParseCSVAndJSON(t *testing.T) {
	csvText := `2026-09-02 00:15:25.123 UTC,"app","shop",4242,"10.0.0.9:5000",68b6.1092,7,"INSERT",2026-09-02 00:15:20 UTC,3/12,0,ERROR,23505,"duplicate key value violates unique constraint ""channel_name_plugin_idx""","Key (name)=(x) already exists.",,,,,"INSERT INTO channel VALUES ($1)",,"_bt_check_unique, nbtinsert.c:664","psql","client backend",,0
`
	p := NewLogParser(LogFormatCSV, nil, time.UTC)
	p.Feed([]byte(csvText), 0)
	es := p.Entries()
	Classify(es)
	if len(es) != 1 {
		t.Fatalf("csv: %d entries", len(es))
	}
	e := es[0]
	if e.PID != 4242 || e.Line != 7 || string(e.User) != "app" || string(e.DB) != "shop" || e.Severity != SevError ||
		!strings.HasPrefix(string(e.Message), "duplicate key") || !strings.HasPrefix(string(e.Statement), "INSERT") || string(e.App) != "psql" {
		t.Errorf("csv entry = %+v", e)
	}

	jsonText := `{"timestamp":"2026-09-02 00:15:25.123 UTC","user":"app","dbname":"shop","pid":4242,"remote_host":"10.0.0.9","session_id":"68b6.1092","line_num":7,"error_severity":"LOG","message":"duration: 12.5 ms  statement: SELECT 1","application_name":"psql","backend_type":"client backend"}
`
	p = NewLogParser(LogFormatJSON, nil, time.UTC)
	p.Feed([]byte(jsonText), 0)
	es = p.Entries()
	Classify(es)
	if len(es) != 1 || es[0].Category != CatSlowQuery || es[0].DurationMs != 12.5 || string(es[0].SQL) != "SELECT 1" || es[0].PID != 4242 {
		t.Errorf("json entry = %+v", es)
	}
}

// TestParseSampleLog validates against the production log checked out next to
// the repo when it is present (it is not committed).
func TestParseSampleLog(t *testing.T) {
	const path = "../../postgresql-17-main.log"
	if _, err := os.Stat(path); err != nil {
		t.Skip("sample log not present")
	}
	r, err := LoadLog(t.Context(), OpenLocalLog(path), "", time.UTC, 0, AggOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.PrefixDetected != true || r.Prefix != debianPrefix {
		t.Errorf("prefix = %q detected=%v", r.Prefix, r.PrefixDetected)
	}
	if r.BySeverity[SevError] != 21 || r.BySeverity[SevFatal] != 2 || r.BySeverity[SevLog] != 1821 {
		t.Errorf("severity counts = %v", r.BySeverity)
	}
	if r.Unparsed != 0 {
		t.Errorf("unparsed = %d", r.Unparsed)
	}
	orphans := 0
	for i := range r.Entries {
		if r.Entries[i].Orphan {
			orphans++
		}
	}
	if orphans != 0 {
		t.Errorf("orphans = %d", orphans)
	}
	want := map[string]int{
		`duplicate key value violates unique constraint "channel_name_plugin_idx"`: 13,
		`column "buffers_backend" does not exist`:                                  8,
		`database "pgbouncer" does not exist`:                                      2,
	}
	for _, g := range r.Groups {
		if n, ok := want[g.Title]; ok {
			if g.Count != n {
				t.Errorf("group %q: count %d, want %d", g.Title, g.Count, n)
			}
			delete(want, g.Title)
		}
	}
	for title := range want {
		t.Errorf("group %q missing", title)
	}
	if r.ByCategory[CatCheckpoint] != 44 || r.ByCategory[CatTempFile] != 70 {
		t.Errorf("category counts = %v", r.ByCategory)
	}
	if r.Hist.Bucket != 5*time.Minute {
		t.Errorf("hist bucket = %v for a %v span", r.Hist.Bucket, r.Window.To.Sub(r.Window.From))
	}
}

func TestParseAutoExplainPlans(t *testing.T) {
	m, _ := CompilePrefix(debianPrefix, time.UTC)
	p := NewLogParser(LogFormatStderr, m, time.UTC)
	p.Feed([]byte("2026-09-02 01:14:42 UTC [3273675-1] foe00@[local] LOG:  duration: 912.432 ms  plan:\n"+
		"        Query Text: DELETE FROM game_player_social_interactions WHERE received_time < 1787879681 \n"+
		"        Delete on game_player_social_interactions  (cost=0.43..45304.05 rows=0 width=0) (actual rows=0 loops=1)\n"+
		"          Buffers: shared hit=1180595 read=1408 dirtied=6556\n"+
		"          ->  Index Scan using idx on game_player_social_interactions  (cost=0.43..45304.05 rows=684409 width=6) (actual rows=667989 loops=1)\n"+
		"2026-09-02 01:14:42 UTC [3273675-2] foe00@[local] LOG:  duration: 912.775 ms  statement: DELETE FROM game_player_social_interactions WHERE received_time < 1787879681 \n"+
		"2026-09-02 01:09:24 UTC [1761596-899] LOG:  duration: 298.779 ms  plan:\n"+
		"        Query Text: SET search_path TO pg_catalog;SELECT public.powa_take_snapshot()\n"+
		"        Result  (cost=0.00..0.26 rows=1 width=4) (actual rows=1 loops=1)\n"+
		"          Buffers: shared hit=181564 read=8 dirtied=437 written=416\n"+
		"2026-09-02 01:09:24 UTC [1761596-900] CONTEXT:  SQL statement \"SET search_path TO pg_catalog;SELECT public.powa_take_snapshot()\"\n"), 0)
	es := p.Entries()
	Classify(es)
	if len(es) != 3 {
		t.Fatalf("before merge: %d entries", len(es))
	}
	plan := es[0]
	if plan.Category != CatSlowQuery || string(plan.SQL) != "DELETE FROM game_player_social_interactions WHERE received_time < 1787879681" ||
		!strings.HasPrefix(string(plan.Plan), "Delete on game_player_social_interactions") || !strings.Contains(string(plan.Plan), "Index Scan") {
		t.Errorf("plan entry: sql=%q plan=%q", plan.SQL, plan.Plan)
	}
	es = MergePlans(es)
	if len(es) != 2 {
		t.Fatalf("after merge: %d entries", len(es))
	}
	stmt := es[0]
	if stmt.DurationMs != 912.775 || len(stmt.Plan) == 0 || !strings.HasPrefix(string(stmt.Plan), "Delete on") {
		t.Errorf("merged statement: dur=%v plan=%q", stmt.DurationMs, stmt.Plan)
	}
	// The powa plan has no statement line: it stays, grouped by its query text.
	powa := es[1]
	if string(powa.SQL) != "SET search_path TO pg_catalog;SELECT public.powa_take_snapshot()" || len(powa.Plan) == 0 || !strings.HasPrefix(string(powa.Context), "SQL statement") {
		t.Errorf("standalone plan: sql=%q plan=%q ctx=%q", powa.SQL, powa.Plan, powa.Context)
	}
	if key, _ := Fingerprint(&powa); !strings.HasPrefix(key, "slow|SET search_path") {
		t.Errorf("standalone plan key = %q", key)
	}

	r := &LogReport{Entries: es}
	Aggregate(r, AggOptions{})
	for _, g := range r.Groups {
		if g.Plans != 1 {
			t.Errorf("group %q Plans = %d", g.Title, g.Plans)
		}
	}
}
