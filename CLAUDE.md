# pgdu

ncdu-style TUI for browsing PostgreSQL disk usage and shared_buffers occupancy and other diagnostic data.
Go 1.26, Bubble Tea, pgx/v5. Single binary, no daemon.
Postgres 17+18+newwer must be supported query wise.

## Layout

```
main.go              # CLI entry: parse flags → pg.Client → tea.Program
internal/cli/        # flag/env parsing → Config (DSN builder, Target string)
internal/pg/         # pgx wrapper: one *pgxpool.Pool per database, lazy
  queries.go         #   all SQL lives here as named const strings
  types.go           #   shared row structs (Database, Schema, Table, Part, …)
  {entity}.go        #   one file per List*/Fill*/Probe* operation
internal/tui/        # Bubble Tea Model/Update/View
  app.go             #   Model, screen, item, level/tool/sortMode + methods
  update.go          #   top-level Update() dispatcher
  update_msgs.go     #   *LoadedMsg / extStatus / reindexDone handlers
  update_keys.go     #   handleKey, handleFilterKey, reindex confirm flow
  update_drill.go    #   drillIn (level ↓), loadCurrent (issue load Cmd)
  update_sort.go     #   applySort + validSorts + cycleSort
  cmds.go            #   tea.Cmd wrappers around pg.Client (query() helper)
  view.go            #   View() + per-level renderers
  row.go             #   shared row + paintBar/barSegment primitives
  filter.go          #   fuzzy filter, visibleIndexes, viewportRange
  styles.go, keys.go #   lipgloss styles, keyMap
internal/humanize/   # Bytes(int64) → "12.34 MB"
```

## Conventions

- **SQL goes in `internal/pg/queries.go`** as `const sql<Name>`. Functions in
  the entity files (`tables.go`, `parts.go`, …) just call `pool.Query` /
  `pool.QueryRow` and scan into types from `types.go`.
- **Identifiers are quoted with `%q.%q` or `quoteIdent()`** when interpolated
  into SQL — never with `%s`. All identifiers come from catalogs or the
  `Table` struct, never raw user input.
- **Errors wrap with context**: `fmt.Errorf("<op> in %q: %w", db, err)`.
- **Client method signature**: `func (c *Client) X(ctx, …) (…, error)`. Every
  TUI Cmd in `cmds.go` already wraps the call in a 30 s timeout via `query()`
  — REINDEX is the only exception (no client-side timeout).
- **TUI state lives on `screen`**, not on `Model`. `Model` carries the stack
  of screens plus the client and shared widget state (spinner, help, keys).
- **Sort logic is on `sortMode`**: `.label(desc)`, `.less(a,b)`, `.defaultDesc()`,
  `.name()`. Add a new sort by extending the enum + those methods + `validSorts`.
- **Adding a new entity**: drop a new file in `internal/pg/`, define its type
  in `types.go`, add `sql<Name>` in `queries.go`, then wire a level in
  `internal/tui/app.go` (`level` enum + `barReserve`), a Cmd in `cmds.go`,
  a handler in `update_msgs.go`, and a drill case in `update_drill.go`.
- **No comments that just restate the code.** Comments explain the *why*
  (subtle invariants, why we picked a constant, why an obvious thing isn't).

## Build / run

```sh
make build       # → ./pgdu
make test        # go test ./...   (internal/pg has an integration test that
                 # needs PGDU_TEST_DSN; skipped otherwise)
make lint        # golangci-lint --fix + go fix
make deb         # Debian package (debian-pkg/, pgdu_<ver>_<arch>.deb)
./pgdu -U user   # libpq-style flags: -h host -p port -U user -d dbname --dsn URL
```

`pgdu` defaults match `psql`: no `-h` → Unix socket + peer auth. `PGPASSWORD`
is read directly; `~/.pgpass` is honoured by libpq at connect time.

Top-queries snapshots are written to `--snapshot-dir` (default a shared
`$TMPDIR/pgdu-snapshots`, i.e. `/tmp/pgdu-snapshots`; `PGDU_SNAPSHOT_DIR`
overrides) as one timestamped gzip-JSON file each (`pg.Snapshot`). The directory
is intentionally shared and world-writable (created 0o777, no sticky bit, files
0o666) so any user on the host can list/load/delete another user's snapshots.

## Things to know before touching code

- `screen.table` is the source of truth at `levelParts`/`levelColumns` — there
  used to be redundant `tableName`/`tableOID` fields; they were dropped.
- `loadCurrent()` clears `extPrompt` and `installing` on entry; any state set
  before it is loaded asynchronously will be wiped.
- `applySort` is called after every load so render order matches sort order.
  `bloatFilledMsg` matches by name, not index, because of this.
- `extPrompt.blocking == true` replaces the list area entirely; non-blocking
  prompts render as a soft hint line above it.
- Reindex flow is two-step: Enter on a >5% bloated index arms `pendingReindex`,
  any next key cancels — `y`/`Y` executes (see `handleKey`).
- Top-queries snapshots (`internal/pg/snapshots.go`): `S` dumps the raw
  cumulative counters to disk, `L` opens `levelSnapshots` — a timeline range
  picker whose `◀ start`/`◀ end` markers reflect the applied window (derived by
  `appliedWindowPaths`). Enter pairs the pick with the applied start (the
  anchor): no anchor → pick→now live (`statBaseSnap`, "since snapshot, live");
  anchor set → time-ordered range, `statEndSnap` too when frozen (no live
  re-sampling — `loadCurrent`/`onStatementsTick` special-case it). A pick that
  lands as a frozen end keeps the browser open (`snapshotFrozenLoadedMsg.stay`);
  start picks pop back to the table.
  Disk-baseline diffs use `DiffStatementsClamped`; snapshots invalidated by a
  `pg_stat_statements_info.stats_reset` after their capture are filtered out of
  the `L` browser (`onSnapshotsListed`, using the live reset from
  `listSnapshotsCmd`) rather than warned about. `D` deletes via the same
  two-step `pendingDeleteSnap` arm as reindex. The `L` list also carries three
  virtual timeline anchors (sentinel paths `@now`/`@session`/`@reset` in
  `cmds.go`, never backed by a file): "now" (live end, sized from
  `screen.statLiveCount`), "session start" (restores the preserved
  `statSessionBaseline`/`statSessionStart` in-memory window — captured once on
  the first live load, untouched by `R`), and "since last reset" (cumulative,
  dated from the live `statLiveReset`). The render loop (`renderStatementSnapshots`)
  and `loadSelectedSnapshot`/`snapTime` special-case anchors: they carry no
  server/db identity (never "other server/db"), `@session` can only diff
  against `@now`, and `@session` never acts as a pairing anchor (the default
  window isn't a "pick").
- The top-queries table's columns come from a single registry
  (`stmtColumnRegistry` in `queries_columns.go`): each
  `stmtColDesc` carries an id, kind, `defaultOn`, an `available(ctx)` gate
  (planning columns need `track_planning`) and a `cell` builder. `C` opens an
  htop-style picker (`showColumnConfig` overlay) to toggle visibility; choices
  are session-only. `buildStatementItems` *projects* the registry to the visible
  subset so `diagCols` and each row's `[]pg.DiagCell` stay parallel by
  construction (no index constants). The sort column is tracked by stable id
  (`Model.stmtSortColID`); `syncStmtSort` remaps it to the projected
  `diagSortCol` after every rebuild, falling back to `total_ms` if it was hidden.
  New opt-in metrics are one registry entry — every counter is already on
  `QueryStat`.
