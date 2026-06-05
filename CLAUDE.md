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
