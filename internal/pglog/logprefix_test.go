package pglog

import (
	"strings"
	"testing"
	"time"
)

const debianPrefix = "%t [%p-%l] %q%u@%h "

func TestCompilePrefixDebian(t *testing.T) {
	m, err := CompilePrefix(debianPrefix, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(`2026-09-02 00:15:25 UTC [872628-1] herocity0@2a00:1f78:fffd:4301::1221 ERROR:  duplicate key value violates unique constraint "channel_name_plugin_idx"`)
	f, ok := m.Match(line)
	if !ok {
		t.Fatalf("no match: %s", m.re)
	}
	if f.pid != 872628 || f.line != 1 {
		t.Errorf("pid/line = %d/%d", f.pid, f.line)
	}
	if string(f.user) != "herocity0" || string(f.host) != "2a00:1f78:fffd:4301::1221" {
		t.Errorf("user/host = %q/%q", f.user, f.host)
	}
	if string(f.tag) != "ERROR" || !strings.HasPrefix(string(f.rest), "duplicate key value") {
		t.Errorf("tag/rest = %q/%q", f.tag, f.rest)
	}
	if f.time.Format("15:04:05") != "00:15:25" {
		t.Errorf("time = %v", f.time)
	}

	// Session-less checkpointer line: the %q tail is absent.
	f, ok = m.Match([]byte(`2026-09-02 00:17:05 UTC [3256329-604] LOG:  checkpoint starting: wal`))
	if !ok || string(f.tag) != "LOG" || f.pid != 3256329 || f.line != 604 || len(f.user) != 0 {
		t.Errorf("checkpointer line: ok=%v tag=%q pid=%d user=%q", ok, f.tag, f.pid, f.user)
	}

	// Unix-socket host renders as [local].
	f, ok = m.Match([]byte(`2026-09-02 06:43:56 UTC [1165189-1] matze@[local] FATAL:  database "pgbouncer" does not exist`))
	if !ok || string(f.host) != "[local]" || string(f.user) != "matze" || string(f.tag) != "FATAL" {
		t.Errorf("[local] line: ok=%v user=%q host=%q tag=%q", ok, f.user, f.host, f.tag)
	}

	// Continuation lines never match.
	if _, ok := m.Match([]byte("\t\tWHERE b.id = ANY($1)")); ok {
		t.Error("tab-indented continuation matched as a primary line")
	}
	if _, ok := m.Match(nil); ok {
		t.Error("empty line matched")
	}
}

func TestCompilePrefixDefault(t *testing.T) {
	m, err := CompilePrefix("%m [%p] %q%u@%d ", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	f, ok := m.Match([]byte(`2026-09-02 00:15:25.123 UTC [872628] postgres@shop LOG:  x`))
	if !ok || f.pid != 872628 || string(f.user) != "postgres" || string(f.db) != "shop" || string(f.rest) != "x" {
		t.Errorf("ok=%v pid=%d user=%q db=%q rest=%q", ok, f.pid, f.user, f.db, f.rest)
	}
	if f.time.Nanosecond() != 123_000_000 {
		t.Errorf("ms not parsed: %v", f.time)
	}
	f, ok = m.Match([]byte(`2026-09-02 00:15:25.123 UTC [12] LOG:  no session`))
	if !ok || f.pid != 12 || len(f.user) != 0 {
		t.Errorf("session-less: ok=%v pid=%d user=%q", ok, f.pid, f.user)
	}
}

func TestCompilePrefixEscapes(t *testing.T) {
	// Every documented escape must compile; unknown ones must not.
	for _, p := range []string{
		"%a %u %d %r %h %b %p %P %t %m %n %i %e %c %l %s %v %x %Q %% ",
		"%t [%p]: [%l-1] user=%u,db=%d,app=%a,client=%h ",
		"",
	} {
		if _, err := CompilePrefix(p, nil); err != nil {
			t.Errorf("CompilePrefix(%q): %v", p, err)
		}
	}
	if _, err := CompilePrefix("%z ", nil); err == nil {
		t.Error("unknown escape %z compiled")
	}
	m, _ := CompilePrefix("%t [%p]: [%l-1] user=%u,db=%d,app=%a,client=%h ", time.UTC)
	f, ok := m.Match([]byte(`2026-09-02 00:15:25 UTC [42]: [7-1] user=app,db=shop,app=psql,client=10.0.0.9 LOG:  hi`))
	if !ok || f.pid != 42 || f.line != 7 || string(f.db) != "shop" || string(f.app) != "psql" || string(f.host) != "10.0.0.9" {
		t.Errorf("pgbadger prefix: ok=%v %+v", ok, f)
	}
	// %Q carries the pg_stat_statements query id (signed 64-bit); 0 when the
	// server had none to report.
	m, _ = CompilePrefix("%m [%p] %q%u@%d %Q ", time.UTC)
	f, ok = m.Match([]byte(`2026-09-02 00:15:25.123 UTC [42] app@shop -1234567890123456789 LOG:  x`))
	if !ok || f.queryID != -1234567890123456789 || string(f.db) != "shop" || string(f.rest) != "x" {
		t.Errorf("%%Q prefix: ok=%v queryID=%d %+v", ok, f.queryID, f)
	}
	f, ok = m.Match([]byte(`2026-09-02 00:15:25.123 UTC [42] app@shop 0 LOG:  x`))
	if !ok || f.queryID != 0 {
		t.Errorf("%%Q zero: ok=%v queryID=%d", ok, f.queryID)
	}
}

func TestDetectPrefix(t *testing.T) {
	sample := []byte(strings.Join([]string{
		`2026-09-02 00:15:25 UTC [872628-1] herocity0@2a00:1f78:fffd:4301::1221 ERROR:  duplicate key value violates unique constraint "channel_name_plugin_idx"`,
		`2026-09-02 00:15:25 UTC [872628-2] herocity0@2a00:1f78:fffd:4301::1221 DETAIL:  Key (name, plugin)=(x, chat) already exists.`,
		`2026-09-02 00:17:05 UTC [3256329-604] LOG:  checkpoint complete: wrote 1 buffers (0.0%)`,
		`	continuation line`,
		`2026-09-02 06:43:56 UTC [1165189-1] matze@[local] FATAL:  database "pgbouncer" does not exist`,
	}, "\n"))

	// A wrong server prefix is overruled by the file.
	m, detected := DetectPrefix(sample, "%a ", time.UTC)
	if !detected || m.prefix != debianPrefix {
		t.Errorf("detected=%v prefix=%q", detected, m.prefix)
	}
	// The right server prefix wins without detection.
	m, detected = DetectPrefix(sample, debianPrefix, time.UTC)
	if detected || m.prefix != debianPrefix {
		t.Errorf("server prefix not accepted: detected=%v prefix=%q", detected, m.prefix)
	}
	// Bare "SEVERITY:  msg" lines fall back to the empty prefix.
	m, detected = DetectPrefix([]byte("LOG:  foo\nERROR:  bar\n"), "", time.UTC)
	if !detected || m.prefix != "" {
		t.Errorf("bare lines: detected=%v prefix=%q", detected, m.prefix)
	}
	// An exotic prefix still splits on the tag via the loose matcher.
	m, detected = DetectPrefix([]byte("<myapp 2026-09-02 00:15:25 UTC pid=[77-3]> LOG:  foo\n<myapp 2026-09-02 00:15:26 UTC pid=[77-4]> ERROR:  bar\n"), "", time.UTC)
	if !detected || !m.loose {
		t.Fatalf("exotic prefix: detected=%v loose=%v prefix=%q", detected, m.loose, m.prefix)
	}
	f, ok := m.Match([]byte("<myapp 2026-09-02 00:15:26 UTC pid=[77-4]> ERROR:  bar"))
	if !ok || string(f.tag) != "ERROR" || string(f.rest) != "bar" || f.pid != 77 || f.line != 4 || f.time.IsZero() {
		t.Errorf("loose match: ok=%v tag=%q rest=%q pid=%d line=%d time=%v", ok, f.tag, f.rest, f.pid, f.line, f.time)
	}
}

func TestDetectLogFormat(t *testing.T) {
	cases := map[string]Format{
		`2026-09-02 00:15:25 UTC [1-1] LOG:  x`:                     FormatStderr,
		`2026-09-02 00:15:25.123 UTC,"u","d",1,"h",abc.1,1,,,,,LOG`: FormatCSV,
		`{"timestamp":"2026-09-02 00:15:25.123 UTC","pid":1}`:       FormatJSON,
	}
	for in, want := range cases {
		if got := DetectFormat([]byte(in + "\n")); got != want {
			t.Errorf("DetectFormat(%q) = %v, want %v", in, got, want)
		}
	}
}
