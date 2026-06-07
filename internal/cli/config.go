package cli

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"
)

// defaultQueriesRefresh is the out-of-the-box re-sample cadence for the
// top-queries window — responsive enough to watch load build without hammering
// the server with snapshot queries.
const defaultQueriesRefresh = 2 * time.Second

// ErrHelp is returned by Parse when the user asked for --help so callers can
// exit cleanly without printing a redundant error.
var ErrHelp = pflag.ErrHelp

// ErrVersion is returned by Parse when the user asked for --version. The caller
// owns the version string (injected at build time), so Parse only signals the
// request and lets main print it.
var ErrVersion = errors.New("version requested")

type Config struct {
	Host     string
	Port     int
	User     string
	Database string
	Password string
	SSLMode  string
	DSN      string

	// QueriesRefresh is how often the top-queries window re-samples. Zero (or
	// negative) disables auto-refresh, leaving the window static until the user
	// resets it (R) or toggles refresh back on (t).
	QueriesRefresh time.Duration
}

func Parse(args []string) (Config, error) {
	fs := pflag.NewFlagSet("pgdu", pflag.ContinueOnError)

	// Defaults mirror libpq / psql: empty means "use libpq's default" so an
	// argless pgdu invocation behaves like an argless psql — Unix socket,
	// peer auth, current user, etc.
	cfg := Config{
		Host:     os.Getenv("PGHOST"),
		Port:     envIntOr("PGPORT", 0),
		User:     envOr("PGUSER", os.Getenv("USER")),
		Database: os.Getenv("PGDATABASE"),
		Password: os.Getenv("PGPASSWORD"),
		SSLMode:  os.Getenv("PGSSLMODE"),

		QueriesRefresh: envDurationOr("PGDU_QUERIES_REFRESH", defaultQueriesRefresh),
	}

	fs.StringVarP(&cfg.Host, "host", "h", cfg.Host, "database server host or socket path (empty = libpq default)")
	fs.IntVarP(&cfg.Port, "port", "p", cfg.Port, "database server port (0 = libpq default 5432)")
	fs.StringVarP(&cfg.User, "username", "U", cfg.User, "database user name")
	fs.StringVarP(&cfg.Database, "dbname", "d", cfg.Database, "initial database to connect to (empty = same as user)")
	fs.StringVar(&cfg.SSLMode, "sslmode", cfg.SSLMode, "SSL mode (disable|allow|prefer|require|verify-ca|verify-full)")
	fs.StringVar(&cfg.DSN, "dsn", "", "full PostgreSQL connection URL (overrides individual flags)")
	fs.DurationVar(&cfg.QueriesRefresh, "queries-refresh", cfg.QueriesRefresh, "top-queries auto-refresh interval (e.g. 5s, 1m; 0 disables)")

	var showVersion bool
	fs.BoolVar(&showVersion, "version", false, "print version and exit")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "pgdu - PostgreSQL disk usage explorer (ncdu-style TUI)\n\n")
		fmt.Fprintf(os.Stderr, "Usage: pgdu [flags]\n\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nWith no -h, pgdu connects via the local Unix socket (same as psql).\n")
		fmt.Fprintf(os.Stderr, "Password: pgdu reads PGPASSWORD; ~/.pgpass is consulted by libpq at connect time.\n")
	}

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	if showVersion {
		return Config{}, ErrVersion
	}

	if cfg.DSN == "" && cfg.User == "" {
		return Config{}, fmt.Errorf("username required: pass -U or set PGUSER")
	}
	return cfg, nil
}

// BuildDSN returns a libpq-style key=value connection string for the given
// (possibly overridden) database name. Empty fields are omitted so libpq/pgx
// can apply its own defaults — most importantly, an empty Host triggers a
// Unix socket connection with peer auth (matching psql).
//
// If --dsn was supplied we hand it back unmodified, except when an override
// database is requested: then we rewrite the path component of the URL.
func (c Config) BuildDSN(overrideDB string) string {
	db := c.Database
	if overrideDB != "" {
		db = overrideDB
	}
	if c.DSN != "" {
		if overrideDB != "" {
			if u, err := url.Parse(c.DSN); err == nil {
				u.Path = "/" + overrideDB
				return u.String()
			}
		}
		return c.DSN
	}

	var parts []string
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, k+"="+kvQuote(v))
		}
	}
	add("host", c.Host)
	if c.Port != 0 {
		parts = append(parts, "port="+strconv.Itoa(c.Port))
	}
	add("user", c.User)
	add("dbname", db)
	add("password", c.Password)
	add("sslmode", c.SSLMode)
	parts = append(parts, "application_name=pgdu")
	return strings.Join(parts, " ")
}

// kvQuote produces a libpq key=value value, single-quoting it when it contains
// whitespace, backslashes, or single quotes.
func kvQuote(v string) string {
	if !strings.ContainsAny(v, " \t'\\") {
		return v
	}
	escaped := strings.ReplaceAll(v, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `'`, `\'`)
	return "'" + escaped + "'"
}

// Redacted returns a Config with any password material blanked out. Use this
// when logging or otherwise surfacing the config so credentials don't leak
// into stderr or telemetry.
func (c Config) Redacted() Config {
	if c.Password != "" {
		c.Password = "<redacted>"
	}
	if c.DSN != "" {
		if u, err := url.Parse(c.DSN); err == nil && u.User != nil {
			if _, hasPW := u.User.Password(); hasPW {
				u.User = url.UserPassword(u.User.Username(), "redacted")
				c.DSN = u.String()
			}
		}
	}
	return c
}

// Target returns a human-friendly connection target for the header bar.
func (c Config) Target() string {
	if c.DSN != "" {
		if u, err := url.Parse(c.DSN); err == nil && u.Host != "" {
			return u.Host
		}
		return c.DSN
	}
	host := c.Host
	if host == "" {
		return "socket"
	}
	port := c.Port
	if port == 0 {
		port = 5432
	}
	if strings.HasPrefix(host, "/") {
		return host
	}
	return host + ":" + strconv.Itoa(port)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// envDurationOr parses a Go duration string (e.g. "5s", "0") from the
// environment, falling back to def when unset or unparseable.
func envDurationOr(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
