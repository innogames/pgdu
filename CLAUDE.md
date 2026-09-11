# pgdu

ncdu-style TUI for browsing PostgreSQL disk usage, shared_buffers occupancy, page
contents, WAL, top queries (pg_stat_statements), maintenance/health, live activity, logs,
pgbouncer and other diagnostic data.
Go 1.26, Bubble Tea, pgx/v5. Single binary, no daemon.
All SQL must work on Postgres 17, 18 and newer.

## Layout

```
main.go              # CLI entry: cli.Parse → pg.New → tea.Program
internal/cli/        # flag/env parsing → Config (DSN builder, Target string)
internal/pg/         # pgx wrapper: one *pgxpool.Pool per database, lazy
  queries.go         #   foundation SQL (databases/schemas/tables) as named const strings
  queries_{domain}.go#   per-domain SQL
  queries_diag_{cat}.go #   diagnostics SQL, one file per category (index/table/vacuum/activity/wal/server)
  diag_defs_{cat}.go #   Diagnostics registry entries per category; diagnostic_defs.go concatenates them
  types.go, types_{domain}.go   # row structs
  {entity}.go        #   one file per entity's List*/Fill*/Probe* operations
  pgbouncer.go       #   DiscoverPgBouncers: pgbouncer.Client.Discover + the behind-a-pooler check
  logdiscover.go     #   log file discovery (pg_current_logfile / pg_ls_logdir) + server-side LogSource
internal/diagres/    # generic column/row result table (kinds, cells, pgx row scan) shared by pg and pgbouncer
internal/pgbouncer/  # pgbouncer console: ini parser, discovery, simple-protocol conns (own Client, no pool)
internal/pageinspect/# byte decoders over pg row types: tuple layout, index keys, jsonb, TOAST
                     #   pointers, compressed blobs (gzip/zlib/zstd inside a bytea)
internal/procfs/     # Linux-only host reads (/proc process list + per-PID stats, stat inode) with no-op fallbacks
internal/pglog/      # log analyzer engine (pure Go, no pgx): parse, classify, aggregate, local/gz sources
internal/tui/        # Bubble Tea Model/Update/View
  app.go             #   Model, screen, item, level/tool enums
  update.go          #   top-level Update() dispatcher
  update_msgs*.go    #   *LoadedMsg handlers (split per feature)
  update_keys*.go    #   key handling + confirm flows (split per feature)
  update_drill*.go   #   drillIn (level ↓) — thin switch; per-tool drill funcs in update_drill_{pages,wal,statements,…}.go
  update_load.go     #   loadCurrent (issue load Cmd for the current screen)
  update_sort.go     #   applySort + validSorts + cycleSort (sortMode in sort_modes.go)
  cmds*.go           #   tea.Cmd wrappers around pg.Client (query() helper)
  colregistry.go     #   generic colDesc/colSpec/colTable behind the C column pickers (queries/activity/tablestats/logs)
  view.go, view_*.go #   View() + per-level renderers
  *_columns.go       #   column registries for the C-picker tables (queries/activity/tablestats/logs/pgbouncer/diag)
  row.go, filter.go, layout.go, styles.go, keys.go
internal/humanize/   # Bytes(int64) → "12.34 MB"
internal/prefs/      # per-user UI prefs (JSON at ~/.config/pgdu, atomic write)
internal/sysmem/     # host memory stats (maintenance/system overview)
```

## Conventions

- **SQL lives in `internal/pg/queries*.go`** as `const sql<Name>`. Entity files just
  call `pool.Query`/`pool.QueryRow` (or the `collect`/`collectBestEffort` helpers in
  `query.go`) and scan into `types*.go` structs.
- **Identifiers interpolated into SQL go through `quoteIdent`/`qualifiedIdent`**
  (`columns.go`), or `fixIdent` for user-facing fix scripts. Never splice a raw name.
  Identifiers always come from catalogs or the `Table` struct, never from user input.
- **Errors wrap with context**: `fmt.Errorf("<op> in %q: %w", db, err)`.
- **Client methods take a ctx**: `func (c *Client) X(ctx, …) (…, error)`. TUI Cmds wrap
  reads in a 30 s timeout via `query()`; long-running or streaming work (REINDEX,
  VACUUM, diagnostic fixes, log loading) intentionally does not use it.
- **TUI state lives on `screen`**, not on `Model`. `Model` carries the stack of screens,
  the client, and shared widget state (spinner, help, keys, column prefs).
- **Sort logic is on `sortMode`**: `.label(desc)`, `.less(a,b)`, `.defaultDesc()`,
  `.name()`. Add a sort by extending the enum + those methods + `validSorts`.
- **Adding a new level**: type in `types*.go`, SQL in `queries*.go`, a `List*` in a new
  entity file, then a `level` value in `app.go` (+ `barReserve()` case in `layout.go`),
  a Cmd in `cmds*.go`, a handler in `update_msgs*.go`, a drill case in
  `update_drill*.go`, and a `case` in `crumbText()` (`view.go`) naming what the screen
  shows.
- **Comments explain the why**, never restate the code.

## Build / run

```sh
make build       # → ./pgdu
make test        # go test ./...   (integration tests need PGDU_TEST_DSN; skipped otherwise)
make lint        # golangci-lint run --fix + go fix
make deb         # Debian package (debian-pkg/)
./pgdu -U user   # libpq-style flags: -h host -p port -U user -d dbname --dsn URL
./pgdu --logs --log-file /var/log/postgresql/postgresql-17-main.log
```

Defaults match `psql`: no `-h` → Unix socket + peer auth. `PGPASSWORD` is read directly;
`~/.pgpass` is honoured at connect time.

Snapshots (`--snapshot-dir`, default `/tmp/pgdu-snapshots`, `PGDU_SNAPSHOT_DIR`) are
deliberately world-writable (dir 0o777, files 0o666) so any host user can manage anyone's.
Prefs (`~/.config/pgdu/prefs.json`, `PGDU_CONFIG_DIR`) are per-user; `prefs.Load` never
fails (missing/corrupt → empty).

## Things to know before touching code

- **Tools & levels**: `levelTools` is the root menu; each `tool` value drills through its
  own `level*` chain. Both enums live in `app.go`.
- **Breadcrumb**: `crumbs()` (`view.go`) emits exactly one crumb per stack screen — the
  root is the host (`cli.Config.HostLabel`, never `Target()`, which keys snapshots) — so
  Back always removes one crumb. Identity (db, schema, table, query id, rmgr, log file…)
  lives in the trail and never on the status row. A screen whose `tool` differs from its
  parent's gets a `tool:` prefix, the first db-scoped screen whose db the trail hasn't
  named gets a `(db)` suffix, cluster-wide tools never show their connection db. A
  table list that replaced the skipped single-schema picker is the db crumb; relations
  are `schema.name` only until the trail has spelled a non-`public` schema
  (`crumbScope.qualify`). Crumb
  text falls back to `screen.title`, then `levelLabel`, so a loading placeholder still
  has a crumb.
- **`loadCurrent()` clears `extPrompt` and `installing`** on entry, so any such state set
  before the async load lands is wiped. `applySort` runs after every load, so handlers
  that patch rows later (e.g. `bloatFilledMsg`) must match by name, not index.
- **`screen.table` is the source of truth** at `levelParts`/`levelColumns`.
- **One drill affordance**: `item.hasChildren` is cosmetic — `drillIn` decides by payload
  type — and paints the muted `↵` in front of the name through `drillMark` (`row.go`), the
  only place the glyph lives; every list renderer calls it (the generic column table adds
  the slot only when `anyDrillable`). A builder sets the flag to exactly what `drillIn`
  will do on that row (opens a screen or overlay, unfolds; not arm-a-confirm or pick).
  `enterLabel` (`keys_enter.go`) names Enter's destination in the footer per level and
  row and disables the key on leaves; cross-view key help reads `→ <where>`. Adding a
  level or a drill means touching all three.
- **Two-step confirm is one shared pattern**: reindex, snapshot delete, backend
  cancel/terminate, VACUUM, extension reset and diagnostic fixes all arm a `pending*`
  field on Enter; any other key cancels, `y` executes. Reuse it, don't invent a new flow.
- **Column-picker tables share one generic** (`colregistry.go`): each table is a registry
  of `colDesc` entries plus a static `colSpec` (registry, prefs key, sort fallback, picker
  title) and a `colTable` state on `Model` (`stmtTable`, `actTable`, `tblTable`,
  `logTable`/`logStatsTable`: visibility map seeded from prefs in `NewModel`, sort column
  by stable id, picker flag + cursor). `spec.visibleCols` *projects* the registry so
  `diagCols` and each row's `cellsFor` cells stay parallel by construction;
  `spec.syncSort` remaps the sort id after every rebuild; `spec.handleKey`/`renderConfig`
  are the picker. A new table = a `*_columns.go` registry + spec + one `colTable` field.
- **Per-tool screen state lives in sub-structs** (`screen.stat`, `.act`, `.wal`, `.log`,
  `.pgb`, `.buf`, `.pages`, `.maintenance`, `.desc`, `.reindex`, `.tbl`, `.progress`,
  `.lock`, `.parts` — the `*State` types below `screen` in `app.go`); only the
  list/nav core, the load context and the generic table infra (`diag*`) are top-level.
- **Byte decoding is not UI**: tuple layout segments, index-key/jsonb/TOAST decoding live
  in `internal/pageinspect` and return plain strings/segments; styling stays in tui.
  `/proc` and stat(2) reads go through `internal/procfs`, never a per-package
  `_linux.go` pair. A varlena payload that is itself a compressed stream (`blob.go`) is
  unwrapped before the text/hex rules apply — magic-number sniffed, inflated to a
  bounded prefix and shown as `zstd:`/`gzip:`/`zlib:` + content, degrading to
  `zstd · 4.48 KB raw` when only the frame header can be read. zstd is the sole reason
  the binary has a non-pg dependency (`klauspost/compress`, isolated in `blob_zstd.go`).
- **Best-effort enrichments must degrade, never break**: the tuple row-content query
  (`sqlHeapTuplesCols`: pk + picked columns as visible-row text and as page bytes →
  `sqlHeapTuplesDecode` → plain `sqlHeapTuples`) and the HOT-chain hop (`fillHotChains`,
  `sqlHeapRedirectKeys`) fall back to the plain view when the catalog lookup or join
  fails. The tuple list's `C` pick (`pages.tuplePick`) is part of that query, so a toggle
  reloads the page; it is deliberately not persisted. For index entries,
  a NULL heap projection means HOT-redirected, *not* dead — only `IndexTuple.Dead` earns
  the `dead` tag. The WAL block payload view (`pg.WALBlockDetail`, `tui/wal_detail.go`)
  is the same shape: the record bytes are mandatory, relation kind / column layout /
  pageinspect decode of the page image all degrade into `DecodeNote`.
- **WAL overview is one list with two tables**: `levelWAL` owns the rmgr rows
  (`wal.rmgrs`) and the by-relation rows (`wal.rels`, chained off the overview load
  because the block scan needs the resolved window). `applySort` rebuilds `s.items`
  via `buildWALItems`; its `walSectionRow` lines (Σ, title, column header, notes) are
  inert (the only rows `skipInertRow` steps over) and kept visible under a filter.
- **Log analyzer** (`internal/pglog`): `pglog.Entry` text fields are `[]byte` sub-slices of the window buffer;
  convert to string only what you render. The `levelLogs` screen owns `log.report`;
  child screens find it via `findLevel(levelLogs)` and are re-pointed on each refresh.
  The groups pane orders itself, so `applySort` special-cases `levelLogs` with
  `diagCols == nil`. Section header rows carry `logSection`; the cursor rests on them and
  Enter folds the section (`log.collapsed`, keyed by category, applied in
  `buildLogGroupItems` so the fold survives every rebuild). `skipLogHeader` lands the
  cursor on the first group after a load or pane/mode switch.
  `Entry.Group` indexes `Report.Groups` (set by `Aggregate`); use it to walk a group's
  full membership, `Group.Samples` is capped. The group screen's tab (`log.params`)
  swaps the entry list for a generic `diagCols` table keyed by `pglog.ParamKey`.
  `log.cat`/`catOn` narrow both panes to one category (`f` cycles it, distinct from
  the text filter). The `l` cross-links (`logLinkFor`/`openLogLink` in
  `update_keys_openlog.go`: WAL → checkpoints, top queries → slow queries on the slow
  pane, system overview → the whole log) push the picker with `log.autoOpen`, and
  `onLogFilesLoaded` then opens the current server log without a pick. Adding a link
  is one `logLinkFor` case.
- **PgBouncer** lives in `internal/pgbouncer`, reached as `pg.Client.PgBouncer`. Instances
  on one host share a TCP port via so_reuseport, so always address by
  `unix_socket_dir/.s.PGSQL.<port>` when the socket exists. The console only speaks the
  simple protocol and rejects the pool's AfterConnect `SET`, so `pgbouncer.Client` keeps
  one raw `pgx.Conn` per instance (never `PoolFor`), kept open because pgbouncer logs
  every console login. `Show` allowlists SHOW commands; keep it read-only. Results are
  `diagres.Result` (aliased as `pg.DiagResult`) and ride the diagnostic-result machinery
  keyed by `screen.diagVisKey()`. Only `pg.Client.DiscoverPgBouncers` stays in pg: it
  adds the "is my own connection behind a pooler" source, which needs the pool.
- **Top-queries snapshots**: the `L` browser carries virtual anchors (`@now`,
  `@session`, `@reset` in `cmds.go`) that are never backed by a file — every path that
  loads or diffs a snapshot must special-case them. Snapshots older than the live
  `stats_reset` are filtered out, not warned about. Frozen windows (`stat.endSnap` set)
  skip live re-sampling in `loadCurrent`/`onStatementsTick`. The same browser is the
  tool's entry screen (`stat.entry`, pushed by `databaseChildScreens` over a
  not-yet-loaded `levelStatements`): no `@now` row, the pick is always the window's
  start, and Back pops the unloaded table too. The session anchor is captured on the
  first live sample whatever base was picked, so `@session` exists after a snapshot or
  cumulative entry as well.
