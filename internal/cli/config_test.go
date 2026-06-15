package cli

import (
	"net/url"
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
