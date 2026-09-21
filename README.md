# pgdu — PostgreSQL Deep Utility

[![CI](https://github.com/innogames/pgdu/actions/workflows/ci.yml/badge.svg)](https://github.com/innogames/pgdu/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/innogames/pgdu)](https://github.com/innogames/pgdu/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

An ncdu-style TUI for deep inspection of a PostgreSQL server — drill from
databases into schemas, tables, partitions, indexes, columns, pages, and tuples;
analyse query performance, live activity, server logs, WAL, and index health;
see what is living in `shared_buffers`; and get a one-screen health verdict with
the fix for every finding. A single binary that talks to the server like `psql`
does — no daemon, no collector, no web server.

## Highlights

- **ncdu for Postgres** — databases → schemas → tables → heap / index / TOAST
  parts → columns, every level a bar scaled to bytes on disk.
- **Maintenance where you see the problem** — every table opens with its bloat
  measured; arm a `REINDEX INDEX CONCURRENTLY` on the bloated index (`↵`, `y`) and watch a live
  progress bar fed by `pg_stat_progress_create_index`; run `VACUUM (VERBOSE,
  ANALYZE)` (`v`) with the server's NOTICE output streamed into a pane. A
  progress monitor (`p`) lists every running vacuum / index build / analyze /
  cluster / copy / basebackup with its phase and percentage.
- **Walk a B-tree level by level** — root → internal pages → leaves; `↵` on an
  internal entry follows its downlink, `↵` on a leaf entry lands in the heap
  page with the cursor on that tuple and its byte layout open, HOT chains are
  followed and flagged, `s` seeks by key. Heap pages drill into tuples and a
  byte-level layout of the tuple header, null bitmap, and each attribute.
- **Shared buffers: who owns the cache, and why** — per-relation buffered bytes,
  cached %, hit %, dirty bytes and dirty %, and a usage-count "temperature"
  histogram with dirty and pinned buffers per band; `p` reads a hot table page
  by page, `m` maps the whole shared-memory segment.
- **System overview as a one-screen health check** — memory sizing against the
  host (huge pages, page cache, swap), buffer-cache temperature and miss latency,
  checkpoint interval and cost with a recommended `max_wal_size`, freeze ages and
  the xmin horizon that caps them, replication lag and slots, lock waits, long
  and prepared transactions, the WAL
  archiver, deadlock and temp-file rates, autovacuum backlog, session hygiene,
  the safety and observability settings, and a catalog sweep of the database
  (sequences near their ceiling, stale statistics, bloat, invalid and duplicate
  indexes). Every ratio is a gauge in one shared column, coloured by the finding
  behind it; every threshold that trips becomes a coloured note, a mark on its
  section title and a line in the **recommendations** panel at the top — red for
  breakage, yellow for
  performance — with a copyable `ALTER SYSTEM` fix; `↵` on a line opens what
  explains it (the diagnostic, the lock tree, the activity list, the settings
  browser). `t` samples twice for per-minute rates; a `pg_settings` browser
  flags non-default and restart-pending values.
- **39 diagnostic queries, 12 with a runnable fix** — `↵` generates a lock-safe
  script (CREATE / REINDEX / DROP INDEX CONCURRENTLY, ANALYZE, VACUUM,
  `lock_timeout`-guarded ALTER), `y` runs it statement by statement with notices
  streamed back.
- **Live activity with OS-level detail** — 20+ columns including RSS, CPU %,
  read/s and write/s per backend (from `/proc`), reverse-DNS client hostname,
  `blocked_by`, inline operation progress; `b` opens the lock-blocking tree, `W` a
  wait-event profiler, `k` / `x` cancel or terminate.
- **Top queries with time travel** — opening the view asks for the window's base
  (*session start* by default, a saved snapshot, or *since the last stats reset*),
  snapshots go to disk (`S`) and any two points can be diffed (`L`), plus virtual
  anchors for *now*, *session start*, and *since the last stats reset*. `EXPLAIN
  ANALYZE` runs on a call rebuilt from the parameters `pg_qualstats` captured or
  a call the server log recorded — never on guessed values.
- **Log analyzer** — errors, slow queries, lock waits, checkpoints, temp files and
  autovacuum grouped by fingerprint, sectioned by category; `auto_explain` plans
  with their hot nodes heat-coloured; live tail, rotated `.gz` files, and
  server-side reading over the connection. Reads pgbouncer's own log too, with
  its periodic stats lines turned into a sparkline pane.
- **WAL inspector down to the bytes** — the recent window by resource manager
  and, beneath it, by relation; a record's block references, then the block itself: the
  change data or full-page image decoded with `pageinspect`, the affected tuple
  reconstructed against the table's column layout. The header shows how close
  WAL is to forcing a checkpoint.
- **PgBouncer console browser** — every instance on the host is found from
  `/proc` and `/etc/pgbouncer`, addressed by its own unix socket (several
  instances usually share one TCP port), and browsed read-only: pools with
  waiting clients in red, per-second stats, clients, servers, databases, users,
  config; `l` opens the instance's log. The menu entry appears only once an
  instance is found.
- **Every table exports to CSV** (`e`) — whatever view you are on, filtered and
  sorted as shown, ready for a spreadsheet or an LLM.
- PostgreSQL 17 and newer; extensions are optional and installable from inside
  the TUI (`i`).

### Disk usage

The default view. Every relation is a bar scaled to its on-disk size, with heap,
index, and TOAST shown as colored segments — so the biggest consumers float to
the top at a glance, ncdu-style.

![Tables view](docs/tables.png)

Drill into a relation (`↵`) to see its **parts** — the heap plus each index
broken out separately, with dead-tuple counts and the last vacuum/analyze
times. Index parts are labelled by access method (btree, GIN, …) and
primary/unique. Bloat is measured with `pgstattuple` as the view opens; an index
above the bloat threshold can then be reindexed in place: `↵` arms
`REINDEX INDEX CONCURRENTLY`, `y` confirms, and a progress bar tracks the build
through its phases (including how many lockers it is waiting for). `v` runs
`VACUUM (VERBOSE, ANALYZE, SKIP_LOCKED)` on the table and streams every NOTICE
into a scrollable pane below the list, so you see the dead-tuple and index
statistics as the server reports them. Long-running maintenance from any
session — vacuums, index builds, analyze, cluster, copy, basebackup — shows up in
the progress monitor (`p` from the activity view or the system overview).

![Table detail](docs/table.png)

One level deeper are the **columns**: each attribute sized by the storage it
occupies, with its type, average width, and null fraction — handy for spotting a
wide column or an under-used nullable one.

![Columns view](docs/table_columns.png)


### Top queries

A workload analyzer in the spirit of [PoWA](https://powa.readthedocs.io/) — but
living entirely in your terminal, no web server or collector daemon to deploy.
It reads `pg_stat_statements` and ranks statements by total time, calls, rows,
cache-hit ratio, WAL bytes, temp usage, planning time, and more. Columns are
configurable (`C`) and remembered across runs.

The table always shows a **window**, not cumulative totals: the numbers are the
delta since a baseline. Opening the view asks for that baseline first — *session
start* (preselected: a fresh sample, so the table shows what ran since you opened
it), any saved snapshot, or *since last reset* for the raw cumulative counters —
and Enter loads the table. `R` re-baselines, `t` cycles auto-refresh. `S` writes a
snapshot of the raw counters to disk, and `L` reopens the timeline browser where
you pick any two points — two snapshots, a snapshot and *now*, the start of this
session, or the server's last `pg_stat_statements` reset — and get the difference
between them. Snapshots that predate a stats reset can no longer serve as a
baseline and are hidden automatically.

![Top queries](docs/top_queries.png)

`↵` opens the statement: full SQL, all counters, and a sample call built only
from real values — the constants `pg_qualstats` captured, or a call the server
logged with its parameters; when neither covers every placeholder no call is
shown and only the generic plan runs. `↵` again runs `EXPLAIN (ANALYZE, BUFFERS)` on read-only
statements — plan nodes are heat-coloured by their share of the total time,
with the single worst node bolded, so the bottleneck stands out without reading
every line — `E` executes it and shows the rows, `p` browses the captured
parameter sets, `d` describes the statement's main table, and `u` jumps to that
table in the disk view. `l` opens the log analyzer on the current server log
narrowed to its slow-query lines (`log_min_duration_statement`), slowest first:
the individual executions behind these aggregates, with the parameters they ran
with.

The describe panel is `\d` with statistics attached: columns, indexes with their
size and scan counts, foreign keys, and the table's own counters. `d` again
switches to detail mode, which adds the table's footprint in `shared_buffers`
and tints an index yellow when it saw no scan in the last hour (a never-used
one is already red); `p` opens the table's heap pages in the page inspector.

### Live activity

A `pg_activity`-style view of what the server is doing *right now*, read from
`pg_stat_activity` and enriched from the operating system. Pick the columns you
need (`C`): pid, database, user, application, client address and its
**reverse-DNS hostname**, backend type, state (with an inline percentage from
`pg_stat_progress_*` when the backend is vacuuming, building an index, or
copying), wait event, query / transaction / state age, `xid` / `xmin`,
**blocked_by**, and — when pgdu runs on the database host — **RSS, CPU %,
read/s and write/s** per backend taken from `/proc`. The main table each query
touches and its command type are derived from the SQL.

![Live activity](docs/activity.png)

`f` cycles the filter (active and waiting → non-idle → all), `v` reveals the
auxiliary backends (checkpointer, WAL writer, launchers), `t` cycles the refresh
cadence from 500 ms to 10 s. `↵` opens the backend's statement in the top-queries
detail view. `k` cancels the query and `x` terminates the backend, each behind a
`y` confirmation.

Two views help with lock problems: `b` shows the **lock tree** — every waiting
backend under the one that blocks it, with transaction age and the lock type,
mode, and relation being waited on — and `W` opens a **wait-event profiler** that
samples activity in the background and aggregates the last few minutes by wait
class, so a checkpoint stall or a lock storm shows up as a shape, not a single
sample. Backends that sit idle in a transaction while holding locks are listed
by the `idle_in_xact_holders` diagnostic, and both conditions feed the system
overview's recommendations.

### Log analyzer

Reads the server log and turns it into something you can navigate. Sources: the
file `pg_current_logfile()` points at, the Debian/Ubuntu
`/var/log/postgresql/*.log` files including rotated `.1` / `.2.gz`, any
`--log-file`, or — with no shell access to the host — the server's log
directory read over the connection via `pg_read_binary_file`, with a file picker
built from `pg_ls_logdir()`. Plain stderr logs are parsed with the server's
`log_line_prefix` (auto-detected when it doesn't fit the file); csvlog and jsonlog
work too, as does pgbouncer's own log (`/var/log/postgresql/pgbouncer*.log` is
listed in the picker). DETAIL / HINT / STATEMENT / CONTEXT lines are attached to their primary
entry and `auto_explain` plans fold into their statement, rendered with the same
heat grading as the top-queries EXPLAIN pane: nodes that dominate the actual
time (or, with `log_timing = off`, the cost) are coloured and the worst one is
bolded.

![Log analyzer](docs/logs_1.png)

The overview groups entries by fingerprint — errors by normalized message, slow
statements by normalized SQL with avg / p95 / max duration — and sections them by
category: errors, warnings, lock waits, temp-file spills, replication,
checkpoints, autovacuum, connections, and — last, because it usually has the
most distinct groups — slow queries. Column headers show the sort. The header
carries a per-severity timeline histogram of the loaded window. `f` narrows both
panes to one category (cycling only the categories the window has). `Tab` cycles the
grouped view, a sortable chronological timeline, a slow-queries pane ordered by
duration and, for pgbouncer logs, a **pooler stats** pane: one row per
`stats_period` with transactions and queries per second, bytes in / out and
transaction / query latency, cells coloured relative to the column's max and a
sparkline per metric in the header. `↵` on a section header folds the section
away (and back) so a long slow-queries list doesn't bury the rest; `m` toggles
the category sections off for a flat count-ordered list. `/` searches, `w`
widens the tail window (32 MiB up to the whole file), `t` starts a live tail
that re-reads incrementally and survives rotation. `↵` drills group → entries →
the full record, `j` jumps from an entry to its line in the timeline, `d`
describes the table behind a statement or error (in the line's database when
the prefix has `%d`, otherwise searched across all databases), and `C` picks
timeline columns (including a reverse-DNS `hostname` for the client).

Inside a group, `Tab` swaps the entry list for a **parameters** table: the
literal values inlined in each member statement — or only the first one, `$1`
— counted and ranked, so the id every error names or the tenant whose query is
the slow one stands out; `↵` on a value lists the entries that carried it.

### PgBouncer

Finds every pgbouncer on the host without configuration: running processes in
`/proc` (their ini is parsed for `unix_socket_dir`, `listen_port`, `logfile`,
`pool_mode`, `admin_users` / `stats_users`), `/etc/pgbouncer/*.ini` for
configured-but-stopped instances, and pgdu's own connection when it evidently
ends at a pooler. `--pgbouncer-target INI|SOCKETDIR|HOST[:PORT]` (repeatable, or
`PGDU_PGBOUNCER_TARGET`) adds one by hand. Each instance is reached over its own
unix socket whenever it exists — several instances commonly share a TCP port via
`so_reuseport`, and a TCP connection then lands on a random one — with TCP on
`listen_addr` as the fallback.

The instance list shows state, version, pid, target, pool mode, client/server
totals with the longest queue wait, pool count, logfile and where the instance
was found; a single instance opens directly. `↵` gives the overview — version,
active/paused/suspended, `SHOW LISTS` counters, pool totals — and a menu of
`SHOW` tables: pools (sorted by `cl_waiting`), stats (pgbouncer's own per-second
`avg_*` rates; cumulative `total_*` via `C`), clients, servers, databases, users,
config, memory, sockets, file descriptors. Columns come from the console
itself, so any pgbouncer version works; microsecond and byte columns are
humanized, clients / servers / sockets gain a reverse-DNS `hostname` next to
`addr`, the `scram_*` key columns are never shown, `t` cycles the refresh (1s → 10s → off), `C` picks columns per
table, `e` exports. `l` opens the instance's logfile in the log analyzer, which
understands pgbouncer's line format (socket events under *conn*, the periodic
`stats:` lines in their own pooler stats pane).

The tool is read-only: it never issues RELOAD, PAUSE, RESUME or KILL. The console
login is `-U` (or `--pgbouncer-user`) and must be in `stats_users` (read-only
suffices) or `admin_users`, with a password in `auth_file`; pgdu reads it from
`PGDU_PGBOUNCER_PASSWORD`, `PGPASSWORD`, or `~/.pgpass` keyed by the socket
directory (`/var/run/pgbouncer_1:6432:pgbouncer:postgres:…`, the libpq
convention) or `localhost`. A refused login shows exactly that hint next to the
instance.

### Diagnostics

**Diagnostics** (under *Other tools*) are 39 saved queries in six categories —
index, table, vacuum, activity, WAL, server — with `f` to filter by category, `s`
to show the SQL, `C` to pick columns, and `d` to describe the object behind a
row (in the row's own database when the query ran across all of them). Twelve of them come with a **fix**:
index bloat, unused / duplicate / redundant / invalid indexes, BRIN and CLUSTER
candidates, table bloat, stale statistics, fillfactor, vacuum stats, and
wraparound freeze age. Duplicate indexes are ranked by the bytes that dropping
the extra copies would free. A BRIN candidate's fix builds the BRIN index next to
the btree it could replace and leaves the `DROP` as a comment to run once the
plans are confirmed to prune — or, when the btree keys on further columns and
still serves lookups on those, a warning not to drop it. `↵` on a result row
generates the script for that object — only lock-safe statements (`CREATE`,
`REINDEX` and `DROP INDEX CONCURRENTLY`, `ANALYZE`, plain `VACUUM`; `ALTER TABLE`
guarded by a 3 s `lock_timeout`; `VACUUM FULL` and `CLUSTER` only ever as
comments) — and `y` runs it statement by statement over a
dedicated connection, streaming server notices and per-statement timings. On
success the diagnostic reloads so you see the effect immediately.

![Index bloat](docs/tool_index_bloat.png)

### System overview

A server health check on one screen: version, role, uptime, connections split by
state, the longest transaction, idle-in-transaction and running query; commit /
rollback ratio, deadlocks and conflicts; tuple-level write and scan activity;
replication and slots (lag, sync state, retained WAL, inactive consumers); the
memory GUCs against the host they run on (huge pages actually allocated, page
cache, swap, `work_mem × max_connections`); autovacuum workers busy, tables past
their vacuum threshold, transaction-ID and multixact age against their
`autovacuum_*freeze_max_age` limits, and the oldest live xmin with whatever pins
it; the buffer cache (`pg_buffercache_summary`,
usage-count temperature, miss latency, SLRU caches, who writes dirty pages); WAL
rate, checkpoint interval and cost, `pg_wal` on disk; the observability settings
(`track_io_timing`, `log_checkpoints`, `log_lock_waits`, `log_temp_files`,
`pg_stat_statements.track`); operational health (pending configuration changes,
lock waits and blocked chains, prepared transactions, temp files, the WAL
archiver); and **schema health**, a catalog sweep of the connection database —
sequences near their ceiling, tables with stale planner statistics, foreign keys
without an index, heavily bloated tables and indexes, invalid and duplicate
indexes — that runs on open and on `space`, never on the auto-refresh tick.
Cumulative counters are labelled with the window they cover; `t` turns on
auto-refresh so per-minute rates appear next to them.

Every ratio on the screen is a gauge in one shared column — connections by state
against `max_connections`, buffer-pool occupancy with its dirty share, host
memory split into shared_buffers (used and unused), other processes, page cache
and free the way the shared-buffers header draws it, swap, autovacuum workers,
the freeze ages, full-page images, timed against requested checkpoints, the
average checkpoint interval against `checkpoint_timeout`, and WAL since the last
checkpoint against `max_wal_size`. A bar takes its colour from the recommendation
that fired for it, so a gauge never contradicts the panel, and a section title
carries the panel's `!` or `~` mark for the worst finding it holds, so the
sections worth reading stand out while scrolling.

Every threshold that trips shows as a coloured note on its row and again in the
**recommendations** panel right under the capacity rows, worst first — red means
something is breaking or about to (a freeze age past half of
`vacuum_failsafe_age`, a stuck archiver, a lost slot, `autovacuum`/`fsync` off, a
missing synchronous standby), yellow is performance or hygiene — with the
concrete change as a copyable `ALTER SYSTEM` line; nothing is applied by pgdu. Each recommendation is an action row: `↑↓`
walk the capacity rows and the recommendations, and `↵` opens whatever explains
the finding — the diagnostic listing the offenders (with its per-row fixes), the
lock tree, the activity list, or the settings browser filtered to the GUC. The
top block shows **extension capacity** — how full `pg_stat_statements` /
`pg_qualstats` and the table statistics are — with a confirmed reset (`↵`, `y`).

Freezing is graded on whether it is *keeping up*, not on how close the next
routine anti-wraparound autovacuum is. An age approaching
`autovacuum_freeze_max_age` is that autovacuum's trigger, so on a busy cluster it
happens constantly and only earns an explanatory note; a warning needs an age
several times the limit, which means a forced freeze cycle already ran without
advancing `relfrozenxid`, and red is reserved for `vacuum_failsafe_age` and the
2^31 stop. The leading indicator is its own finding: the **oldest live xmin**
across backends, replication slots and prepared transactions, named down to the
holding pid, slot or gid — while a horizon is pinned no vacuum can advance
`relfrozenxid` anywhere, so it moves days before the ages do. A
`pg_stat_activity` this role cannot fully read is reported as unknown rather than
as an all-clear.

`s` opens the **settings browser**: every
`pg_settings` entry with its value, filterable with `/`, non-default values in
yellow and settings still waiting for a restart in red — the place to go when the
dashboard's *pending config* line names something. `a`, `w`, `r`, `o`, `p` and `l`
jump to activity, WAL, replication, the full `pg_stat_io` table, the progress
monitor and the log analyzer on the current server log.

### Table overview

Per-table statistics for a schema in one sortable table with configurable
columns (`C`): heap / index / TOAST sizes and their ratio, live and dead rows,
inserts / updates / deletes with the HOT-update share, sequential vs. index
scans, cache hit ratios, buffered and dirty bytes from `pg_buffercache`, vacuum
and analyze age and counts, transaction-ID age, fillfactor, and per-table
autovacuum overrides.

### Shared buffers

Inspect what is actually living in `shared_buffers` right now. The header shows
three bars: the host's memory (shared buffers vs. other processes vs. page cache
vs. free), the buffer pool by relation, this database, other databases, and free,
and a cluster-wide **temperature** histogram from buffer usage counts with dirty
and pinned totals. Below it, one bar per relation with buffered bytes, the share
of the table that is cached, hit ratio, **dirty bytes** and the **dirty share**
of what is buffered (graded on fixed thresholds, since a mostly-dirty cache means
write pressure whatever the table's size), and a mean usage count coloured
cold → hot — so a table that sits in the pool without being reused, or one whose
pages are all dirty and hot, is obvious at a glance.

![Shared buffers](docs/shared_buffers.png)

Drill into a relation for its **buffer detail**: cache footprint, hit ratio,
dirty bytes as a share of what is buffered, and the relation's own histogram from
cold (evictable) to hot (frequently reused), with the dirty and pinned portion of
each band marked. Heap, TOAST, and indexes are counted together. From either
screen `p` opens the table's heap pages in the page inspector, so a hot or dirty
table can be read page by page.

![Shared buffer detail](docs/shared_buffer_details.png)

`m` opens a map of the entire **shared-memory segment** from
`pg_shmem_allocations`, bucketed into buffer pool, WAL, transaction SLRUs, locks,
backends, stats, anonymous, and free — the buffer pool is not the only thing in
there.

### Physical layout (Pages / Tuples)

The page inspector puts `pageinspect` behind a cursor. Drill past a heap into its
**pages**: per-page space used, live / dead tuple counts, dead percentage, and —
when `pg_buffercache` is loaded — whether the page is in shared buffers and dirty.
Large relations are read in windows of 2000 pages; `PgUp` / `PgDn` slide the
window.

![Heap pages](docs/pages.png)

A **B-tree index** opens sorted by level, root on top, leaves at the bottom, with
a banner from `bt_multi_page_stats` giving the tree shape (`L2 1 root · L1 154 ·
L0 139538 leaf`) and the metapage's root block, height, and deduplication
status. Every page row shows type, live / dead items, free space, and sibling
links. `↵` on a page lists its **index tuples**; `↵` on an entry of an internal
page follows the downlink to the child page, so you can descend from root to a
particular leaf one level at a time. Leaf entries are decoded against the heap
and show the indexed key; an entry that points at a HOT-updated row is resolved
through the redirect line pointer, marked with the hop and flagged `H`. `↵` on a
leaf entry opens the heap page it points at with the cursor on that tuple and
its byte layout already open, so the index → heap hop stays inside the page
inspector. `s` seeks to a key (or, on BRIN, a heap block). GiST, GIN, and BRIN indexes open in block order
with their own page statistics.

![Index pages](docs/pages_index.png)

Below a heap page are the raw **heap tuples**: line-pointer flags (normal / dead
/ redirect / unused), `xmin` / `xmax`, `ctid`, and the decoded `infomask` bits
(`HEAP_ONLY`, `UPDATED`, frozen, …) — `heap_page_items` made browsable. `↵` on a
redirect line pointer moves the cursor to its target, so repeated `↵` walks a
HOT chain. `C` picks which of the table's own columns ride along as value
columns (the primary key by default): a live row shows its value as the session
sees it, while a dead, aborted or superseded tuple shows the value decoded from
its own bytes, muted — the versions no query can reach any more. Integer columns
named like a time (`*_at`, `timestamp`) whose values fall in a plausible range are
rendered as timestamps.

![Tuples](docs/page_tuples.png)

`↵` on a tuple opens its **byte layout**: header, null bitmap, and each attribute
as a segment with its offset, length, and decoded value; a TOAST pointer drills
into the TOAST relation and reassembles the out-of-line value from its chunks.

![Single Tuple](docs/page_tuple.png)

![Single Heap Tuple](docs/page_heap_tuple.png)

### WAL Inspector

Puts `pg_walinspect` behind a cursor. The overview stacks two tables over the
same window. The first breaks the recently generated WAL down by resource
manager — Heap, Btree, Transaction, XLOG, … — with each bar split into record
bytes and full-page images, so write amplification from too-frequent checkpoints
shows as a colour rather than a ratio to compute. Beneath it the window is
regrouped **by relation** — which tables and indexes caused the WAL, names
resolved from relfilenodes across databases, with how much of the window the
block-level bytes account for. The header gives the current insert / flush LSN
and segment, the size of `pg_wal`, the lifetime `pg_stat_wal` counters, the LSN
window analysed (the most recent 16 MiB — every breakdown covers that window,
not the WAL since the checkpoint), and a **checkpoint bar**: WAL written since
the last checkpoint's redo point against `max_wal_size`, with the timed /
requested split and when the next timed checkpoint is due. `l` opens the log
analyzer on the current server log narrowed to its checkpoint lines — what each
one wrote, how long write/sync took, how many WAL files it recycled. Both tables
end in a Σ row.

![WAL inspector](docs/wal_inspector.png)

`↵` on a resource manager lists its records oldest first under a per-record-type
summary (INSERT / HOT_UPDATE / LOCK …); `↵` on a relation lists its block
references across the window, FPI-heaviest first; `↵` on a record shows its
**block references** — relation, fork, block number, and whether a page image or
only change data was logged; `↵` once more opens the **block payload**: the
change data or the full 8 KiB
page image with its line pointers decoded by `pageinspect`, the tuple this
record inserted / updated / deleted marked, and its column values reconstructed
from the raw bytes against the table's current column layout. Every level
exports (`e`). Needs `pg_walinspect` and a superuser or `pg_read_server_files`
role; the checkpoint block needs superuser and is left out otherwise.

## Install

Grab a pre-built binary for your platform from the
[Releases](https://github.com/innogames/pgdu/releases) page (Linux, macOS; amd64 and arm64).

Debian/Ubuntu — download the `.deb` from the same page and:

```sh
sudo dpkg -i pgdu_*_amd64.deb
```

From source (needs Go 1.26+):

```sh
make build      # ./pgdu
make deb        # pgdu_<version>_amd64.deb
```

## Usage

Connects like `psql` — no flags means local Unix socket / peer auth:

```sh
pgdu
pgdu -h db.example.com -U readonly -d production
pgdu --dsn postgres://user:pass@host:5432/dbname
```

Honors the usual libpq environment: `PGHOST`, `PGPORT`, `PGUSER`,
`PGDATABASE`, `PGPASSWORD`, `PGSSLMODE`, and `~/.pgpass`.

Skip the tool picker and start in a specific view:

```sh
pgdu --disk-usage
pgdu --shared-buffers
pgdu --activity
pgdu --top-queries --queries-refresh 5s --snapshot-dir /var/lib/pgdu/snapshots
pgdu --logs --log-file /var/log/postgresql/postgresql-17-main.log.2.gz
pgdu --pgbouncer
pgdu --pgbouncer --pgbouncer-target /var/run/pgbouncer_2 --pgbouncer-user nagios
```

`PGDU_LOG_FILE`, `PGDU_SNAPSHOT_DIR`, `PGDU_PGBOUNCER_TARGET` and
`PGDU_PGBOUNCER_USER` set the same defaults from the
environment; `PGDU_CONFIG_DIR` relocates the per-user preferences (column
choices) from `~/.config/pgdu`.

## Keys

The title line of every screen is a breadcrumb — `host ▸ tool ▸ database ▸ schema
▸ object ▸ …` — with one crumb per screen, so `esc` always steps back exactly one
crumb. A screen entered by jumping tools (a describe panel into the page
inspector, an overview recommendation into the lock tree) is prefixed with the
tool it belongs to, and the first screen scoped to a database the trail hasn't named yet shows
it in parentheses.

Rows that `↵` opens — the next level, a detail overlay, or an unfolding row —
carry a muted `↵` in front of their name; rows without it are leaves. The footer
names where `↵` leads on the current screen (`↵ parts`, `↵ query detail`,
`↵ fix`), and a key hint written `→ …` (`p → pages`, `b → lock tree`) jumps to
another view instead of acting on the one you are in.

Every view has its own `?` reference explaining the columns and what the numbers
mean. Keys shared by all views:

| Key       | Action                       |
|-----------|------------------------------|
| `↑` `↓`   | move                         |
| `↵`       | open the highlighted row     |
| `q`/`esc` | back                         |
| `/`       | filter                       |
| `←` `→`   | sort column                  |
| `r`       | reverse sort                 |
| `C`       | configure columns            |
| `space`   | refresh                      |
| `e`       | export view to CSV           |
| `i`       | install a missing extension  |
| `?`       | help (all view-specific keys)|

Frequently used view-specific keys:

| Key        | Where            | Action                                            |
|------------|------------------|---------------------------------------------------|
| `b`        | activity         | open the lock tree                                |
| `v`        | parts / activity | run VACUUM / show auxiliary backends              |
| `p`        | activity, overview / parts, buffers, describe | progress monitor / open the page inspector |
| `W`        | activity         | wait-event profiler                               |
| `k` `x`    | activity         | cancel query / terminate backend (`y` to confirm) |
| `S` `L` `D`| top queries      | save / browse & diff / delete snapshots           |
| `R`        | top queries      | re-baseline the window                            |
| `d`        | queries, logs, diagnostics, wal | describe the table / index (`d` again: detail mode) |
| `m`        | buffers / logs   | shared-memory map / toggle log sections           |
| `Tab`      | logs / log group | groups → timeline → slow queries → pooler stats / entries → parameters → `$1` |
| `t`        | activity, queries, logs, pgbouncer, overview / describe | cycle auto-refresh / live tail / open top queries filtered to the table |
| `l`        | pgbouncer / wal / top queries / overview | log analyzer: the instance's log / the server log's checkpoint lines / its slow-query lines / the whole log |
| `f`        | activity, diagnostics, logs | cycle the backend / category filter    |
| `s`        | diagnostics / index tuples / overview | show SQL / seek to a key / settings browser |
| `a` `w` `r` `o` `l` | overview | jump to activity / WAL / replication slots / `pg_stat_io` by backend type / the server log |

`e` writes the current view — filtered and sorted as displayed — to
`$TMPDIR/pgdu-<tool>-YYYYMMDD-HHMMSS.csv` and prints the path. The temp
directory is used on purpose: under `sudo -u postgres` the working directory is
usually not writable.

## Sample data

To try pgdu against a database with varied relations — heap-heavy and
index-heavy tables, several index types (btree, partial, GIN trigram, GIN
jsonb), out-of-line TOAST columns, and some bloat — load
[`docs/sample-data.sql`](docs/sample-data.sql):

```sh
createdb pgdu_test
psql -d pgdu_test -f docs/sample-data.sql
pgdu -d pgdu_test
```

It creates `app`, `analytics`, and `archive` schemas (~430 MB total) and is
safe to re-run — each table is dropped and rebuilt.

## Requirements

- PostgreSQL 17+
- Extensions are used opportunistically per view: `pg_stat_statements`,
  `pgstattuple`, `pg_buffercache`, `pageinspect`, `pg_walinspect`,
  `pg_qualstats`; press `i` in the relevant view to install one if missing.
- Some views need server roles rather than extensions: the shared-memory map
  needs `pg_read_all_stats`, reading logs over the connection needs
  `pg_read_server_files` (and `pg_monitor` for the file picker).
- The PgBouncer tool needs its login in `stats_users` or `admin_users` with a
  password in `auth_file` (see above); process discovery via `/proc` is
  Linux-only and needs pgdu on the pgbouncer host.
- The per-backend RSS / CPU / IO columns in the activity view, the host memory
  bar in the shared-buffers header and the host rows of the system overview
  (huge pages, page cache, swap) are Linux-only and require pgdu to run on the
  database host; IO rates additionally need the same UID as the postgres
  processes or root. The overview's `pg_wal` size and `pg_buffercache_summary`
  rows need `pg_monitor` and show `n/a` without it.
