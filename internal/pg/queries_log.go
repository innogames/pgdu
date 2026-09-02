package pg

const (
	// sqlLogCurrentLogfile is empty when logging_collector is off — the
	// Debian default, where stderr is redirected by pg_ctlcluster and only the
	// local /var/log/postgresql glob knows the path.
	sqlLogCurrentLogfile = `SELECT COALESCE(pg_current_logfile(), '')`

	// sqlLogStatFile uses missing_ok so a vanished (rotated-away) file yields
	// NULLs instead of an error.
	sqlLogStatFile = `SELECT size, modification FROM pg_stat_file($1, true)`

	// sqlLogReadFile reads one chunk; missing_ok=true returns NULL past EOF.
	// Needs superuser or pg_read_server_files (any path) — pg_ls_logdir's
	// pg_monitor grant does not extend to reading.
	sqlLogReadFile = `SELECT COALESCE(pg_read_binary_file($1, $2, $3, true), ''::bytea)`

	// sqlLogLsLogdir lists log_directory; needs pg_monitor and only helps when
	// logging_collector is on.
	sqlLogLsLogdir = `SELECT name, size, modification FROM pg_ls_logdir() ORDER BY modification DESC`

	// sqlLogLsDir is the superuser-only fallback for the Debian layout when
	// pgdu runs on another host: list the directory, stat each file.
	sqlLogLsDir = `
SELECT d.name, s.size, s.modification
FROM   pg_ls_dir($1) AS d(name)
       LEFT JOIN LATERAL pg_stat_file($1 || '/' || d.name, true) AS s ON true
WHERE  d.name LIKE 'postgresql-%'
ORDER  BY s.modification DESC NULLS LAST`

	sqlLogIsSuper = `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`
)

// logSettingsKeys are the GUCs the analyzer needs to find and parse the log.
var logSettingsKeys = []string{
	"log_line_prefix",
	"log_destination",
	"logging_collector",
	"log_directory",
	"log_filename",
	"data_directory",
	"log_min_duration_statement",
	"log_timezone",
}
