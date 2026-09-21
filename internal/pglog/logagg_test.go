package pglog

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeSQL(t *testing.T) {
	a := NormalizeSQL("/* BattleRepository.deleteByLocationType */ DELETE FROM battle\n\t\tWHERE id IN (1, 2, 3) AND state = 'ACTIVE' AND ts > NOW() - $1")
	b := NormalizeSQL("/* BattleRepository.deleteByLocationType */   DELETE FROM battle WHERE id IN (4,5) AND state = 'DONE' AND ts > NOW() - $1")
	if a != b {
		t.Errorf("IN-list / literal variants differ:\n%s\n%s", a, b)
	}
	if !strings.HasPrefix(a, "/* BattleRepository.deleteByLocationType */ DELETE") {
		t.Errorf("comment hint lost: %s", a)
	}
	if !strings.Contains(a, "IN ($?...)") || !strings.Contains(a, "state = $?") || !strings.Contains(a, "NOW() - $1") {
		t.Errorf("normalized = %s", a)
	}
	if got := NormalizeSQL("INSERT INTO channel(plugin, name, ephemeral) VALUES ($1, $2, $3), ($4, $5, $6) RETURNING id"); got != "INSERT INTO channel(plugin, name, ephemeral) VALUES (...) RETURNING id" {
		t.Errorf("values = %s", got)
	}
	if got := NormalizeSQL("SELECT E'it''s', $$dollar$$, 1.5e3, t1.col FROM t1 -- trailing\nWHERE x = 0x1"); got != "SELECT $?, $?, $?, t1.col FROM t1 WHERE x = $?x1" {
		t.Errorf("literals = %s", got)
	}
	if got := NormalizeSQL(`SELECT "Quoted Col" FROM "T"`); got != `SELECT "Quoted Col" FROM "T"` {
		t.Errorf("quoted identifiers changed: %s", got)
	}
}

// NormalizeCall matches a logged call to a pg_stat_statements row: pgss numbers
// inlined constants as further $n after the bound ones and keeps only the
// first-seen comment, so both $n and literals fold to $? and comments go.
func TestNormalizeCall(t *testing.T) {
	pgss := "/* Repo.find */ SELECT a FROM t WHERE x = $1 AND y = $2 AND id IN ($3, $4) LIMIT $5"
	logged := "/* Repo.findAll */ SELECT a\n\tFROM t WHERE x = $1 AND y = 'z' AND id IN (1, 2) LIMIT 10"
	want := "SELECT a FROM t WHERE x = $? AND y = $? AND id IN ($?...) LIMIT $?"
	if a, b := NormalizeCall(pgss), NormalizeCall(logged); a != b || a != want {
		t.Errorf("NormalizeCall differs:\n%s\n%s\nwant %s", a, b, want)
	}
	// PG18 squashes long lists to `IN ($1 /*, ... */)`; the comment must go
	// before the list fold so it folds like a logged `IN (1, 2, 3)`.
	if got := NormalizeCall("SELECT a FROM t WHERE id IN ($1 /*, ... */)"); got != "SELECT a FROM t WHERE id IN ($?...)" {
		t.Errorf("squashed list = %s", got)
	}
	// NULL / TRUE / FALSE are constants pg_stat_statements numbers as $n while
	// the log keeps the keyword; IS NOT NULL folds the same on both sides.
	pgss = "/* BattleRepository.cleanBattleLog */\nUPDATE battle\nSET\n  battle_log = $3\nWHERE id IN ( SELECT id FROM battle WHERE modified_at < NOW() - $1 AND battle_log IS NOT NULL AND state != $4 ORDER BY modified_at ASC LIMIT $2 )"
	logged = "/* BattleRepository.cleanBattleLog */\n\tUPDATE battle\n\tSET\n\t  battle_log = NULL\n\tWHERE id IN ( SELECT id FROM battle WHERE modified_at < NOW() - $1 AND battle_log IS NOT NULL AND state != 'ACTIVE' ORDER BY modified_at ASC LIMIT $2 )"
	if a, b := NormalizeCall(pgss), NormalizeCall(logged); a != b {
		t.Errorf("NULL constant differs:\n%s\n%s", a, b)
	}
	if a, b := NormalizeCall("SELECT * FROM t WHERE active = $1"), NormalizeCall("SELECT * FROM t WHERE active = TRUE"); a != b {
		t.Errorf("boolean constant differs:\n%s\n%s", a, b)
	}
	// Identifiers that merely contain the words are left alone.
	if got := NormalizeCall("SELECT nullable, is_true FROM t"); got != "SELECT nullable, is_true FROM t" {
		t.Errorf("identifiers folded: %s", got)
	}
	// The grouping normalizer is untouched: comment, $n and NULL kept.
	if got := NormalizeSQL("/* A */ SELECT $1, NULL"); got != "/* A */ SELECT $1, NULL" {
		t.Errorf("NormalizeSQL changed: %s", got)
	}
}

func TestNormalizeMessage(t *testing.T) {
	a := normalizeMessage(`column "buffers_backend" does not exist at character 18`)
	b := normalizeMessage(`column "buffers_backend" does not exist at character 42`)
	if a != b || a != `column "buffers_backend" does not exist` {
		t.Errorf("at character: %q / %q", a, b)
	}
	if normalizeMessage(`database "pgbouncer" does not exist`) == normalizeMessage(`database "foo" does not exist`) {
		t.Error("identifier-shaped names must stay distinct")
	}
	if got := normalizeMessage(`duplicate key value violates unique constraint "channel_name_plugin_idx"`); got != `duplicate key value violates unique constraint "channel_name_plugin_idx"` {
		t.Errorf("constraint name changed: %q", got)
	}
	if got := normalizeMessage(`Key (name, plugin)=(player-to-player-723188-1448882, chat) already exists.`); got != `Key (name, plugin)=(player-to-player-N-N, chat) already exists.` {
		t.Errorf("digits: %q", got)
	}
	if got := normalizeMessage(`invalid input syntax for type integer: "12 apples"`); got != `invalid input syntax for type integer: "?"` {
		t.Errorf("non-identifier quoted: %q", got)
	}
	if got := normalizeMessage(`could not connect to 'host1' after 30 seconds, lsn 3C4A7/20967C50, 0x1F`); got != `could not connect to '?' after N seconds, lsn N/N, N` {
		t.Errorf("literals: %q", got)
	}
	if got := normalizeMessage(`parameter $2 is 5`); got != `parameter $2 is N` {
		t.Errorf("placeholder: %q", got)
	}
}

func TestAggregate(t *testing.T) {
	base := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	mk := func(i int, sev Severity, cat Category, msg string) Entry {
		return Entry{Time: base.Add(time.Duration(i) * time.Hour), Severity: sev, Category: cat, Message: []byte(msg)}
	}
	r := &Report{}
	for i, d := range []float64{248.569, 260.446, 8337.081, 3139.568} {
		e := mk(i, SevLog, CatSlowQuery, "duration")
		e.DurationMs = d
		e.SQL = []byte("SELECT " + strings.Repeat("x", i)) // distinct texts …
		e.SQL = []byte("SELECT 1")                         // … folded to one key
		r.Entries = append(r.Entries, e)
	}
	for i := range 3 {
		r.Entries = append(r.Entries, mk(i, SevError, CatError, `database "pgbouncer" does not exist`))
	}
	r.Entries = append(r.Entries, mk(7, SevError, CatError, `column "x" does not exist at character 3`))
	for i := range 2 {
		e := mk(i, SevLog, CatCheckpoint, "checkpoint complete: wrote 1 buffers")
		e.Checkpoint = &CheckpointFields{WriteSec: 1000 + float64(i)*500, TotalSec: 1001, Buffers: 10}
		r.Entries = append(r.Entries, e)
	}
	Aggregate(r, AggOptions{MaxSamples: 2})

	if len(r.Groups) != 4 {
		t.Fatalf("groups = %d", len(r.Groups))
	}
	g := r.Groups[0]
	if g.Category != CatSlowQuery || g.Count != 4 || g.Slow == nil {
		t.Fatalf("first group = %+v", g)
	}
	if g.Slow.MaxMs != 8337.081 || g.Slow.SumMs < 11985 || g.Slow.SumMs > 11986 || g.Slow.P95Ms != 8337.081 {
		t.Errorf("slow stats = %+v", *g.Slow)
	}
	if len(g.Samples) != 2 || g.Samples[1] != 3 {
		t.Errorf("samples capped wrong: %v", g.Samples)
	}
	if r.Groups[1].Count != 3 || r.Groups[1].Severity != SevError {
		t.Errorf("second group = %+v", r.Groups[1])
	}
	var ck *Group
	for i := range r.Groups {
		if r.Groups[i].Category == CatCheckpoint {
			ck = &r.Groups[i]
		}
	}
	if ck == nil || ck.Checkpoint.Complete != 2 || ck.Checkpoint.SumWrite != 2500 {
		t.Errorf("checkpoint group = %+v", ck)
	}
	if r.BySeverity[SevError] != 4 || r.ByCategory[CatSlowQuery] != 4 || r.ByCategory[CatCheckpoint] != 2 {
		t.Errorf("counts: sev=%v cat=%v", r.BySeverity, r.ByCategory)
	}
	if r.Hist.Bucket != 5*time.Minute || len(r.Hist.Counts) != 7*12+1 {
		t.Errorf("hist = %v × %d for a 7h span", r.Hist.Bucket, len(r.Hist.Counts))
	}
	if r.Hist.Series(SevError)[0] != 1 || r.Hist.Total()[0] != 3 {
		t.Errorf("hist bucket 0: err=%d total=%d", r.Hist.Series(SevError)[0], r.Hist.Total()[0])
	}
}

func TestHistogramBucketChoice(t *testing.T) {
	cases := []struct {
		span time.Duration
		want time.Duration
	}{
		{30 * time.Minute, time.Minute},
		{7 * time.Hour, 5 * time.Minute},
		{24 * time.Hour, 15 * time.Minute},
		{5 * 24 * time.Hour, time.Hour},
		{60 * 24 * time.Hour, 24 * time.Hour},
	}
	base := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	for _, c := range cases {
		h := histogram([]Entry{{Time: base}, {Time: base.Add(c.span)}}, base, base.Add(c.span))
		if h.Bucket != c.want {
			t.Errorf("span %v: bucket %v, want %v", c.span, h.Bucket, c.want)
		}
		if len(h.Counts) > histMaxBuckets+1 {
			t.Errorf("span %v: %d buckets", c.span, len(h.Counts))
		}
	}
}
