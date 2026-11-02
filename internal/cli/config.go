package cli

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
)

// defaultRetention keeps 30 captures of each kind per database per host —
// about a month of once-a-day history. It mirrors pg.DefaultRetention, which
// this package cannot import because pg imports it.
const defaultRetention = 30

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

	// SnapshotDir is where top-queries snapshots are written (S) and read (L),
	// and where size samples (--sample, *.sizes.gz) live alongside them.
	// Defaults to a shared directory under the system temp dir
	// ($TMPDIR/pgdu-snapshots, i.e. /tmp/pgdu-snapshots) so snapshots taken by
	// one user can be read and deleted by any other user on the same host.
	//
	// On most distributions that path is tmpfs or cleaned at boot, so point it
	// somewhere persistent if you want size history to outlive a reboot.
	SnapshotDir string

	// Tool, when non-empty, names the top-level tool to open directly, skipping
	// the tool-picker screen. One of the canonical tool names ("disk",
	// "buffers", "queries", "activity", "logs", "pgbouncer", "growth"); empty
	// means start on the picker.
	Tool string

	// Sample turns pgdu into a one-shot size sampler: capture, write, prune,
	// print the path, exit — no TUI. The "no daemon" rule means size history
	// only accrues if something runs on a schedule, so this is the cron entry
	// point.
	Sample bool

	// SampleMinBytes folds relations below this size into the sample's "other"
	// counters. 0 (the default) keeps every relation.
	SampleMinBytes int64

	// SampleRetention is how many captures of each kind to keep per
	// (host, database): the newest N survive a prune, the rest are deleted.
	// 0 disables pruning entirely. One knob governs both pruners, so an
	// operator who widens it for cron does not have the TUI's auto-capture
	// sweep the extra files away the next time they open a tool.
	SampleRetention int

	// AutoSample makes opening the growth or top-queries tool capture a sample
	// for that database when none was captured today. PGDU_AUTO_SAMPLE=0 or
	// --no-auto-sample turns it off; there is deliberately no prefs entry,
	// because it is a host policy (who may scan, what /tmp costs), not a
	// per-user UI preference.
	AutoSample bool

	// LogFile is an explicit server log to analyze (--log-file / PGDU_LOG_FILE).
	// Empty means auto-detect: pg_current_logfile(), then /var/log/postgresql.
	LogFile string
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

		SnapshotDir: envOr("PGDU_SNAPSHOT_DIR", defaultSnapshotDir()),
		LogFile:     os.Getenv("PGDU_LOG_FILE"),

		SampleMinBytes:  envBytesOr("PGDU_SAMPLE_MIN_BYTES", 0),
		SampleRetention: envIntOr("PGDU_SAMPLE_RETENTION", defaultRetention),
		AutoSample:      envBoolOr("PGDU_AUTO_SAMPLE", true),
	}

	fs.StringVarP(&cfg.Host, "host", "h", cfg.Host, "database server host or socket path (empty = libpq default)")
	fs.IntVarP(&cfg.Port, "port", "p", cfg.Port, "database server port (0 = libpq default 5432)")
	fs.StringVarP(&cfg.User, "username", "U", cfg.User, "database user name")
	fs.StringVarP(&cfg.Database, "dbname", "d", cfg.Database, "initial database to connect to (empty = same as user)")
	fs.StringVar(&cfg.SSLMode, "sslmode", cfg.SSLMode, "SSL mode (disable|allow|prefer|require|verify-ca|verify-full)")
	fs.StringVar(&cfg.DSN, "dsn", "", "full PostgreSQL connection URL (overrides individual flags)")
	fs.StringVar(&cfg.SnapshotDir, "snapshot-dir", cfg.SnapshotDir, "directory for top-queries snapshots (S saves, L loads)")
	fs.StringVar(&cfg.LogFile, "log-file", cfg.LogFile, "server log to analyze (plain or .gz; default: auto-detect via pg_current_logfile / /var/log/postgresql)")

	var showVersion bool
	fs.BoolVar(&showVersion, "version", false, "print version and exit")

	// Size sampling: the non-interactive half of the growth tool.
	fs.BoolVar(&cfg.Sample, "sample", false, "capture a size sample into --snapshot-dir, prune old ones, print the path and exit (no TUI)")
	minBytes := fs.String("sample-min-bytes", formatBytes(cfg.SampleMinBytes), "fold relations below this size into the sample's \"other\" total (e.g. 1MB; 0 = keep all)")
	fs.IntVar(&cfg.SampleRetention, "sample-retention", cfg.SampleRetention,
		"how many captures of each kind to keep per database (0 = never prune)")

	// A --no- flag defaulting false rather than --auto-sample=true: the env var
	// already carries the default, and --help printing "--auto-sample (default
	// true)" next to an env override would state a default that may not hold.
	var noAutoSample bool
	fs.BoolVar(&noAutoSample, "no-auto-sample", false,
		"do not capture a daily sample when the growth or top-queries tool opens")

	// Tool shortcuts: each opens a top-level tool directly, skipping the picker.
	// They are mutually exclusive — pick at most one.
	var diskUsage, sharedBuffers, activity, topQueries, logs, pgbouncer, growth bool
	fs.BoolVar(&diskUsage, "disk-usage", false, "start directly in the disk-usage browser")
	fs.BoolVar(&sharedBuffers, "shared-buffers", false, "start directly in the shared-buffers browser")
	fs.BoolVar(&activity, "activity", false, "start directly in the activity tool (pg_stat_activity)")
	fs.BoolVar(&topQueries, "top-queries", false, "start directly in the top-queries tool (pg_stat_statements)")
	fs.BoolVar(&logs, "logs", false, "start directly in the log analyzer")
	fs.BoolVar(&pgbouncer, "pgbouncer", false, "start directly in the pgbouncer tool")
	fs.BoolVar(&growth, "growth", false, "start directly in the size-history/growth tool")

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

	var err error
	if cfg.SampleMinBytes, err = parseBytes(*minBytes); err != nil {
		return Config{}, fmt.Errorf("--sample-min-bytes: %w", err)
	}
	if cfg.SampleRetention < 0 {
		return Config{}, errors.New("--sample-retention: must not be negative")
	}
	// Applied after Parse so the flag beats an env-set default, never the other
	// way round. It is deliberately NOT rejected alongside --sample: there it is
	// simply irrelevant, the way --snapshot-dir is to a tool flag.
	if noAutoSample {
		cfg.AutoSample = false
	}

	// Map the tool shortcut flags onto cfg.Tool, rejecting more than one. The
	// flag spellings are listed once, so the error can never drift from the set
	// of flags as tools are added.
	tools := []struct {
		name, flag string
		set        bool
	}{
		{"disk", "--disk-usage", diskUsage},
		{"buffers", "--shared-buffers", sharedBuffers},
		{"activity", "--activity", activity},
		{"queries", "--top-queries", topQueries},
		{"logs", "--logs", logs},
		{"pgbouncer", "--pgbouncer", pgbouncer},
		{"growth", "--growth", growth},
	}
	for _, t := range tools {
		if !t.set {
			continue
		}
		if cfg.Tool != "" {
			flags := make([]string, 0, len(tools))
			for _, t := range tools {
				flags = append(flags, t.flag)
			}
			return Config{}, fmt.Errorf("only one of %s may be given", strings.Join(flags, "/"))
		}
		cfg.Tool = t.name
	}

	// --sample is non-interactive, so a tool to open is meaningless with it.
	if cfg.Sample && cfg.Tool != "" {
		return Config{}, errors.New("--sample is non-interactive and cannot be combined with a tool flag")
	}

	if cfg.DSN == "" && cfg.User == "" {
		return Config{}, errors.New("username required: pass -U or set PGUSER")
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

// HostLabel is the root crumb of the TUI breadcrumb: the machine the cluster
// runs on, with the port when it disambiguates. Unlike Target it never reads
// "socket" — a Unix-socket connection is to this very machine, so the local
// hostname is the honest answer. Target stays as it is because snapshots and
// pgbouncer instance names are keyed on it.
func (c Config) HostLabel() string {
	host, port := c.Host, c.Port
	if c.DSN != "" {
		if u, err := url.Parse(c.DSN); err == nil && u.Host != "" {
			if u.Port() == "" {
				return u.Host + ":5432"
			}
			return u.Host
		}
		// Keyword DSN: pick host=/port= out of it; a DSN naming neither is a
		// socket connection like an empty --host.
		host, port = "", 0
		for tok := range strings.FieldsSeq(c.DSN) {
			k, v, ok := strings.Cut(tok, "=")
			if !ok {
				continue
			}
			v = strings.Trim(v, "'")
			switch k {
			case "host":
				host = v
			case "port":
				port, _ = strconv.Atoi(v)
			}
		}
	}
	if host == "" || strings.HasPrefix(host, "/") {
		name, err := os.Hostname()
		if err != nil || name == "" {
			name = "localhost"
		}
		if port != 0 && port != 5432 {
			return name + ":" + strconv.Itoa(port)
		}
		return name
	}
	if port == 0 {
		port = 5432
	}
	return host + ":" + strconv.Itoa(port)
}

// defaultSnapshotDir is a single shared directory under the system temp dir
// ($TMPDIR, or /tmp). It is deliberately NOT per-user: snapshots are meant to be
// readable and deletable across users on the same host, so every user resolves
// to the same path. SaveSnapshot widens the directory's permissions to make that
// sharing actually work (see SaveSnapshot).
func defaultSnapshotDir() string {
	return filepath.Join(os.TempDir(), "pgdu-snapshots")
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

// parseBytes accepts a plain byte count or one with a K/M/G/T suffix (KB, MiB
// and so on too), so a cron line reads --sample-min-bytes 1MB rather than
// 1048576. Suffixes are binary: 1K is 1024.
func parseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	// Strip the optional "B" and the "i" of the KiB/MiB spellings before
	// reading the unit letter, so 1K, 1KB and 1KiB all mean the same thing.
	mult := int64(1)
	num := strings.ToUpper(s)
	num = strings.TrimSuffix(num, "B")
	num = strings.TrimSuffix(num, "I")
	if num != "" {
		switch num[len(num)-1] {
		case 'K':
			mult, num = 1<<10, num[:len(num)-1]
		case 'M':
			mult, num = 1<<20, num[:len(num)-1]
		case 'G':
			mult, num = 1<<30, num[:len(num)-1]
		case 'T':
			mult, num = 1<<40, num[:len(num)-1]
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(num), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	if n < 0 {
		return 0, fmt.Errorf("size %q must not be negative", s)
	}
	return n * mult, nil
}

// formatBytes renders a byte count the way parseBytes reads it, so the flag's
// printed default round-trips.
func formatBytes(n int64) string {
	switch {
	case n == 0:
		return "0"
	case n%(1<<40) == 0:
		return strconv.FormatInt(n/(1<<40), 10) + "T"
	case n%(1<<30) == 0:
		return strconv.FormatInt(n/(1<<30), 10) + "G"
	case n%(1<<20) == 0:
		return strconv.FormatInt(n/(1<<20), 10) + "M"
	case n%(1<<10) == 0:
		return strconv.FormatInt(n/(1<<10), 10) + "K"
	}
	return strconv.FormatInt(n, 10)
}

func envBytesOr(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := parseBytes(v); err == nil {
			return n
		}
	}
	return def
}

// envBoolOr reads an on/off env knob through strconv.ParseBool (1/0, t/f,
// true/false), falling back to def on anything unparseable — an operator typo
// must not silently flip a default-on behaviour.
func envBoolOr(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
