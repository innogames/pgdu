# pgdu — PostgreSQL Deep Utility

[![CI](https://github.com/innogames/pgdu/actions/workflows/ci.yml/badge.svg)](https://github.com/innogames/pgdu/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/innogames/pgdu)](https://github.com/innogames/pgdu/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

An ncdu-style TUI for deep inspection of your PostgreSQL database — drill
from databases into schemas, tables, partitions, indexes, columns, pages,
and tuples; analyse query performance, live activity, WAL, and index health;
inspect what's living in `shared_buffers`; and run a one-key server health
triage.

### Disk usage

The default view. Every relation is a bar scaled to its on-disk size, with heap,
index, and TOAST shown as colored segments — so the biggest consumers float to
the top at a glance, ncdu-style.

![Tables view](docs/tables.png)

Drill into a relation (`↵`) to see its **parts** — the heap plus each index
broken out separately, with per-part bloat estimates, dead-tuple counts, and the
last vacuum/analyze times. Index parts are labelled by access method (btree,
GIN, …) and primary/unique. From here you can arm a `REINDEX` on a bloated index.

![Table detail](docs/table.png)

One level deeper are the **columns**: each attribute sized by the storage it
occupies, with its type, average width, and null fraction — handy for spotting a
wide column or an under-used nullable one.

![Columns view](docs/table_columns.png)


### Top queries

A workload analyzer in the spirit of [PoWA](https://powa.readthedocs.io/) (the
web-based PostgreSQL Workload Analyzer) — but living entirely in your terminal,
no web server or collector daemon to deploy. It reads `pg_stat_statements` and
ranks statements by total time, calls, rows, cache-hit ratio, WAL bytes, and
more. Enter shows the full query with `EXPLAIN` and captured sample
parameters. Columns are configurable (`C`), and snapshots can be captured
(`S`) and diffed over a time window (`L`) so you can see what changed between
two points instead of only cumulative totals.

![Top queries](docs/top_queries.png)

### Live activity

A `pg_activity`-style live view of what the server is doing *right now*, read
from `pg_stat_activity`. Each backend is a row showing its state
(active / idle-in-transaction / waiting), how long the current query and
transaction have been running, RSS and CPU, per-backend read/write rates, the
table it's touching, and the query itself. Sort by any column, and filter by
state to zero in on long-running or blocked backends.

![Live activity](docs/activity.png)

### Table overview

Per-table statistics for a schema in one sortable table: size, write/scan
activity, cache hit ratios, bloat, vacuum age, and storage options — with
configurable columns (`C`), like the top-queries view.

### System overview & health triage

**System overview** is a server health dashboard: connections, transactions,
I/O, replication, autovacuum, WAL, PgBouncer, extension capacity, and a
`pg_settings` browser.

**Health triage** runs the whole diagnostic battery concurrently and boils it
down to a red/yellow/green report; Enter drills into the check that fired. The
individual diagnostics (index sizes/bloat, table health, vacuum, locks, …) are
also browsable under **Other tools**.

### Shared buffers

Inspect what's actually living in `shared_buffers` right now — one bar per
relation showing how much of it is buffered, the cache hit ratio, how many pages
are dirty, and a per-relation "temperature" derived from buffer usage counts.
The header summarises the whole buffer pool: used vs. free, this database's
share, and the cold→hot distribution.

![Shared buffers](docs/shared_buffers.png)

Drill into a relation for its **buffer detail**: cache footprint (how much of the
table is cached, the hit ratio, dirty bytes, average usage count) plus a
temperature histogram from cold (evictable) to hot (frequently reused), with the
dirty portion of each band marked.

![Shared buffer detail](docs/shared_buffer_details.png)

### Physical layout (Pages / Tuples)

Drill past a heap into its **pages**: per-page space used, live/dead tuple
counts, and the dead-tuple percentage — a direct look at fragmentation and where
vacuum has work to do.

![Heap pages](docs/pages.png)

The same drill on an index shows its **index tuples** — every entry's `ctid`,
key, and flags, page by page.

![Index pages](docs/pages_index.png)

And one level below a heap page are the raw **heap tuples**: line-pointer flags
(normal/dead/redirect), `xmin`/`xmax`, `ctid`, and the decoded `infomask` bits
(`HEAP_ONLY`, `UPDATED`, frozen, …) — `heap_page_items` made browsable.

![Tuples](docs/page_tuples.png)

![Single Tuple](docs/page_tuple.png)

### WAL Inspector

Breaks down recently generated WAL by record type / resource manager — bytes,
full-page images, and record counts per category — with the individual records
listed alongside, so you can see exactly what is driving WAL volume.

![WAL inspector](docs/wal_inspector.png)

Among the diagnostics under **Other tools**, **index bloat** estimates wasted
space per index (bloat %, bloat MB vs. index/table size, and scan counts) to
surface unused or bloated indexes that are candidates for a `REINDEX` or drop.

![Index bloat](docs/tool_index_bloat.png)

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

## Keys

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
| `?`       | help (all view-specific keys)|

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
