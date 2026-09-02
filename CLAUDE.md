# pgdu

ncdu-style TUI for browsing PostgreSQL disk usage, shared_buffers occupancy, page
contents, WAL, top queries (pg_stat_statements), maintenance/health, live activity, and
other diagnostic data.
Go 1.26, Bubble Tea, pgx/v5. Single binary, no daemon.
Postgres 17, 18 and newer must be supported query wise.

## Layout

```
main.go              # CLI entry: parse flags → pg.Client → tea.Program
internal/cli/        # flag/env parsing → Config (DSN builder, Target string)
internal/pg/         # pgx wrapper: one *pgxpool.Pool per database, lazy
  queries.go         #   foundation SQL (databases/schemas/tables) as named const strings
  types.go           #   foundation row structs (Database, Schema, Table, Part, …)
  queries_{domain}.go#   per-domain SQL (queries_diag/pages/statements/wal/…)
  types_{domain}.go  #   per-domain row structs (types_statements/pages/wal/…)
  {entity}.go        #   one file per entity's List*/Fill*/Probe* operations
internal/tui/        # Bubble Tea Model/Update/View
  app.go             #   Model, screen, item, level/tool/sortMode + methods
  update.go          #   top-level Update() dispatcher
  update_msgs*.go    #   *LoadedMsg / extStatus / reindexDone handlers (split per feature)
  update_keys*.go    #   handleKey, handleFilterKey, confirm flows (split per feature)
  update_drill*.go   #   drillIn (level ↓)
  update_load.go     #   loadCurrent (issue load Cmd)
  update_sort.go     #   applySort + validSorts + cycleSort (sortMode methods in sort_modes.go)
  cmds*.go           #   tea.Cmd wrappers around pg.Client (query() helper)
  view.go, view_*.go #   View() + per-level renderers (view_activity/queries/snapshots/…)
  row.go             #   shared row + paintBar/barSegment primitives
  filter.go          #   fuzzy filter, visibleIndexes, viewportRange
  layout.go          #   layout math, barReserve()
  styles.go, keys.go #   lipgloss styles, keyMap
internal/humanize/   # Bytes(int64) → "12.34 MB"
internal/prefs/      # per-user UI prefs (JSON at ~/.config/pgdu, atomic write)
internal/sysmem/     # host memory stats (used by the maintenance/system overview)
```

## Conventions

- **SQL goes in `internal/pg/queries.go` or a sibling `queries_<domain>.go`** as
  `const sql<Name>` (`queries.go` is the foundation; domain SQL lives in the siblings).
  Functions in the entity files (`tables.go`, `parts.go`, …) just call `pool.Query` /
  `pool.QueryRow` and scan into types from `types.go` / `types_<domain>.go`.
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
  in `types.go` (or a `types_<domain>.go`), add `sql<Name>` in `queries.go` (or a
  `queries_<domain>.go`), then wire a `level` enum value in `internal/tui/app.go`
  (+ a `barReserve()` case in `layout.go`), a Cmd in `cmds.go`, a handler in
  `update_msgs.go`, and a drill case in `update_drill.go`.
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
./pgdu --logs --log-file /var/log/postgresql/postgresql-17-main.log   # log analyzer on a file
```

`pgdu` defaults match `psql`: no `-h` → Unix socket + peer auth. `PGPASSWORD`
is read directly; `~/.pgpass` is honoured by libpq at connect time.

Top-queries snapshots are written to `--snapshot-dir` (default a shared
`$TMPDIR/pgdu-snapshots`, i.e. `/tmp/pgdu-snapshots`; `PGDU_SNAPSHOT_DIR`
overrides) as one timestamped gzip-JSON file each (`pg.Snapshot`). The directory
is intentionally shared and world-writable (created 0o777, no sticky bit, files
0o666) so any user on the host can list/load/delete another user's snapshots.

Per-user UI preferences (currently just C-picker column visibility, per table)
persist to `~/.config/pgdu/prefs.json` via `internal/prefs` (`os.UserConfigDir`,
`PGDU_CONFIG_DIR` overrides; dir 0o700, file 0o600, atomic temp+rename). The
schema (`prefs.Prefs` → `Tables[key].Columns`) is extensible — add sort/refresh
fields later with no migration. `prefs.Load` never fails (missing/corrupt →
empty prefs). The TUI seeds the per-table `*ColsVisible` maps from it in
`NewModel` and writes back via `saveColPrefs` in the C-toggle handlers
(`update_keys_columns.go`); a new table just needs a `colPrefs<Name>` key + one
`saveColPrefs` call.

## Things to know before touching code

- **Tools & levels**: `levelTools` is the root menu; from it you pick one of the `tool`
  values (`toolDisk`, `toolBuffers`, `toolPageInspect`, `toolTools` (diagnostics), `toolWAL`,
  `toolQueries` (top queries), `toolMaintenance`, `toolActivity`, `toolTableStats`,
  `toolTriage`, `toolLogs` (log analyzer)) and drill through that tool's own `level*` chain.
  Both enums live in `app.go`.
- **Tuple `pk` column** (`levelHeapTuples`): `ListHeapTuples` looks up the table's
  primary key and joins it back per line pointer by ctid (`sqlHeapTuplesPK`), so a
  slot shows the row it holds, not just its physical address. The join runs under the
  session's snapshot, so dead/aborted/uncommitted tuples read `—`; both the catalog
  lookup and the join are best-effort (either failing degrades to the plain
  `sqlHeapTuples`, never to a broken view). The key also rides in `item.name`, which is
  what the `/` filter matches, and leads the selected row's expanded block.
- **Index entries and HOT** (`levelIndexTuples`): a `ctid = …` Tid Scan does not follow
  LP_REDIRECT line pointers, so the heap projection in `sqlIndexTuplesDecoded` comes back
  NULL for every HOT-updated row — its index entry still points at the chain root.
  `fillHotChains` walks that one hop (`sqlHeapRedirectKeys`, best-effort) and fills
  `HotCtid`/`HotDecoded`; the row then renders the resolved key plus a `▸off` hop and
  drills into the redirect target. A missing projection is therefore *not* a dead-entry
  signal — only `IndexTuple.Dead` (bt_page_items' own LP_DEAD bit) earns the `dead` tag.
- **Two-step confirm is a shared pattern**: reindex, snapshot delete (`pendingDeleteSnap`),
  backend cancel/terminate (`pendingBackend*`), streaming VACUUM (`pendingVacuum`),
  extension reset, and the diagnostic suggested-fix (`diagFixRun.pending`) all use the
  same flow — Enter arms a `pending*` field, any next key cancels, `y`/`Y` executes (in
  the relevant key handler).
- **Diagnostic fixes** (`levelDiagnosticResult`, `Diagnostic.Fix` builders in
  `pg/diagnostic_fixes.go`): Enter on a row builds the script into `screen.diagFix` and
  opens the overlay (`renderDiagFix`); Enter again arms, `y` runs it via
  `pg.RunFix` (statement-by-statement over a dedicated connection, notices streamed
  like the VACUUM pane), any key after completion dismisses and — on success — reloads
  the result. Identifiers use `fixIdent` (quote only when quote_ident would), not
  `quoteIdent`.
- **Activity** (`toolActivity`, `view_activity.go`): live `pg_stat_activity` browser with a
  `C` column picker (mirrors top-queries), filter/verbose/auto-refresh cycling, and backend
  cancel/terminate. Linux derives CPU%/IO from `/proc` (`activity_proc_linux.go`).
- **Shared-memory map** (`levelShmem`, `view_shmem.go`): from the buffer-tables list,
  `m` opens the whole shared-memory segment (`pg_shmem_allocations`, not just the buffer
  pool). Allocations are bucketed by `shmemCatOf` into coarse subsystem categories
  (buffer pool / WAL / txn-SLRU / locks / backends / stats / other / anonymous / free);
  a grouped category bar sits above the per-allocation list, with `free` as the bar's
  muted tail. The view is built-in (no extension) but needs `pg_read_all_stats`/superuser;
  a lesser role surfaces the permission error. The two NULL-name rows are classified in
  `pg.ShmemAllocations` (off NULL → anonymous, off set → free).
- **Log analyzer** (`toolLogs`, `internal/pg/log*.go`, `internal/tui/*logs*.go`): the
  engine is pure Go — `LoadLog(src, prefix, …)` reads a tail window through a `LogSource`
  (local file, local `.gz`, server-side `pg_read_binary_file`), picks the `log_line_prefix`
  (`DetectPrefix`: server value first, then common Debian/PG defaults, then a loose
  "split on `TAG:  `" fallback), parses stderr/csvlog/jsonlog into `LogEntry`s whose text
  fields are zero-copy sub-slices of the window buffer, attaches DETAIL/HINT/STATEMENT/
  CONTEXT lines to their primary by pid (+ `%l` ordering), classifies each entry into one
  `LogCategory`, and `Aggregate` builds count-sorted `LogGroup`s (fingerprint =
  `normalizeMessage` for errors, `NormalizeSQL` — comments kept — for slow queries) plus
  a per-severity histogram. Spam (`LogCategory.IsSpam`, hidden by `v`) is a *view* filter;
  the report always holds everything. `RefreshLog` is incremental for seekable sources: it re-reads
  from the last entry's offset (`LogCursor`), re-feeds the retained `LogParser`, and falls
  back to a full load on inode change / shrink / gz. In the TUI the levelLogs screen owns
  `logReport`; group/entry children read it via `findLevel(levelLogs)` and are re-pointed
  on every refresh. The groups pane orders itself (`buildLogGroupItems`: sections + sort
  within), so `applySort` special-cases `levelLogs` with `diagCols == nil`; the timeline
  pane is the generic diag table over `logColumnRegistry()`. Section header rows carry
  `logSection` as data and are inert; `skipLogHeader` keeps the cursor off them.
- **Streaming VACUUM** (on `levelParts`): live `NOTICE` output streamed into a scrollable
  pane held in `vacuumState`.
- **Maintenance / system overview** (`toolMaintenance`, `view_maintenance*.go`): extension
  capacity stats (pg_stat_statements/pg_qualstats) with a reset confirm, plus a settings
  browser (`levelSettings`).
- `screen.table` is the source of truth at `levelParts`/`levelColumns`.
- `loadCurrent()` clears `extPrompt` and `installing` on entry; any state set
  before it is loaded asynchronously will be wiped.
- `applySort` is called after every load so render order matches sort order.
  `bloatFilledMsg` matches by name, not index, because of this.
- `extPrompt.blocking == true` replaces the list area entirely; non-blocking
  prompts render as a soft hint line above it.
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
  persist via `internal/prefs`. `buildStatementItems` *projects* the registry to the visible
  subset so `diagCols` and each row's `[]pg.DiagCell` stay parallel by
  construction (no index constants). The sort column is tracked by stable id
  (`Model.stmtSortColID`); `syncStmtSort` remaps it to the projected
  `diagSortCol` after every rebuild, falling back to `total_ms` if it was hidden.
  New opt-in metrics are one registry entry — every counter is already on
  `QueryStat`.
