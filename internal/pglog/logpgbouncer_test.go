package pglog

import (
	"context"
	"strings"
	"testing"
	"time"
)

const samplePgBouncerLog = `2026-09-03 08:00:20.374 UTC [1396084] LOG stats: 921 xacts/s, 1992 queries/s, 0 client parses/s, 0 server parses/s, 0 binds/s, in 249055 B/s, out 4230871 B/s, xact 13340 us, query 191 us, wait 0 us
2026-09-03 08:00:21.001 UTC [1396084] LOG C-0x55d1c0a2b3e0: shop/app@10.1.2.3:53412 login attempt: db=shop user=app tls=no replication=no
2026-09-03 08:00:21.002 UTC [1396084] LOG S-0x55d1c0b0c0d0: shop/app@10.0.0.5:5432 new connection to server (from 10.1.2.3:41234)
2026-09-03 08:00:22.000 UTC [1396084] WARNING C-0x55d1c0a2b3e0: shop/app@10.1.2.3:53412 pooler error: query_wait_timeout
2026-09-03 08:00:22.500 UTC [1396084] ERROR S-0x55d1c0b0c0d0: shop/app@10.0.0.5:5432 could not connect to server: Connection refused
2026-09-03 08:00:23.000 UTC [1396084] LOG C-0x55d1c0a2b3e0: shop/app@10.1.2.3:53412 closing because: client close request (age=12s)
2026-09-03 08:00:24.000 UTC [1396084] LOG C-0x55d1c0a2b3f0: shop/app@10.1.2.4:53499 closing because: client close request (age=3s)
2026-09-03 08:01:20.370 UTC [1396084] NOISE safe_evict: 0 buffers
2026-09-03 08:01:20.374 UTC [1396084] LOG stats: 925 xacts/s, 1951 queries/s, 0 client parses/s, 0 server parses/s, 0 binds/s, in 238948 B/s, out 3929869 B/s, xact 13259 us, query 188 us, wait 0 us
`

type memSource struct{ data []byte }

func (s *memSource) Info() SourceInfo {
	return SourceInfo{Kind: "local", Path: "/var/log/postgresql/pgbouncer_1.log", Size: int64(len(s.data))}
}

func (s *memSource) ReadTail(_ context.Context, n int64) ([]byte, Window, error) {
	return s.data, Window{Requested: n, FileSize: int64(len(s.data)), Bytes: int64(len(s.data))}, nil
}
func (s *memSource) ReadFrom(context.Context, int64) ([]byte, error) { return nil, ErrNotIncremental }
func (s *memSource) Cursor(context.Context) *Cursor                  { return nil }

func TestDetectPgBouncerFormat(t *testing.T) {
	if got := DetectFormat([]byte(samplePgBouncerLog)); got != FormatPgBouncer {
		t.Fatalf("DetectFormat = %v, want pgbouncer", got)
	}
	// A Postgres line with the colon must not be mistaken for pgbouncer.
	if got := DetectFormat([]byte("2026-09-03 08:00:20.374 UTC [1396084] LOG:  checkpoint starting: time\n")); got != FormatStderr {
		t.Errorf("Postgres %%m [%%p] line detected as %v", got)
	}
}

func TestLoadPgBouncerLog(t *testing.T) {
	r, err := Load(t.Context(), &memSource{data: []byte(samplePgBouncerLog)}, "%t [%p-%l] %q%u@%h ", time.UTC, 0, AggOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Format != FormatPgBouncer || r.Prefix != "pgbouncer" || r.PrefixDetected {
		t.Errorf("format %v prefix %q detected %v", r.Format, r.Prefix, r.PrefixDetected)
	}
	if len(r.Entries) != 9 || r.Unparsed != 0 {
		t.Fatalf("entries %d unparsed %d, want 9/0", len(r.Entries), r.Unparsed)
	}
	if r.Window.Bytes != int64(len(samplePgBouncerLog)) {
		t.Errorf("window shrank to %d bytes: skipContinuation dropped pgbouncer lines", r.Window.Bytes)
	}
	e := r.Entries[0]
	if e.PID != 1396084 || e.Severity != SevLog || e.Time.Format("15:04:05") != "08:00:20" || e.Category != CatOther {
		t.Errorf("stats line: pid %d sev %v time %s cat %v", e.PID, e.Severity, e.Time, e.Category)
	}
	if !strings.HasPrefix(string(e.Message), "stats: 921 xacts/s") {
		t.Errorf("message = %q", e.Message)
	}
	wantCat := []Category{CatOther, CatConnection, CatConnection, CatWarning, CatError, CatConnection, CatConnection, CatOther, CatOther}
	wantSev := []Severity{SevLog, SevLog, SevLog, SevWarning, SevError, SevLog, SevLog, SevDebug, SevLog}
	for i, e := range r.Entries {
		if e.Category != wantCat[i] {
			t.Errorf("entry %d %q: category %v, want %v", i, e.FirstLine(), e.Category, wantCat[i])
		}
		if e.Severity != wantSev[i] {
			t.Errorf("entry %d: severity %v, want %v", i, e.Severity, wantSev[i])
		}
	}
	// Grouping: the two stats lines collapse, the two "closing because" lines
	// collapse (different sockets, addresses and ages), login and new-connection
	// are separate client/server groups.
	titles := map[string]int{}
	for _, g := range r.Groups {
		titles[g.Title] = g.Count
	}
	if titles["stats: periodic pooler statistics (tab → pooler stats pane)"] != 2 {
		t.Errorf("stats lines not grouped: %v", titles)
	}
	if r.PoolerStats != 2 {
		t.Errorf("PoolerStats = %d, want 2", r.PoolerStats)
	}
	st := r.Entries[0].PoolerStats
	want := PgBouncerStats{XactsPerSec: 921, QueriesPerSec: 1992, InBytesPerSec: 249055, OutBytesPerSec: 4230871, XactUs: 13340, QueryUs: 191}
	if st == nil || *st != want {
		t.Errorf("stats = %+v, want %+v", st, want)
	}
	if r.Entries[1].PoolerStats != nil {
		t.Error("socket line carries PoolerStats")
	}
	// Two stats lines a minute apart land in two 1-minute buckets; the
	// series carries each line's own figure (a single-line bucket averages to itself).
	if len(r.Pooler.Counts) != len(r.Hist.Counts) || r.Hist.Bucket != time.Minute {
		t.Fatalf("pooler hist %d buckets vs %d (bucket %s)", len(r.Pooler.Counts), len(r.Hist.Counts), r.Hist.Bucket)
	}
	if q := r.Pooler.Series(PoolerQueries); q[0] != 1992 || q[len(q)-1] != 1951 {
		t.Errorf("queries series = %v", q)
	}
	if titles["client closing because: client close request (age=Ns)"] != 2 {
		t.Errorf("closing lines not grouped: %v", titles)
	}
	if titles["server new connection to server (from N:N)"] != 1 {
		t.Errorf("server connection group missing: %v", titles)
	}
	for title := range titles {
		if strings.Contains(title, "0x") {
			t.Errorf("socket pointer leaked into a group title: %q", title)
		}
	}
}

func TestParsePgBouncerStats(t *testing.T) {
	// pre-1.18 shape: no parses/binds fields.
	e := Entry{Severity: SevLog, Message: []byte("stats: 90 xacts/s, 1357 queries/s, in 399055 B/s, out 1253187 B/s, xact 17861 us, query 242 us, wait 3 us")}
	classify(&e)
	want := PgBouncerStats{XactsPerSec: 90, QueriesPerSec: 1357, InBytesPerSec: 399055, OutBytesPerSec: 1253187, XactUs: 17861, QueryUs: 242, WaitUs: 3}
	if e.Category != CatOther || e.PoolerStats == nil || *e.PoolerStats != want {
		t.Errorf("short stats line: cat %v stats %+v", e.Category, e.PoolerStats)
	}
	e = Entry{Severity: SevLog, Message: []byte("stats: 1 xacts/s, 2 queries/s, 3 client parses/s, 4 server parses/s, 5 binds/s, in 6 B/s, out 7 B/s, xact 8 us, query 9 us, wait 10 us")}
	classify(&e)
	want = PgBouncerStats{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if e.PoolerStats == nil || *e.PoolerStats != want {
		t.Errorf("full stats line: %+v", e.PoolerStats)
	}
	for _, bad := range []string{"stats: garbage", "stats: 90 xacts/s, in 1 B/s", "stats: x xacts/s, 2 queries/s"} {
		e = Entry{Severity: SevLog, Message: []byte(bad)}
		classify(&e)
		if e.PoolerStats != nil || e.Category != CatOther {
			t.Errorf("%q: stats %+v cat %v, want nil/other", bad, e.PoolerStats, e.Category)
		}
	}
}

func TestClassifyPgBouncerBeforeReplication(t *testing.T) {
	// "replication=no" contains the bare replication marker; the socket-tagged
	// shape must win.
	e := Entry{Severity: SevLog, Message: []byte("C-0x1: db/u@1.2.3.4:5 login attempt: db=db user=u tls=no replication=no")}
	classify(&e)
	if e.Category != CatConnection {
		t.Errorf("category %v, want connection", e.Category)
	}
	side, tail, ok := pgBouncerSocketTail(string(e.Message))
	if !ok || side != "client" || tail != "login attempt: db=db user=u tls=no replication=no" {
		t.Errorf("tail = %q %q %v", side, tail, ok)
	}
	if _, _, ok := pgBouncerSocketTail("connection authorized: user=x"); ok {
		t.Errorf("Postgres connection line mistaken for a pgbouncer socket line")
	}
}
