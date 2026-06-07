# pgdu

[![CI](https://github.com/innogames/pgdu/actions/workflows/ci.yml/badge.svg)](https://github.com/innogames/pgdu/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/innogames/pgdu)](https://github.com/innogames/pgdu/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

PostgreSQL disk usage explorer — an ncdu-style TUI for browsing what's
taking up space in your database.

Drill from databases into schemas, tables, partitions, indexes and
columns; sort by size, bloat, or buffer hit ratio; spot the relations
worth reindexing or pruning.

### Disk usage

Drill from tables into a single relation and down to its columns.

![Tables view](docs/tables.png)

![Table detail](docs/table.png)

![Columns view](docs/columns.png)

### Shared buffers

Inspect what's occupying `shared_buffers` — per relation, per page, and per tuple.

![Shared buffers](docs/shared_buffer.png)

![Heap pages](docs/pages.png)

![Index pages](docs/pages_index.png)

![Tuples](docs/tuples.png)

### Diagnostics

Built-in diagnostic queries for live activity, WAL, and index health.

![Running queries](docs/tool_running_querries.png)

![WAL inspector](docs/wal_inspector.png)

![Index sizes](docs/tool_index_size.png)

![Index bloat](docs/tool_index_bloat.png)

## Install

Grab a pre-built binary for your platform from the
[Releases](https://github.com/innogames/pgdu/releases) page (Linux, macOS and
Windows; amd64 and arm64).

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

| Key            | Action                |
|----------------|-----------------------|
| `↑`/`k` `↓`/`j`| move                  |
| `↵`/`l`        | drill in              |
| `←`/`h`/`esc`  | back                  |
| `/`            | filter                |
| `s` / `r`      | sort column / reverse |
| `space`        | refresh               |
| `e`            | export view to CSV    |
| `b`            | toggle bloat stats    |
| `i`            | install extension     |
| `?`            | help                  |
| `q`            | quit                  |

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
- `pg_stat_statements` and `pgstattuple` are used opportunistically;
  press `i` in the relevant view to install them if missing.
