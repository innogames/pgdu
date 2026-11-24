package cli

import (
	"errors"
	"flag"
	"net/url"
	"os"
	"strings"
	"testing"
)

func parseKV(s string) map[string]string {
	out := map[string]string{}
	for tok := range strings.FieldsSeq(s) {
		before, after, ok := strings.Cut(tok, "=")
		if !ok {
			continue
		}
		k, v := before, after
		v = strings.Trim(v, "'")
		out[k] = v
	}
	return out
}

func TestBuildDSNFromFlags(t *testing.T) {
	c := Config{Host: "db.example", Port: 6432, User: "alice", Database: "shop", SSLMode: "require"}
	kv := parseKV(c.BuildDSN(""))
	if kv["host"] != "db.example" {
		t.Errorf("host = %q", kv["host"])
	}
	if kv["port"] != "6432" {
		t.Errorf("port = %q", kv["port"])
	}
	if kv["user"] != "alice" {
		t.Errorf("user = %q", kv["user"])
	}
	if kv["dbname"] != "shop" {
		t.Errorf("dbname = %q", kv["dbname"])
	}
	if kv["sslmode"] != "require" {
		t.Errorf("sslmode = %q", kv["sslmode"])
	}
	if kv["application_name"] != "pgdu" {
		t.Errorf("application_name = %q", kv["application_name"])
	}
}

func TestBuildDSNDefaultsToSocket(t *testing.T) {
	c := Config{User: "alice"} // no host, no port → socket via libpq
	dsn := c.BuildDSN("")
	if strings.Contains(dsn, "host=") {
		t.Errorf("expected no host= when Host is empty, got %q", dsn)
	}
	if strings.Contains(dsn, "port=") {
		t.Errorf("expected no port= when Port is 0, got %q", dsn)
	}
	if !strings.Contains(dsn, "user=alice") {
		t.Errorf("user missing: %q", dsn)
	}
}

func TestBuildDSNOverrideDB(t *testing.T) {
	c := Config{Host: "h", Port: 5432, User: "u", Database: "a", SSLMode: "disable"}
	kv := parseKV(c.BuildDSN("other"))
	if kv["dbname"] != "other" {
		t.Errorf("override dbname = %q", kv["dbname"])
	}
}

func TestBuildDSNRawDSNOverride(t *testing.T) {
	c := Config{DSN: "postgres://u:p@h:5432/orig?sslmode=disable"}
	if got := c.BuildDSN(""); got != c.DSN {
		t.Errorf("raw DSN should pass through, got %q", got)
	}
	got := c.BuildDSN("newdb")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Path != "/newdb" {
		t.Errorf("override DB path = %q", u.Path)
	}
	if u.User.Username() != "u" {
		t.Errorf("user lost: %q", u.User.Username())
	}
}

func TestBuildDSNQuotesValueWithSpace(t *testing.T) {
	c := Config{User: "alice", Password: "p w"}
	dsn := c.BuildDSN("")
	if !strings.Contains(dsn, "password='p w'") {
		t.Errorf("expected quoted password, got %q", dsn)
	}
}

func TestTargetSocket(t *testing.T) {
	if got := (Config{}).Target(); got != "socket" {
		t.Errorf("Target empty host = %q, want socket", got)
	}
	if got := (Config{Host: "/var/run/postgresql"}).Target(); got != "/var/run/postgresql" {
		t.Errorf("socket-path Target = %q", got)
	}
	if got := (Config{Host: "db", Port: 5432}).Target(); got != "db:5432" {
		t.Errorf("TCP Target = %q", got)
	}
}

func TestHostLabel(t *testing.T) {
	local, err := os.Hostname()
	if err != nil || local == "" {
		local = "localhost"
	}
	cases := []struct {
		cfg  Config
		want string
	}{
		{Config{Host: "db", Port: 5432}, "db:5432"},
		{Config{Host: "db"}, "db:5432"},
		{Config{Host: "db", Port: 6432}, "db:6432"},
		{Config{}, local},
		{Config{Port: 5432}, local},
		{Config{Host: "/var/run/postgresql"}, local},
		{Config{Host: "/run/pg", Port: 6432}, local + ":6432"},
		{Config{DSN: "postgres://u:p@h:6432/db"}, "h:6432"},
		{Config{DSN: "postgres://u@h/db"}, "h:5432"},
		{Config{DSN: "postgres:///db"}, local},
		{Config{DSN: "host=h port=6432 dbname=db"}, "h:6432"},
		{Config{DSN: "dbname=db port=6432"}, local + ":6432"},
	}
	for _, c := range cases {
		if got := c.cfg.HostLabel(); got != c.want {
			t.Errorf("HostLabel(%+v) = %q, want %q", c.cfg, got, c.want)
		}
	}
}

func TestParseToolShortcuts(t *testing.T) {
	t.Setenv("PGUSER", "alice")
	cases := map[string]string{
		"--disk-usage":     "disk",
		"--shared-buffers": "buffers",
		"--activity":       "activity",
		"--top-queries":    "queries",
	}
	for flag, want := range cases {
		cfg, err := Parse([]string{flag})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", flag, err)
		}
		if cfg.Tool != want {
			t.Errorf("%s: Tool = %q, want %q", flag, cfg.Tool, want)
		}
	}

	if cfg, err := Parse([]string{}); err != nil || cfg.Tool != "" {
		t.Errorf("no flag: Tool = %q (err %v), want empty", cfg.Tool, err)
	}

	if _, err := Parse([]string{"--disk-usage", "--activity"}); err == nil {
		t.Error("expected error when two tool shortcuts are given")
	}
}

func TestParseRequiresUser(t *testing.T) {
	t.Setenv("PGUSER", "")
	t.Setenv("USER", "")
	if _, err := Parse([]string{}); err == nil {
		t.Fatal("expected error when no user set")
	}
}

func TestParsePgBouncerTool(t *testing.T) {
	cfg, err := Parse([]string{"-U", "postgres", "--pgbouncer"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tool != "pgbouncer" {
		t.Errorf("Tool = %q", cfg.Tool)
	}
	if _, err := Parse([]string{"-U", "x", "--pgbouncer", "--logs"}); err == nil || !strings.Contains(err.Error(), "--pgbouncer") {
		t.Errorf("two tool flags must be rejected naming --pgbouncer: %v", err)
	}
}

// Both spellings of a connection flag are one flag: whichever is typed sets
// the same field, with the value as its own word or behind '='.
func TestParseShortAndLongSpellings(t *testing.T) {
	for _, args := range [][]string{
		{"-h", "db", "-p", "6432", "-U", "alice", "-d", "shop"},
		{"--host", "db", "--port", "6432", "--username", "alice", "--dbname", "shop"},
		{"--host=db", "-p=6432", "--username=alice", "-d=shop"},
	} {
		cfg, err := Parse(args)
		if err != nil {
			t.Fatalf("Parse(%v): %v", args, err)
		}
		if cfg.Host != "db" || cfg.Port != 6432 || cfg.User != "alice" || cfg.Database != "shop" {
			t.Errorf("Parse(%v) = host %q port %d user %q db %q", args, cfg.Host, cfg.Port, cfg.User, cfg.Database)
		}
	}
}

// psql's attached form is not a value here: the flag package reads -p5433 as a
// flag of that name, and that must fail loudly rather than connect to the
// default port.
func TestParseRejectsAttachedShortValue(t *testing.T) {
	for _, arg := range []string{"-p5433", "-Ualice", "-dshop", "-hdb"} {
		if _, err := Parse([]string{"-U", "u", arg}); err == nil {
			t.Errorf("%s: want an error", arg)
		}
	}
}

// Parsing stops at the first word that is not a flag, so one has to be an
// error: otherwise the -U behind it is dropped and pgdu connects as someone
// else.
func TestParseRejectsPositionalArgument(t *testing.T) {
	t.Setenv("PGUSER", "me")
	_, err := Parse([]string{"shop", "-U", "alice"})
	if err == nil || !strings.Contains(err.Error(), `"shop"`) {
		t.Errorf("want an error naming the stray argument, got %v", err)
	}
}

// -h is the host, as in psql; only --help asks for help.
func TestParseHelp(t *testing.T) {
	if _, err := Parse([]string{"--help"}); !errors.Is(err, ErrHelp) {
		t.Errorf("--help: err = %v, want ErrHelp", err)
	}
	if _, err := Parse([]string{"-U", "u", "-h"}); err == nil || errors.Is(err, ErrHelp) {
		t.Errorf("-h without a value: err = %v, want a missing-argument error", err)
	}
}

// A short spelling shares its long flag's row instead of getting one of its
// own, zero defaults stay unprinted, and the usage column lines up.
func TestUsageText(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.String("host", "", "server host")
	fs.Var(fs.Lookup("host").Value, "h", "")
	fs.String("snapshot-dir", "/tmp/x", "where snapshots go")
	fs.Int("sample-retention", 30, "how many to keep")
	fs.Int("limit", 0, "row limit")
	fs.Bool("logs", false, "open the log analyzer")
	lines := strings.Split(usageText(fs), "\n")

	rows := []struct{ flag, usage string }{
		{"  -h, --host string", "server host"},
		{"      --logs", "open the log analyzer"},
		{"      --limit int", "row limit"},
		{"      --sample-retention int", "how many to keep (default 30)"},
		{"      --snapshot-dir string", `where snapshots go (default "/tmp/x")`},
	}
	col := -1
	for _, r := range rows {
		found := false
		for _, line := range lines {
			if !strings.HasPrefix(line, r.flag+"  ") || !strings.HasSuffix(line, "  "+r.usage) {
				continue
			}
			found = true
			if at := len(line) - len(r.usage); col == -1 {
				col = at
			} else if at != col {
				t.Errorf("%q: usage starts at column %d, the rows above at %d", r.flag, at, col)
			}
		}
		if !found {
			t.Errorf("no row %q … %q in:\n%s", r.flag, r.usage, strings.Join(lines, "\n"))
		}
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "      --h ") || strings.HasPrefix(line, "  -h ") {
			t.Errorf("the short spelling got a row of its own: %q", line)
		}
	}
}

func TestParseBytes(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"1024", 1024, false},
		{"1K", 1 << 10, false},
		{"1KB", 1 << 10, false},
		{"1KiB", 1 << 10, false},
		{"4M", 4 << 20, false},
		{"2g", 2 << 30, false},
		{"1T", 1 << 40, false},
		{"1MBs", 0, true},
		{"-5M", 0, true},
		{"abc", 0, true},
	}
	for _, c := range cases {
		got, err := parseBytes(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseBytes(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if err == nil && got != c.want {
			t.Errorf("parseBytes(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// The printed --sample-min-bytes default must round-trip through its own
// parser, or --help advertises a value the flag would reject. (--sample-retention
// is a plain int, so the flag package prints and parses it without a
// hand-written pair.)
func TestSampleFlagDefaultsRoundTrip(t *testing.T) {
	for _, n := range []int64{0, 1 << 20, 1536} {
		got, err := parseBytes(formatBytes(n))
		if err != nil || got != n {
			t.Errorf("bytes %d -> %q -> %d (%v)", n, formatBytes(n), got, err)
		}
	}
}

func TestParseGrowthTool(t *testing.T) {
	cfg, err := Parse([]string{"-U", "u", "--growth"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Tool != "growth" {
		t.Errorf("Tool = %q, want growth", cfg.Tool)
	}
}

func TestParseRejectsTwoTools(t *testing.T) {
	_, err := Parse([]string{"-U", "u", "--growth", "--disk-usage"})
	if err == nil {
		t.Fatal("want an error for two tool flags")
	}
	// The message is built from the flag list, so it must name the new one too.
	if !strings.Contains(err.Error(), "--growth") || !strings.Contains(err.Error(), "--disk-usage") {
		t.Errorf("error does not name both flags: %v", err)
	}
}

func TestParseSampleFlags(t *testing.T) {
	cfg, err := Parse([]string{"-U", "u", "--sample", "--sample-min-bytes", "1MB", "--sample-retention", "10"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !cfg.Sample {
		t.Error("Sample not set")
	}
	if cfg.SampleMinBytes != 1<<20 {
		t.Errorf("SampleMinBytes = %d, want 1MiB", cfg.SampleMinBytes)
	}
	if cfg.SampleRetention != 10 {
		t.Errorf("SampleRetention = %d, want 10", cfg.SampleRetention)
	}
}

func TestParseSampleRejectsToolFlag(t *testing.T) {
	if _, err := Parse([]string{"-U", "u", "--sample", "--growth"}); err == nil {
		t.Error("want an error: --sample is non-interactive")
	}
}

func TestParseSampleBadValues(t *testing.T) {
	if _, err := Parse([]string{"-U", "u", "--sample-min-bytes", "lots"}); err == nil {
		t.Error("want an error for an unparseable size")
	}
	if _, err := Parse([]string{"-U", "u", "--sample-retention", "forever"}); err == nil {
		t.Error("want an error for an unparseable count")
	}
	if _, err := Parse([]string{"-U", "u", "--sample-retention", "-1"}); err == nil {
		t.Error("want an error for a negative count")
	}
}

func TestParseSampleDefaults(t *testing.T) {
	cfg, err := Parse([]string{"-U", "u"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Sample {
		t.Error("Sample must default off")
	}
	if cfg.SampleMinBytes != 0 {
		t.Errorf("SampleMinBytes = %d, want 0 (keep every relation)", cfg.SampleMinBytes)
	}
	if cfg.SampleRetention != defaultRetention {
		t.Errorf("SampleRetention = %d, want %d", cfg.SampleRetention, defaultRetention)
	}
	if !cfg.AutoSample {
		t.Error("AutoSample must default on — the history only accrues if it does")
	}
}

func TestParseNoAutoSample(t *testing.T) {
	cfg, err := Parse([]string{"-U", "u", "--no-auto-sample"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.AutoSample {
		t.Error("--no-auto-sample must turn it off")
	}
}

// The flag is applied after Parse so it beats an env-set default, and an
// unparseable env value falls back rather than silently flipping the default.
func TestAutoSampleEnv(t *testing.T) {
	cases := []struct {
		env  string
		args []string
		want bool
	}{
		{"0", nil, false},
		{"false", nil, false},
		{"1", nil, true},
		{"maybe", nil, true},
		{"1", []string{"--no-auto-sample"}, false},
	}
	for _, c := range cases {
		t.Setenv("PGDU_AUTO_SAMPLE", c.env)
		cfg, err := Parse(append([]string{"-U", "u"}, c.args...))
		if err != nil {
			t.Fatalf("Parse(%q, %v): %v", c.env, c.args, err)
		}
		if cfg.AutoSample != c.want {
			t.Errorf("PGDU_AUTO_SAMPLE=%q %v -> AutoSample %v, want %v", c.env, c.args, cfg.AutoSample, c.want)
		}
	}
}

func TestLocalServer(t *testing.T) {
	cases := []struct {
		cfg  Config
		want bool
	}{
		{Config{}, true},
		{Config{Host: "/var/run/postgresql"}, true},
		{Config{Host: "localhost"}, true},
		{Config{Host: "127.0.0.1"}, true},
		{Config{Host: "::1"}, true},
		{Config{Host: "db"}, false},
		{Config{Host: "10.0.0.5"}, false},
		{Config{DSN: "postgres://u@127.0.0.1:5432/db"}, true},
		{Config{DSN: "postgres://u@h/db"}, false},
		{Config{DSN: "host=/run/pg dbname=db"}, true},
		{Config{DSN: "host=h dbname=db"}, false},
	}
	for _, c := range cases {
		if got := c.cfg.LocalServer(); got != c.want {
			t.Errorf("LocalServer(%+v) = %v, want %v", c.cfg, got, c.want)
		}
	}
}
