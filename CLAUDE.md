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
  sizehistory.go     #   size samples: capture, save/list/load, prune (growth tool)
  growth.go          #   DiffSizes + the scope roll-ups over two samples
  snapfile.go        #   the gzip-JSON file layer both sample kinds share, plus the
                     #   keep-newest-N sweep and the once-a-day predicate over it
  pgbouncer.go       #   DiscoverPgBouncers: pgbouncer.Client.Discover + the behind-a-pooler check
  logdiscover.go     #   log file discovery (pg_current_logfile / pg_ls_logdir) + server-side LogSource
  diskspace.go       #   free space under data_directory: same-host proof + procfs.Statfs
internal/diagres/    # generic column/row result table (kinds, cells, pgx row scan) shared by pg and pgbouncer
internal/pgbouncer/  # pgbouncer console: ini parser, discovery, simple-protocol conns (own Client, no pool)
internal/pageinspect/# byte decoders over pg row types: tuple layout, index keys, jsonb, TOAST
                     #   pointers, compressed blobs (gzip/zlib/zstd inside a bytea)
internal/procfs/     # Linux-only host reads (/proc process list + per-PID stats, stat inode, statfs) with no-op fallbacks
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
The directory holds two kinds: query snapshots (`*.json.gz`) and size samples
(`*.sizes.gz`). Both accrue by themselves: opening the top-queries or growth tool
captures one for that database when none was captured on today's **UTC** calendar
day, and that capture prunes to the newest `--sample-retention` (default 30) per
`(target, database, kind)`. `PGDU_AUTO_SAMPLE=0` / `--no-auto-sample` turns it off.
On most distros that path is tmpfs or cleaned at boot, so point it somewhere
persistent for real history.
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
  cancel/terminate, VACUUM, extension reset, buffer eviction and diagnostic fixes all
  arm a `pending*` field on Enter; any other key cancels, `y` executes. Reuse it, don't
  invent a new flow. Buffer eviction (`X` on the buffers screens, PG18's
  `pg_buffercache_evict_relation`/`_evict_all`) is the one that costs other sessions:
  the per-table row evicts the heap only — `pg_buffercache_evict_relation` does not
  reach the TOAST heap or the indexes the folded row counts, which is why `bufEvictName`
  says so — and `evict_all` goes cluster-wide, so its banner spells that out.
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
- **Scrolling bodies keep their layout between frames**: `View` runs per key press, so
  a body that scrolls must not re-lay-out what did not change. The query detail
  memoizes its two statements (`sqlLayout`, keyed by text + width, so a new sample
  call invalidates itself) and assembles the body as lines (`lineBuf` →
  `scrollLines`); the log entry keeps its whole body (`logEntryBody`), which is why
  `logEntryText` may read nothing but the entry and the width — and why the report
  is in the key: a live tail re-parses the newest record in place. Tokens are styled
  through `sgrPair`, not `Style.Render`. `view_scroll_test.go` carries the
  benchmarks (`go test ./internal/tui -bench 'Scroll|Open'`).
- **Byte decoding is not UI**: tuple layout segments, index-key/jsonb/TOAST decoding live
  in `internal/pageinspect` and return plain strings/segments; styling stays in tui.
  `/proc` and stat(2) reads go through `internal/procfs`, never a per-package
  `_linux.go` pair. A varlena payload that is itself a compressed stream (`blob.go`) is
  unwrapped before the text/hex rules apply — magic-number sniffed, inflated to a
  bounded prefix and shown as `zstd:`/`gzip:`/`zlib:` + content, degrading to
  `zstd · 4.48 KB raw` when only the frame header can be read. zstd is the sole reason
  the binary has a non-pg dependency (`klauspost/compress`, isolated in `blob_zstd.go`).
- **Server-version differences have four tools, in this order of preference**:
  (1) write SQL every supported version accepts; (2) read a column a newer server
  added through `to_jsonb(row) ->> 'col'`, which yields NULL instead of failing
  the statement (`queries_diag_server.go`, `sqlMaintIO`, `sqlTableStats`,
  `sqlDescribeStats`) — NULL then means "no such counter", distinct from zero;
  (3) `Client.serverVersion` (`version.go`, cached once for the whole client,
  `pgVer18`/`pgVer19`) for a function or view that does not exist at all, where
  there is nothing to degrade to — `Diagnostic.MinVersion` is this gate for a
  diagnostic and refuses with a version-naming error; (4) probe the catalog
  (`to_regclass`) when even the version is not a safe predictor, as the progress
  UNION does: `progressBase(progressCaps)` assembles the `pg_stat_progress_*`
  arms the server actually has, because one missing view fails the whole
  statement and takes the screen with it, and `Client.diagSQL` resolves the
  diagnostic's SQL per server while the registry keeps the PG17/18 shape for
  the query view to display. Prefer the lowest-numbered tool that works.
- **Best-effort enrichments must degrade, never break**: the tuple row-content query
  (`sqlHeapTuplesCols`: pk + picked columns as visible-row text and as page bytes →
  `sqlHeapTuplesDecode` → plain `sqlHeapTuples`) and the HOT-chain hop (`fillHotChains`,
  `sqlHeapRedirectKeys`) fall back to the plain view when the catalog lookup or join
  fails. The tuple list's `C` pick (`pages.tuplePick`) is part of that query, so a toggle
  reloads the page; it is deliberately not persisted. TOAST chunk tuples share the
  byte-layout overlay (their descriptor is `chunk_id`/`chunk_seq`/`chunk_data`); the
  `chunk_data` value pane reassembles the whole value (`ReadToastValue`, capped, owner
  column types as hints) and `pageinspect.DecodeToastValue` sniffs the pglz/lz4
  `va_tcinfo` header — the toast table doesn't record compression, so a value counts as
  compressed only when the pure-Go decoder (`pglz.go`/`lz4.go`) inflates it to exactly
  the declared size — then decodes jsonb/text/hex, degrading to a note + stored hex.
  There is no separate toast-value screen. For index entries,
  a NULL heap projection means HOT-redirected, *not* dead — only `IndexTuple.Dead` earns
  the `dead` tag. The WAL block payload view (`pg.WALBlockDetail`, `tui/wal_detail.go`)
  is the same shape: the record bytes are mandatory, relation kind / column layout /
  pageinspect decode of the page image all degrade into `DecodeNote`.
- **WAL overview is one list with two tables**: `levelWAL` owns the rmgr rows
  (`wal.rmgrs`) and the by-relation rows (`wal.rels`, chained off the overview load
  because the block scan needs the resolved window). `applySort` rebuilds `s.items`
  via `buildWALItems`; its `walSectionRow` lines (Σ, title, column header, notes) are
  inert (the only rows `skipInertRow` steps over) and kept visible under a filter.
- **Buffers list is one load, two views**: `RelationBufferStats` returns a row per storage
  relation (heap; toast = TOAST heap + its index; each index) with `TableBufferStat.Rel`
  set, kept on `buf.rels`. `buildBufferItems` folds it per table via `pg.RollUpBufferStats`
  (the default, also the describe panel's figure) or lists it as-is when `buf.byRel`
  (`tab`), so the split never re-scans pg_buffercache and both Σ rows agree.
  OID/Schema/Name stay the owning table's; rows key by `TableBufferStat.Key()`, and the
  detail histogram takes `whole` = `Rel == nil`. A split row's `item.name` is
  `table › relname` (the `/` and name-sort key; the table bare in `public`, else
  `schema.table`, per `bufTableName`), drawn as separate table and relation columns
  sized by `bufSplitWidths`.
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
  The entry view ends the statement with a `SAMPLE CALL`: `pglog.SubstituteParams`
  splices the logged bind values (`BoundParams` — the `Parameters:` DETAIL line, or the
  list an error carries on CONTEXT) into the `$n` placeholders. Bound values only, since
  `Params`' inlined-literal fallback would otherwise be spliced back into its own SQL.
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
- **System overview is quiet by default** (`view_maintenance*.go`): a finding is printed once —
  in the recommendations panel when it is actionable (`maintView.panel`), inline via `note()`
  only when it is an Info note without a fix — and the metric it is about is only coloured
  (`graded`/`fill`). A setting row goes through `v.guc(label, guc, adviceKey)`, which drops a
  default-valued knob (`pg_settings.reset_val = boot_val` → `MaintenanceInfo.SettingDefault`)
  nothing grades; a check that passed appends its label to the section's `ok` list and
  `v.section()` folds it into the title as `✓ a · b`. `v` (`Model.maintVerbose`, persisted as
  `prefs.OverviewVerbose`) shows every row, including the verbose-only observability section.
  Adding a row means deciding which of the three it is. The statistics block's
  extension rows distinguish *absent* from *absent here*: both extensions keep
  cluster-wide counters in shared memory, so when the library is preloaded
  (`ExtCapacity.Preloaded`, probed only when the extension is missing) the row
  says it is collecting and the overview arms the shared non-blocking
  `extPrompt`, which is what makes the global `i` run the CREATE EXTENSION.
  Action rows are the recommendations first, then the four stats-reset rows of the closing statistics block (`actionRows`); the fix
  line renders under the cursor row only.
- **The parts panel follows the overview's rule, with its own bands**: the
  maintenance section under the parts list (`view_parts_maint.go`) prints a
  `gaugeBar` row only for a metric a finding fired on and folds every healthy
  one into the header as `✓ vacuum 3d · dead 0%` — the figure survives, the
  line does not — so a healthy table is two lines. There is no verbose toggle:
  `v` is VACUUM here. The judgement is `pg.TableAdvice` (`advice_table.go`),
  the table-scoped sibling of `MaintAdvice`, deliberately kept out of its set so
  a per-table finding can never reach the overview's recommendations panel.
  Its bands grade **work pending, not the clock** — a static table nobody has
  vacuumed in a year is not a fault — and a counter *at* its autovacuum
  threshold is a worker being due, so rows start at twice it
  (`tableBehindFactor`), or at a fifth of the table for dead rows, below
  `tableMinRows` never. Only `AdviceWarn`+ earns a row; the one Info rule
  (freeze) folds with its note. Freezing is graded by the cluster's own
  `freezeLevel`, which is why an age at `autovacuum_freeze_max_age` is routine
  here too — the panel used to paint it red — and the freeze bar measures the
  way to the failsafe (`FreezeDangerFrac`), not to the next routine cycle,
  while the figure shows the unclamped `FreezeFactor`. The four statements the
  panel's hint lines used to advertise now live in the footer
  (`maintKeysInFooter`), which is their only home.
- **Free space is a host read, and a gate**: Postgres exposes no free-space
  function, so `pg.Client.DiskSpace` reads `data_directory` out of `pg_settings`
  (absent, not an error, for an unprivileged role), proves the path is the
  *server's* by stat'ing `PG_VERSION` on both sides — `pg_stat_file` vs the local
  file, falling back to `cli.Config.LocalServer` where that function is not
  granted — and then `procfs.Statfs`es it. Anything else wraps
  `ErrDiskSpaceUnknown`, which is a normal answer (remote host, no privilege,
  non-Linux) and renders as nothing at all. The figure covers that one
  filesystem: a tablespace or a relocated `pg_wal` is on another, so every
  caller names the path. `Model.disk` holds it cluster-wide, re-probed by
  `loadCurrent` on `toolDisk` screens at most every `diskSpaceMaxAge`. Two
  consumers: the status row (`diskSpaceLabel`, graded) and the REINDEX gate —
  `reindexNeedBytes` is a second copy of every index the rebuild touches (the
  **sum** for the table form: REINDEX TABLE CONCURRENTLY swaps them all in at
  the end), `reindexRefusal` refuses to arm at need ≥ free and
  `reindexTightNote` warns past half. An unknown never refuses: a key that dies
  wherever pgdu cannot see the disk is worse than no gate.
- **Growth / size history** (`toolGrowth`, `--growth`): pgdu accrues its own history,
  since the server keeps none. A *sample* (`internal/pg/sizehistory.go`) is one
  per-relation size capture of one database; a *window* is two samples, diffed by
  `DiffSizes`. Samples share the snapshot directory with query snapshots but are a
  separate kind: `snapfile.go` holds the one `snapKind` registry both listers claim
  their files through, and the meta cache is **per kind** (`metaStore[T]`) because a
  single path-keyed map would have each lister's disappeared-files sweep evict the
  other's entries. Rows key on the logical identity `(database, schema, relname)` —
  OID survives `pg_upgrade` but not a dump/restore — and every consequence is a tag,
  never a silent correction: `new` carries its whole size as the delta, `gone` a
  negative one, `rewritten` (relfilenode changed) leaves the delta alone because the
  shrink is real disk, and a rename reads as one `gone` plus one `new`. A window
  under 60 s is flagged `ShortWindow`, not refused — the byte deltas are measured
  at any spacing and only the per-day extrapolation suffers; a reversed pair is
  ordered by time and flagged; a straddled
  stats reset clears `RowsKnown` but never touches the byte deltas, because sizes are
  measured and rows are counted. `RowsKnown` folds by **AND**, and `accumulate` can
  only ever clear it, so `foldBy` seeds it true for every scope: a fold that starts
  from a bare `GrowthRow` can never recover it, which is what blanked the rows
  columns on the schema and database scopes while the relation and partition
  scopes showed them. *Straddled* means the two samples disagree about
  `pg_stat_database.stats_reset`, not that it is NULL: NULL is a database nobody has
  ever reset, which is the normal case and settles that no reset happened between
  them. Reading two NULLs as two unknowns blanked the rows columns on every such
  cluster. The partition roll-up walks `ParentOID` client-side
  (`RollUpGrowth`), *not* `sqlDiagTableShowSize`'s recursive CTE, because the tree
  differs between the two samples and only the client holds both files.
  Three capture modes, and `sizeCaptureMode` names them rather than passing a pair
  of bools, because the one combination that must never exist is a key press that
  deletes history: `pgdu --sample` is the cron one-shot (`runSample` in `main.go`,
  its own 10-minute budget, path to stdout) and prunes; the daily auto capture on
  tool open prunes too, because it is pgdu's own doing and pgdu owns bounding what
  it created; `S` is written and never pruned. All three keep every relation — the
  size floor is cron's knob alone, and a window spanning a floored file and an
  unfloored one is already explained by `GrowthResult.FloorChanged`.
  The four scopes are Tab-cycled in-place views over one row set, and because a roll-up row is a `pg.GrowthRow` too they share **one**
  column registry — unlike the top-queries roll-ups, whose group rows are a different
  type and need a second. The picker duplicates the snapshot browser's idiom rather
  than its code (that one is welded to `stmtState`); what differs is a bytes bar,
  `@now` as the only anchor, and a compatibility check that adds `DatabaseOID`/
  `SystemID` — where **zero means unknown and must read as compatible**, since
  `pg_control_system()` is revoked from PUBLIC.
  Every *change* column uses one of the `Diag*Delta*` kinds, which is what makes
  the explicit `+`/`-` safe: `diagCellText` (`view_diag.go`) is the single place a
  numeric cell becomes text, so the width pass, the data row, the headline-bar
  number and the Σ footer cannot disagree about the spelling — before the kinds
  existed the bar re-derived its number from `Num` and would have dropped a sign
  the cell had added. `deltaStyle` colours by **direction, not judgement**: a
  shrink green, growth graded against the column's largest absolute change,
  zero muted. An absolute figure (`size`, `base`, `rows`) must never take a delta
  kind, and `signedCount` formats the magnitude itself rather than handing a
  negative to `formatRows`, whose `?` means "unknown" on a counter, not "fewer".
- **Top-queries snapshots**: the `L` browser carries virtual anchors (`@now`,
  `@session`, `@reset` in `cmds.go`) that are never backed by a file — every path that
  loads or diffs a snapshot must special-case them. Snapshots older than the live
  `stats_reset` are listed and tagged `pre-reset`, not hidden: the reset says nothing
  about the counters between two captures that both predate it, so they still pair
  with each other. What the old filter guaranteed is now earned per pair by
  `snapStraddledReset` (`update_drill_snapshots.go`), which refuses a window with a
  reset inside it — the reset to compare against comes from the window's **end** (the
  live `stats_reset` for `@now`, the snapshot's own recorded one otherwise), which is
  what makes a chain of resets tractable with only the last one in hand. A zero on
  either side is unknown and stays permissive. The set is collected into
  `stat.preReset` by every listing, counted in the browser's header, and `P` deletes
  it behind the shared two-step confirm. Frozen windows (`stat.endSnap` set)
  skip live re-sampling in `loadCurrent`/`onStatementsTick`. The same browser is the
  tool's entry screen (`stat.entry`, pushed by `databaseChildScreens` over a
  not-yet-loaded `levelStatements`): no `@now` row, the pick is always the window's
  start, and Back pops the unloaded table too. That first listing is also where the
  daily auto capture is decided (`first && entry`, so the re-list it triggers cannot
  fire a second), and the predicate counts **every file on disk**, pre-reset ones
  included — the day's quota is about what was written, not what is pairable, and a
  reset since this morning's capture would otherwise make today look empty and
  re-capture on every open. The session anchor is captured on the
  first live sample whatever base was picked, so `@session` exists after a snapshot or
  cumulative entry as well.
- **Top-queries roll-ups are in-place views, not levels**: `Tab` cycles `stat.view`
  (queries → by table → by type) on the one `levelStatements` screen and
  `rebuildStatementItems` re-projects the same `stat.rows` (`queries_groups.go`: fold by
  `stmtView.key`, metric columns wrapped from the list's registry, own picker
  `stmtGroupTable`/`colPrefsQueryGroups`). Enter on a group row sets `stat.group`, a
  structured narrowing of the list (a type group has no text the `/` filter could match);
  Esc unwinds view, then narrowing, then pops. `time%` always divides by the whole window.
  The grouped views' bar rides the sort column (`syncStmtBar`; the width memo must be
  dirtied when it moves). Exactly one of `stat.cols`/`stat.groupCols` is non-nil, which is
  how `cycleSort` picks the sort memory to write.
- **Sample calls are never guessed**: the query detail's sample call comes only from captured
  values — a pg_qualstats example, its per-predicate constants covering every `$n`
  (`BuildSampleCall` returns "" otherwise; `MapQualConstants` matches each `$n` by column,
  scope-resolved table and operator via `paramSlots`, and keeps one AND group's values from
  a single `uniquequalid` combination), or a call the server logged with its binds
  (`pglog.LatestBoundCall`, matched by query id or `NormalizeCall`, read from the current
  server log and cached in `Model.logCall`) — never table samples or synthesized literals.
  The one filled-in value is an EXISTS subquery's lone select-list `$n` (`inertParams`,
  `ParamInert`): it becomes `1`, which cannot change the result or the plan.
  `stat.sampleCall != ""` is the single gate for `↵`/`E` and the real (non-generic) EXPLAIN;
  `sampleSource` only labels the origin.
