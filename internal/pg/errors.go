package pg

import "fmt"

// MissingExtensionError signals that an optional Postgres extension pgdu
// would like to use isn't installed in the target database. The TUI uses
// the typed error to offer an interactive `CREATE EXTENSION` instead of
// either silently degrading or failing with an opaque message.
type MissingExtensionError struct {
	Extension string
	DB        string
	// Installable is true when the extension shows up in pg_available_extensions
	// (i.e. CREATE EXTENSION would succeed given sufficient privileges).
	Installable bool
}

func (e *MissingExtensionError) Error() string {
	if e.Installable {
		return fmt.Sprintf("extension %q is not installed in %q (can be installed)", e.Extension, e.DB)
	}
	return fmt.Sprintf("extension %q is not installed in %q and not available on the server", e.Extension, e.DB)
}

// OutdatedExtensionError signals that an extension pgdu needs is installed but
// at a version too old to satisfy the columns pgdu's queries select — the
// classic case being a cluster pg_upgraded to PG17 whose pg_stat_statements
// objects were never `ALTER EXTENSION ... UPDATE`d off the old 1.6/1.7 layout
// (which lacks total_exec_time/plans/wal_*). The TUI uses the typed error to
// offer an interactive upgrade with the installed/available versions shown,
// instead of failing with an opaque "column does not exist" message.
type OutdatedExtensionError struct {
	Extension string
	DB        string
	Installed string // currently installed version, e.g. "1.6"
	Available string // pg_available_extensions.default_version, e.g. "1.11"
	Required  string // minimum version pgdu needs, e.g. "1.8"
	// Updatable is true when Available satisfies Required, i.e. an
	// `ALTER EXTENSION ... UPDATE` would lift the version high enough. False
	// when even the server's default version is too old (the binaries
	// themselves predate what pgdu needs — nothing an in-database UPDATE fixes).
	Updatable bool
}

func (e *OutdatedExtensionError) Error() string {
	return fmt.Sprintf("extension %q in %q is version %s; pgdu needs >= %s (server default: %s)",
		e.Extension, e.DB, e.Installed, e.Required, e.Available)
}
