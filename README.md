# pgdu — PostgreSQL Deep Utility

[![CI](https://github.com/innogames/pgdu/actions/workflows/ci.yml/badge.svg)](https://github.com/innogames/pgdu/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/innogames/pgdu)](https://github.com/innogames/pgdu/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

An ncdu-style TUI for deep inspection of a PostgreSQL server — drill from
databases into schemas, tables, partitions, indexes, columns, pages, and tuples;
analyse query performance, live activity, server logs, WAL, and index health;
see what is living in `shared_buffers`; and run a one-key health triage. A single
binary that talks to the server like `psql` does — no daemon, no collector, no
web server.

## Highlights

- **ncdu for Postgres** — databases → schemas → tables → heap / index / TOAST
  parts → columns, every level a bar scaled to bytes on disk.
- **Maintenance where you see the problem** — measure bloat (`b`), arm a
  `REINDEX INDEX CONCURRENTLY` on the bloated index (`↵`, `y`) and watch a live
  progress bar fed by `pg_stat_progress_create_index`; run `VACUUM (VERBOSE,
  ANALYZE)` (`v`) with the server's NOTICE output streamed into a pane. A
  progress monitor (`p`) lists every running vacuum / index build / analyze /
  cluster / copy / basebackup with its phase and percentage.
- **Walk a B-tree level by level** — root → internal pages → leaves; `↵` on an
  internal entry follows its downlink, leaf entries decode the key and open the
  heap row, HOT chains are followed, `s` seeks by key. Heap pages drill into
  tuples and a byte-level layout of the tuple header, null bitmap, and each
  attribute.
- **Shared buffers: who owns the cache, and why** — per-relation buffered bytes,
  cached %, hit %, dirty bytes, and a usage-count "temperature" histogram with
  dirty and pinned buffers per band; `m` maps the whole shared-memory segment.
- **Health triage board** — 23 checks run concurrently and land as red / yellow /
  green; `↵` jumps straight to the offender. A system overview shows the server's
  vital signs and a `pg_settings` browser that flags non-default and
  restart-pending values.
- **38 diagnostic queries, 11 with a runnable fix** — `↵` generates a lock-safe
  script (REINDEX / DROP INDEX CONCURRENTLY, ANALYZE, VACUUM, `lock_timeout`-guarded
  ALTER), `y` runs it statement by statement with notices streamed back.
- **Live activity with OS-level detail** — 20+ columns including RSS, CPU %,
  read/s and write/s per backend (from `/proc`), reverse-DNS client hostname,
  `blocked_by`, inline operation progress; `b` opens the lock-blocking tree, `W` a
  wait-event profiler, `k` / `x` cancel or terminate.
- **Top queries with time travel** — a baseline is taken when you open the view,
  snapshots go to disk (`S`) and any two points can be diffed (`L`), plus virtual
  anchors for *now*, *session start*, and *since the last stats reset*. `EXPLAIN
  ANALYZE` uses the parameters `pg_qualstats` captured, or inferred ones.
- **Log analyzer** — errors, slow queries, lock waits, checkpoints, temp files and
  autovacuum grouped by fingerprint, sectioned by category; live tail, rotated
  `.gz` files, and server-side reading over the connection.
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
primary/unique. Press `b` to measure bloat with `pgstattuple`; an index above
the bloat threshold can then be reindexed in place: `↵` arms
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

The table always shows a **window**, not cumulative totals: a baseline is taken
when the view opens and the numbers are the delta since then. `R` re-baselines,
`t` cycles auto-refresh. `S` writes a snapshot of the raw counters to disk, and
`L` opens a timeline browser where you pick any two points — two snapshots, a
snapshot and *now*, the start of this session, or the server's last
`pg_stat_statements` reset — and get the difference between them. Snapshots that
predate a stats reset can no longer serve as a baseline and are hidden
automatically.

![Top queries](docs/top_queries.png)

`↵` opens the statement: full SQL, all counters, and a sample call. With
`pg_qualstats` installed the sample uses constants actually seen in production;
without it, parameters are inferred from the prepared statement and a generic
plan is shown. `↵` again runs `EXPLAIN (ANALYZE, BUFFERS)` on read-only
statements, `E` executes it and shows the rows, `p` browses the captured
parameter sets, `d` describes the statement's main table, and `u` jumps to that
table in the disk view.

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
by the `idle_in_xact_holders` diagnostic, and both conditions feed the health
triage.

### Log analyzer

Reads the server log and turns it into something you can navigate. Sources: the
file `pg_current_logfile()` points at, the Debian/Ubuntu
`/var/log/postgresql/*.log` files including rotated `.1` / `.2.gz`, any
`--log-file`, or — with no shell access to the host — the server's log
directory read over the connection via `pg_read_binary_file`, with a file picker
built from `pg_ls_logdir()`. Plain stderr logs are parsed with the server's
`log_line_prefix` (auto-detected when it doesn't fit the file); csvlog and jsonlog
work too. DETAIL / HINT / STATEMENT / CONTEXT lines are attached to their primary
entry and `auto_explain` plans fold into their statement.

![Log analyzer](docs/logs_1.png)

The overview groups entries by fingerprint — errors by normalized message, slow
statements by normalized SQL with avg / p95 / max duration — and sections them by
category: errors, warnings, lock waits, temp-file spills, replication, slow
queries, checkpoints, autovacuum, connections. The header carries a per-severity
timeline histogram of the loaded window. `Tab` flips between the grouped view, a
sortable chronological timeline, and a slow-queries pane ordered by duration; `m`
toggles the category sections off for a flat count-ordered list. `/` searches, `w`
widens the tail window (32 MiB up to the whole file), `t` starts a live tail
that re-reads incrementally and survives rotation. `↵` drills group → entries →
the full record, `j` jumps from an entry to its line in the timeline, `d`
describes the table behind a statement or error, and `C` picks timeline columns
(including a reverse-DNS `hostname` for the client).

### Health triage & diagnostics

**Health triage** runs 23 checks concurrently — wraparound age, WAL archiver,
replication lag, connection saturation, PgBouncer waits, checkpoint pressure,
prepared transactions, blocked backends, long and idle-in-transaction sessions,
replication slots, cache hit ratio, SLRU pressure, deadlocks, temp files,
rollback ratio, sequence exhaustion, stale statistics, missing FK indexes, table
and index bloat, invalid indexes — and boils them down to a red / yellow / green
board sorted most-severe first. `↵` on a row drills into whatever explains it:
the diagnostic query, the lock tree, the activity list, or the system overview.

**Diagnostics** (under *Other tools*) are 38 saved queries in six categories —
index, table, vacuum, activity, WAL, server — with `f` to filter by category, `s`
to show the SQL, and `C` to pick columns. Eleven of them come with a **fix**:
index bloat, unused / duplicate / redundant / invalid indexes, CLUSTER candidates,
table bloat, stale statistics, fillfactor, vacuum stats, and wraparound freeze
age. `↵` on a result row generates the script for that object — only lock-safe
statements (`REINDEX INDEX CONCURRENTLY`, `DROP INDEX CONCURRENTLY`, `ANALYZE`,
plain `VACUUM`; `ALTER TABLE` guarded by a 3 s `lock_timeout`; `VACUUM FULL` and
`CLUSTER` only ever as comments) — and `y` runs it statement by statement over a
dedicated connection, streaming server notices and per-statement timings. On
success the diagnostic reloads so you see the effect immediately.

![Index bloat](docs/tool_index_bloat.png)

### System overview

A server dashboard on one screen: version, role, uptime, connection usage split by
state, longest transaction; commit / rollback ratio, deadlocks and conflicts;
tuple-level write and scan activity; replication and slots; PgBouncer pools;
memory GUCs; autovacuum settings and transaction-ID age against
`autovacuum_freeze_max_age`; WAL and checkpoint statistics; `pg_stat_io`; and
pending configuration changes, including settings that still need a restart. The
top block shows **extension capacity** — how full `pg_stat_statements` /
`pg_qualstats` and the table statistics are — with a confirmed reset (`↵`,
`y`). `s` opens the **settings browser**: every `pg_settings` entry with its
value, filterable with `/`, non-default values in yellow and settings still
waiting for a restart in red — the place to go when the dashboard's *pending
config* line names something. `a`, `w`, `r`, and `p` jump to activity, WAL,
replication, and the progress monitor.

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
of the table that is cached, hit ratio, **dirty bytes**, and a mean usage count
coloured cold → hot — so a table that sits in the pool without being reused, or
one whose pages are all dirty and hot, is obvious at a glance.

![Shared buffers](docs/shared_buffers.png)

Drill into a relation for its **buffer detail**: cache footprint, hit ratio,
dirty bytes as a share of what is buffered, and the relation's own histogram from
cold (evictable) to hot (frequently reused), with the dirty and pinned portion of
each band marked. Heap, TOAST, and indexes are counted together.

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
through the redirect line pointer and marked with the hop. `s` seeks to a key
(or, on BRIN, a heap block). GiST, GIN, and BRIN indexes open in block order
with their own page statistics.

![Index pages](docs/pages_index.png)

Below a heap page are the raw **heap tuples**: line-pointer flags (normal / dead
/ redirect / unused), `xmin` / `xmax`, `ctid`, and the decoded `infomask` bits
(`HEAP_ONLY`, `UPDATED`, frozen, …) — `heap_page_items` made browsable. `↵` on a
redirect line pointer moves the cursor to its target, so repeated `↵` walks a
HOT chain.

![Tuples](docs/page_tuples.png)

`↵` on a tuple opens its **byte layout**: header, null bitmap, and each attribute
as a segment with its offset, length, and decoded value; a TOAST pointer drills
into the TOAST relation and reassembles the out-of-line value from its chunks.

![Single Tuple](docs/page_tuple.png)

![Single Heap Tuple](docs/page_heap_tuple.png)

### WAL Inspector

Breaks down recently generated WAL by record type / resource manager — bytes,
full-page images, and record counts per category — with the individual records
and the blocks they touch listed alongside, so you can see exactly what is
driving WAL volume.

![WAL inspector](docs/wal_inspector.png)

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
```

`PGDU_LOG_FILE` and `PGDU_SNAPSHOT_DIR` set the same defaults from the
environment; `PGDU_CONFIG_DIR` relocates the per-user preferences (column
choices) from `~/.config/pgdu`.

## Keys

Every view has its own `?` reference explaining the columns and what the numbers
mean. Keys shared by all views:

| Key       | Action                       |
|-----------|------------------------------|
| `↑` `↓`   | move                         |
| `↵`       | drill in                     |
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
| `b`        | parts / activity | measure bloat / open the lock tree                |
| `v`        | parts / activity | run VACUUM / show auxiliary backends              |
| `p`        | activity, overview | progress monitor for running operations         |
| `W`        | activity         | wait-event profiler                               |
| `k` `x`    | activity         | cancel query / terminate backend (`y` to confirm) |
| `S` `L` `D`| top queries      | save / browse & diff / delete snapshots           |
| `R`        | top queries      | re-baseline the window                            |
| `d`        | queries, logs    | describe the statement's main table               |
| `m`        | buffers / logs   | shared-memory map / toggle log sections           |
| `Tab`      | logs             | groups → timeline → slow queries                  |
| `t`        | activity, queries, logs | cycle auto-refresh / live tail             |
| `s`        | diagnostics / index tuples / overview | show SQL / seek to a key / settings browser |

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
- The per-backend RSS / CPU / IO columns in the activity view and the host
  memory bar in the shared-buffers header are Linux-only and require pgdu to run
  on the database host; IO rates additionally need the same UID as the postgres
  processes or root.
