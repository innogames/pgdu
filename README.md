# pgdu

PostgreSQL disk usage explorer — an ncdu-style TUI for browsing what's
taking up space in your database.

Drill from databases into schemas, tables, partitions, indexes and
columns; sort by size, bloat, or buffer hit ratio; spot the relations
worth reindexing or pruning.

![Tables view](docs/tables.png)

![Table detail](docs/table.png)

![Columns view](docs/columns.png)

![Shared buffers](docs/shared_buffer.png)

![Shared buffers](docs/pages.png)

![Shared buffers](docs/tuples.png)

![Shared buffers](docs/pages_index.png)

## Install

Pre-built `.deb`:

```sh
sudo dpkg -i pgdu_0.1.0_amd64.deb
```

From source:

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
| `b`            | toggle bloat stats    |
| `i`            | install extension     |
| `?`            | help                  |
| `q`            | quit                  |

## Requirements

- PostgreSQL 12+
- `pg_stat_statements` and `pgstattuple` are used opportunistically;
  press `i` in the relevant view to install them if missing.
