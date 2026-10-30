# Top queries & disk usage — remaining work

## Context

An audit of pgdu's two oldest surfaces — the **disk-usage browser** (`toolDisk`:
`levelDatabases → levelSchemas → levelTables → levelParts → levelColumns`) and the
**top-queries tool** (`toolQueries`: `levelSnapshots` entry → `levelStatements` →
`levelStatementDetail` → samples / result) — against the bar the newer tools set
(a `?` reference per view, a column registry with a `C` picker, graded colours, a
findings panel, degraded-not-broken reads) and against what PG17/18 actually exposes.

The correctness pass is done (see below). What is left is coverage and new surfaces.
Line references are current as of this rewrite.

### Already landed (2026-10-30)

**Correctness** (`ac2b8ab`): pooled stddev; the `toplevel` filter; no more
fabricated non-btree bloat (`~` marks a statistics estimate, REINDEX arms only on a
measured one); the size cap applied to indexes; per-part timeout and bounded fanout in
`FillBloat`; the `sqlBloatProbe` short-circuit; the stale snapshot path.

**Tier A — coverage** (`4adf975`, `c394b40`, `69d3c0e`), all of it:
rate columns (`calls/s`, `rows/s`, `wal/s`, `active`) · `local_blks_*` columns ·
the `to_jsonb` counters (jit_ms, jit%, parallel%, generic%, wal_buf_full) with their
nullable diff path · `stats_since` eviction marking (`↺`) · the `minmax_only` reset
(`M`) · the silent degradations named · bloat on the tables list with `sortByBloat` ·
real bloat for GIN/GiST/hash plus the GIN pending list · a `?` reference for all five
disk levels · `a` ANALYZE, `F` VACUUM FREEZE, `R` REINDEX TABLE CONCURRENTLY, and the
reclaim line.

Still unverified against a live server: every SQL change above is covered by shape
tests and the compiler only. The pooled stddev, the `toplevel` filter, the rewritten
`sqlBloatProbe`, `sqlBloatTablesEstimate`, `pgstattuple` on non-btree indexes and the
whole `to_jsonb` block each want one run on PG17 and one on PG18.

## Remaining gaps

### Top queries

1. **No index advice**, although pg_qualstats is already a dependency and already read.
   `pg_qualstats_index_advisor()`, `nbfiltered`, `execution_count` and `eval_type` are
   all unused; `hypopg` is absent. The one place pgdu reads an advisor's input and then
   doesn't advise. → C8
2. **No auto_explain cross-link in the query detail.** The log analyzer already parses
   auto_explain plans with the same heat grading, and `Model.logCall` already searches
   the current log for this query id. "The last real plan the server logged for this
   statement" next to the EXPLAIN is nearly free. → C8
3. **Stats are current-database only**; `dbid` is filtered on and then zeroed. No
   cluster-wide view, although `Client.PoolFor` already opens a pool per database lazily.
4. **No per-role breakdown** — `userid` is collapsed by construction, so a `by user`
    roll-up is impossible without changing the aggregation.
5. `pg_qualstats` samples are capped at `LIMIT 50`, which caps both the `p` browser and
    the constant pool `MapQualConstants` draws from.
6. **No pg_stat_kcache** (real CPU time and physical I/O per statement) — the only way
   to tell CPU-bound from I/O-bound without guessing from `io_ms`.

### Disk usage

1. **Nothing tracks size over time.** No growth rate, no bytes/day, no projection, no
   size diff. The snapshot machinery exists but is exclusively pg_stat_statements. → C9
2. **Partitioned tables are not handled in the browser.** `sqlTables` includes
   `relkind = 'p'`, so a partitioned parent is a ~0-byte row and every partition is an
   unrelated sibling; on a heavily partitioned schema the list is as long as the
   partition count and the parent is invisible at `size↓`. The roll-up SQL already
   exists — but only as the `table_show_size` diagnostic
   (`internal/pg/queries_diag_table.go:123`). The README's "drill from databases into
   schemas, tables, partitions, indexes" is not true today. → C10
3. **`heap + idx + toast ≠ size`** at `levelTables`: `heap` is `pg_relation_size` (main
   fork only), so the heap's FSM/VM lands in `total` and in no breakdown column. It is
   drawn as the bar's muted remainder and never named. The three-argument
   `pg_relation_size(rel, 'fsm'|'vm'|'init')` is never used anywhere.
4. **No tablespace view at all** — no `pg_tablespace_size`, no `reltablespace` column,
   no per-location grouping. On a cluster with a separate index or archive tablespace,
   pgdu cannot answer "which mount is filling up". → C11
5. **No cluster-level physical picture**: no data-directory size, no filesystem free
   space, no `pg_ls_tmpdir` (on-disk temp spill — only the `temp_bytes` counters), no
   archive status dir. `pg_wal` size exists, but only as one row in the system overview.
6. **Parts sizes are stale by one level.** `TableParts` reuses `Table.HeapBytes` and
   `ToastBytes` from when the *tables list* loaded; only index sizes are re-measured, so
   `space` on the parts screen does not refresh the heap figure.
7. **No `LIMIT` and no server-side paging** in any foundation query. `sqlSchemas` calls
    `pg_total_relation_size` once per relation in the database on every open and
    refresh; a schema with 50 000 tables builds and re-sorts 50 000 items on every
    `applySort`.
8. **VACUUM still has no option picker.** `INDEX_CLEANUP`, `PARALLEL` and
   `DISABLE_PAGE_SKIPPING` are not reachable; `v` and `F` are two fixed forms. A picker
   overlay is the remaining piece. → C7
9. **Column level stops at `avg_width`/`null_frac`.** `n_distinct` and `correlation`
    are read by the index diagnostics but never shown next to the column they describe,
    although they are what make a column a BRIN or CLUSTER candidate; MCV/histogram are
    unused everywhere. No per-column compression ratio, although the TOAST decoder
    already knows pglz/lz4.
10. Sequences and foreign tables (`relkind` `S`/`f`) are outside every size sum, because
    empty schemas are dropped deliberately (`internal/tui/update_msgs.go:77`). Worth a
    line in the new `?` overlay rather than a code change.
11. `heapPageBlockSize` is hardcoded `8192` in the TUI (`internal/tui/items.go:26`)
    although the SQL side reads `current_setting('block_size')`.

---

## Ranked next steps

Tier A is done. What is left:

**Tier B — new surfaces.** C9 (size history — the big one), C10 (partition roll-up in
the browser), C11 (tablespace level), C8 (index advice from pg_qualstats, the
auto_explain cross-link).

**Tier C — the rest.** The VACUUM option picker (the piece of C7 not built);
cluster-wide and per-role query roll-ups; column-level `n_distinct` / `correlation`;
`pg_stat_kcache`; the qualstats `LIMIT 50`; `heapPageBlockSize`; server-side paging on
the foundation queries.

**The structural decision is now overdue.** The disk levels are still the only major
tables not on the generic column registry (`internal/tui/colregistry.go`). Their columns
are hand-plumbed as fixed `item` fields with hand-computed widths, so the bloat column
just added had to touch `items.go`, `row.go` (header, row, Σ totals), `update_sort.go`
and `update_msgs.go` — and the growth columns of C9 phase 4 will pay the same toll
again. Migrating `levelTables` onto `colSpec`/`colTable` is a self-contained refactor
that makes the next five columns nearly free and gives the flagship view the `C` picker
every other tool has. The generic renderer already has a headline bar column
(`screen.diagBarCol`) and already adds the `drillMark` slot when any row is drillable;
what it does **not** have is the disk view's *segmented* bar (`renderSegmentedBar`,
`internal/tui/row.go`), which is the identity of the view and must survive.

---

## Implementation notes

### C8 — Index advice and the auto_explain cross-link

A new level under `toolQueries` fed by `pg_qualstats_index_advisor()`, reusing the
diagnostic-fix pattern to generate `CREATE INDEX CONCURRENTLY` behind the usual
two-step confirm, optionally validated with hypopg inside a rolled-back transaction.
Separately: show the most recent auto_explain plan the server logged for this query id
next to the EXPLAIN pane — the log analyzer already parses and heat-grades those plans,
and `Model.logCall` already does the lookup.

### C9 — Size history / growth

"What is growing, how fast, and when does it bite?", per database, schema, table and
index. The server keeps no size history, so pgdu has to accrue it.

**Samples.** Reuse the snapshot *mechanism*, not the snapshot *store*:

- **Immutable timestamped sample files**, written the way `SaveSnapshot` writes query
  snapshots (gzip'd JSON, never rewritten, header-first streaming listing with the
  `(modTime, size)` memo cache). An append-only log was the alternative and is the wrong
  call: the growth table only ever needs **two** files, so appending buys nothing and
  costs a file-locking story the codebase has no precedent for. Immutable
  single-writer files mean two concurrent samplers produce two files, not a corrupt one.
- **A cron-friendly `--sample` mode**: capture, write, prune, print the path, exit; no
  TUI. The "no daemon" rule means history cannot accrue while nobody is looking unless
  there is a one-shot, and without it the growth table is empty on every first run. It
  branches in `main.go` before `tea.NewProgram`, reusing `cli.Parse` and `pg.New`, and
  deliberately does **not** wrap the capture in `query()`'s 30 s budget (the same
  exemption REINDEX/VACUUM/log loading take).

**Directory — the one deliberate break with convention.** Query snapshots live in
`$TMPDIR/pgdu-snapshots` at `0o777`/`0o666` (`internal/cli/config.go:273`), right for
short-lived shared scratch and wrong for a year of history: on most distros `/tmp` is
tmpfs or cleaned at boot, so the history would quietly vanish, and any local user could
plant or truncate a sample. Add `--size-dir` / `PGDU_SIZE_DIR` defaulting to
`$XDG_DATA_HOME/pgdu/sizes` at `0o755`/`0o644`, and let a team that wants shared-scratch
semantics point it at a shared path. **This is the decision most worth explicit
sign-off.**

**Format**: `<sanitized-db>-<UTC>.sizes.gz`. The extension matters — `ListSnapshots`
matches `strings.HasSuffix(e.Name(), snapshotExt)` where `snapshotExt = ".json.gz"`
(`internal/pg/snapshots.go:200`), so a `*.sizes.json.gz` in a shared directory would be
listed as a query snapshot and silently skipped or half-parsed. Use `.sizes.gz` *and*
add an explicit skip.

```go
type SizeSample struct {
    Version, BlockSize      int64
    Target, Database        string   // Target(), not HostLabel() — see below
    DatabaseOID uint32; SystemID uint64 // identity guard; SystemID 0 = refused
    CapturedAt, StatsReset  time.Time
    Databases  []DBSize   // per-database totals, so the database scope works from one db
    OtherBytes int64      // what --sample-min-bytes folded away, so Σ stays honest
    RowCount   int        // serialized BEFORE Rows: header-first listing stops here
    Rows       []SizeRow
    Indexes    []IndexSize // nil unless --sample-indexes
}
```

`SizeRow` carries `oid, schema, name, relkind, relfilenode, parent_oid, partbound,
heap/idx/toast/total, reltuples, n_live, n_ins, n_del, n_ins_since_vacuum` with short
JSON tags — a deliberate divergence from `Snapshot`'s "no struct tags" comment, because
`SizeRow` is the high-cardinality part and short keys roughly halve the pre-gzip size.

**Cardinality and retention.** ~10 000 relations ≈ 1.2 MB JSON ≈ 220 KB gzipped. Two
levers: `--sample-min-bytes` (relations below the floor fold into `OtherBytes`) and a
prune-on-write policy — every sample < 7 days, newest per day to 90 days, newest per ISO
week to 2 years, capping a database at ~140 files ≈ 30 MB/year. `PruneSizeSamples` runs
best-effort after every successful save and swallows its errors the way the existing
`Chmod`s do. Because retention drops *whole* samples, every retained file stays
complete and self-describing and any pair diffs.

**Row identity across time**: key on the logical identity `(database, schema, relname)`,
carrying `oid`, `relfilenode` and `relkind` as attributes. Logical identity beats OID
(which survives `pg_upgrade` but not a dump/restore) and beats name-only (the row still
wants to notice a rewrite). Consequences surface as a tag column, never a silent
correction: absent in the baseline → `new`, with the full size as the delta (a new
40 GB partition *is* growth); absent now → `gone`, negative delta, hidden behind `v`;
`relfilenode` changed → `rewritten` (VACUUM FULL / CLUSTER / REINDEX / rewriting ALTER)
with the delta unmodified, because the shrink is real disk; a rename reads as one `gone`
plus one `new`, documented rather than fixed.

**SQL** — one statement per database in a new `internal/pg/queries_growth.go`, shaped
like `sqlTables` but database-wide, joined to `pg_inherits` for `parent_oid` and
carrying `pg_get_expr(c.relpartbound, c.oid, true)`. Every construct is
version-neutral, so this is CLAUDE.md's tool (1) — no version ladder.
`COALESCE(pg_*_size(...), 0)` is defensive against a relation dropped mid-scan (it
becomes a `gone` row next window). The partition roll-up is done **client-side** by
walking `parent_oid` to the root rather than with `sqlDiagTableShowSize`'s recursive
CTE — baking the roll-up into the stored sample would make the per-table scope
unrecoverable. `pg_control_system()` for `SystemID` goes through `collectBestEffort`:
it is revoked from PUBLIC, so an unprivileged role simply yields 0.

**UI — a new tool, with scopes as in-place views.** `toolGrowth` with three levels:
`levelGrowthWindow` (the sample/window picker and the tool's entry screen),
`levelGrowth` (the table), `levelGrowthHistory` (one relation across every retained
sample). The entry chain copies the top-queries pattern exactly: `databaseChildScreens`
returns the unloaded table with the picker pushed on top, the pick applies the window
and pops, Back from the picker pops the unloaded table too.

Scopes are **Tab-cycled in-place views over one row set**, not levels —
`table → schema → database → partition tree → index` — per CLAUDE.md's "top-queries
roll-ups are in-place views, not levels" rule, with Enter narrowing structurally and Esc
unwinding scope, then narrowing, then popping. One column registry
(`growth_columns.go`), one `colTable`, one prefs key; scope-specific columns use
`colDesc.available`'s existing gate rather than a second registry.

Columns: `name` (mandatory), `size` (now), `base` (then), `Δ`, `Δ%`, `per_day` (default
sort), `rows_day`, `days_to`, `tag`, `Δheap`/`Δidx`/`Δtoast`, `parts` and
`partition_span` on the tree scope, plus the fallback proxies gated on "no window".
Σ footer via the existing `diagTotalRow`. `BytesPerDay = Δbytes × 86400 / Δt`; `Δ%` is
undefined when the baseline is 0 (renders `new`, carries no `HasNum`, so it sinks under
the existing unknown-sorts-last rule); `days_to(ceiling)` takes its ceiling from a
key-set value persisted in prefs, later from filesystem free space on the data directory
(a new `procfs.FreeSpace` statfs paired with `SHOW data_directory`, degrading to `—` off
Linux or without privilege) — **never invent a ceiling**. A window shorter than 60 s is
refused with a notice rather than reported as noise; negative Δt (clock skew, a sample
copied from another host) orders the pair by time and tags the window.

**Sharing with the snapshot browser: share the file layer, duplicate the screen.**
*Generalize* into a new `internal/pg/snapfile.go`: the gzip-JSON write with the
`MkdirAll`+`Chmod` dance (parameterized by perms), the header-first streaming decoder
(`skipValue`/`countArrayElements`, generalized to "decode these header keys, stop at
this array"), `sanitizeDB`, and `DeleteSnapshot` (already kind-agnostic).
**The memo cache must be re-keyed**: today it is a package-level map keyed by path
holding `SnapshotMeta` (`internal/pg/snapshots.go:160`), and its "drop entries for files
that disappeared" sweep would evict the other reader's entries on every listing — key it
on `(ext, path)` and store `any`. *Duplicate* the picker screen (~130 lines):
`levelSnapshots` is inseparable from `stmtState` — `renderStatementSnapshots`,
`loadSelectedSnapshot`, `appliedWindowPaths`, `snapTime` and the two summary renderers
all reach for `findLevel(levelStatements)` and mutate
`baseSnap`/`endSnap`/`cumulative`/`sessionBaseline`. Copy the *idiom* (`◀ start`/`◀ end`
markers, the two-pick range, the compatibility flag); what genuinely differs is the bar
(bytes, not query count), the anchors (`@now` only — a size window has no in-memory
baseline and sizes have no reset) and the compatibility check, which adds
`DatabaseOID`/`SystemID` so a restored cluster is tagged `db recreated` rather than
silently diffed. *Share verbatim*: `m.pendingDeleteSnap` and its confirm block, which is
path-keyed and kind-agnostic — only the delete dispatch needs a `switch s.level`.

**First-run fallback**, so the tool is not dead on arrival. With zero or one sample the
growth columns read `—`, the header says *one sample — press `S`, or run `pgdu --sample`
from cron*, and three proxies turn on, all from data already read and all labelled as
proxies, never as growth:

- `n_tup_ins − n_tup_del` × bytes-per-row ÷ the age of `pg_stat_database.stats_reset` —
  a crude bytes/day;
- `n_ins_since_vacuum` × bytes-per-row — "unvacuumed insert bytes";
- **range-partition bounds as a free time axis** — the good one. For a RANGE-partitioned
  table, `pg_get_expr(relpartbound)` gives each partition's real interval, so
  bytes-per-partition *is* a growth curve from a single sample, no guessing from
  partition names. This is why `PartBound` is in the stored row.

A fourth signal worth cross-linking rather than computing: **WAL bytes by relation**,
which the WAL inspector already resolves across databases
(`internal/pg/queries_wal.go:173`) — the best short-horizon "what is writing" answer.

**Phases.** 1: format + capture + save/list/load/delete + prune + `--sample`; tool → db
picker → window picker → table, **table scope only**, two-sample diff, columns
name/size/base/Δ/Δ%/per_day. Ships alone and answers the question end to end.
2: Tab scopes, narrowing, Σ footer, the `new`/`gone`/`rewritten` tags, `v`, and Enter →
`levelParts` reusing the name→`pg.Table` resolve that already backs the top-queries `u`
jump. 3: the single-sample fallback, the ceiling and `days_to`, and
`levelGrowthHistory` with the existing `sparkline()` (`internal/tui/sparkline.go:16`,
already used by the pgbouncer pooler-stats pane) across every retained sample — the only
reader that opens more than two files, so it needs a streaming per-relation projection
rather than materializing a year. 4: `--sample-indexes` + the index scope, the `Δ/day`
column and a new `sortByGrowth` on `levelTables`/`levelSchemas`/`levelDatabases`, and
`l`-style cross-links from the sequences diagnostic and the WAL relation table.

**What complicates this today.** (a) the `.json.gz` suffix collision above — a hard
blocker if the two kinds ever share a directory; (b) the path-keyed, kind-blind meta
cache; (c) `levelSnapshots`' welding to `stmtState`, the concrete reason for a separate
level; (d) samples must key on `Target()`, **not** `HostLabel()` — CLAUDE.md is explicit
that `Target()` exists to key snapshots, and `HostLabel` resolves `os.Hostname()` for
socket connections, which differs between a container and the host, so a cron sampler
and an interactive run would disagree; (e) `loadCurrent()` wipes `extPrompt`/`installing`
and `applySort` runs after every load, so any chained second load that patches rows must
match **by key, not index** (the `bloatFilledMsg` precedent); (f) the 30 s `query()`
budget — `CaptureSizes` does ~4 `stat()`s per relation, so on tens of thousands of
relations with a cold page cache the TUI's `S` must degrade with a notice pointing at
`pgdu --sample`; (g) `tool.Name()` is load-bearing three ways (breadcrumb prefix,
`toolByName`, the `--<tool>` flag), so a missing `toolByName` entry makes `--growth`
silently fall back to the picker; (h) `levelLast` (`internal/tui/app.go:67`) drives test
loops in `view_breadcrumb_test.go` and `layout_test.go`, so the new levels must land
with their `crumbText`, `levelLabel`, `barReserve` and `enterLabel` answers in the same
commit — a useful forcing function.

**Files.** New in `internal/pg`: `queries_growth.go`, `types_growth.go`
(`SizeSample`/`SizeRow`/`GrowthRow`), `sizehistory.go`
(`CaptureSizes`/`Save`/`List`/`Load`/`Delete`/`Prune`), `growth.go` (`DiffSizes`,
`RollUp`, the derived rates), `snapfile.go` (the file layer extracted from
`snapshots.go`). New in `internal/tui`, one per the established per-feature split:
`growth_columns.go`, `cmds_growth.go`, `update_msgs_growth.go`,
`update_drill_growth.go`, `update_keys_growth.go`, `view_growth.go`,
`view_growth_window.go`. Touched: the standard "adding a level" sweep — `app.go`
(levels, `levelLast`, `toolGrowth`, `tool.Name`/`toolByName`/`toolItems`, the
`growthState` sub-struct, the `colTable` field and its `NewModel` prefs seeding),
`update_drill.go`, `update_load.go`, `update.go`, `view.go` (`crumbText`,
`dbScopedLevel`), `format.go`, `layout.go`, `keys_enter.go`, `keys.go`,
`update_keys.go` — plus `internal/cli/config.go` (`--size-dir`, `--sample`,
`--sample-indexes`, `--sample-min-bytes`, `--sample-retention`, `--growth`), `main.go`
(the non-TUI branch), `internal/prefs` (one additive field) and a new
"Growth / size history" bullet in `CLAUDE.md`.

### C10 — Partition roll-up in the browser

Fold a partition tree into its root at `levelTables` so the parent carries the tree's
bytes and the partitions stop being flat siblings, with a key to unfold. The SQL shape
already exists in `sqlDiagTableShowSize` (`internal/pg/queries_diag_table.go:123`).
Cheapest if C9's client-side `parent_oid` walk lands first and is reused; otherwise it
is a `WITH RECURSIVE pg_inherit` CTE added to `sqlTables`. Fixes the README claim.

### C11 — Tablespace level

`pg_tablespace_size` per tablespace plus `reltablespace` on the relation rows, as a
level above databases or a view toggle on the disk tool, so "which mount is filling up"
is answerable. Pairs naturally with filesystem free space from C9's `procfs.FreeSpace`.

---

## Verification

Local toolchain works (go 1.27, golangci-lint 2.13); no network — deps come from
`/workspace/.modcache` via `GOPROXY=file://`. Ignore pre-existing `gofmt -l` noise.

1. `make build && make test && make lint`.
2. **The outstanding live-server check for what already landed.** None of the SQL has
   run against a real cluster. Against **both a PG17 and a PG18** instance, with
   `docs/sample-data.sql` loaded:
   - `--top-queries`: the pooled `stddev_ms` and `cv` carry values on the *since last
     reset* anchor; with `pg_stat_statements.track = all` and a PL/pgSQL function in the
     load, `total_ms` no longer double counts; `C` the new columns and confirm the
     to_jsonb ones read "—" on 17 and real values on 18 (`parallel%`, `generic%`,
     `wal_buf_full`), never an error; `M` then `y` leaves the cumulative counters alone.
   - `--disk-usage`: the tables list grows a `~`-marked bloat column and sorts by it; a
     GIN index shows a measured figure and its pending list, a BRIN index shows neither
     and offers no REINDEX, a bloated btree still does; `a`, `F` and `R` each stream and
     name themselves correctly in the pane header.
   - Repeat both as a **non-superuser** and on a server **without pgstattuple**: every
     new read must degrade to "—" or a blank cell, never an error screen. The header
     should now say *stats unreadable* instead of silently claiming track_planning is off.
3. Unit tests alongside each new change, following the existing `_test.go` patterns.
   For C9 specifically: `internal/pg/growth_test.go` for `DiffSizes` over synthetic
   samples (new / gone / rewritten / zero-baseline / clock skew / partition-root walk)
   and `internal/pg/sizehistory_test.go` for the save→list→load round trip, the
   header-first listing stopping before `Rows`, and the prune policy over a synthetic
   directory (`internal/pg/snapshots_test.go` is the model).
4. The loops over `levelTools..levelLast` in `view_breadcrumb_test.go` and
   `layout_test.go` will fail until any new level has its
   `crumbText`/`levelLabel`/`barReserve`/`enterLabel` answers — treat that as the
   acceptance test for level wiring.
5. Growth, once built: `pgdu --sample` twice with an INSERT and a `CREATE TABLE` in
   between, then `--growth` — the picker lists both samples, `per_day` is right for the
   grown table, the new table is tagged `new`, a `VACUUM FULL` between samples tags
   `rewritten`, a `DROP` tags `gone` and is hidden until `v`, and two samples less than a
   minute apart are refused. The first run on an empty `--size-dir` opens in fallback
   mode rather than erroring, and a sample file dropped into the *snapshot* dir does not
   appear in the top-queries `L` browser.
6. `make deb` last, if the change is being cut for release.
