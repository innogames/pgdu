# Future: unified live progress monitor

Status: design only (not implemented). Precursor shipped: the `progress_all`
diagnostic (`internal/pg/queries_diag.go`, registered in `diagnostic_defs.go`)
already UNIONs every `pg_stat_progress_*` view into one table you can run today
from **Other Tools → Running operations (progress)**.

## Motivation

Watching a long migration (`CREATE INDEX CONCURRENTLY`), a manual `VACUUM
FULL`/`CLUSTER`, a big `COPY`, or a base backup is a common "is it nearly done?"
question. The diagnostic result is a point-in-time snapshot — you have to press
refresh. A dedicated live level would auto-refresh and draw a real progress bar
per operation, so you can leave it open and watch a migration crawl to 100%.

## Data sources

Reuse `sqlDiagProgressAll` as-is (it already normalizes pid, command, relation,
phase, `done_pct`, `running_for`, username across
`pg_stat_progress_{vacuum,create_index,analyze,cluster,copy,basebackup}`). For a
richer bar, extend it to also return `done`/`total` raw counters so the bar can
render blocks-done/blocks-total instead of only the percentage.

## UX sketch

A new `levelProgress` under `toolMaintenance`, opened with `p` from
`levelMaintenance` (mirrors the B5 cross-links). One row per running operation:

```
CREATE INDEX  public.orders_created_idx   building index: 3 of 5   [██████████░░░░]  64%   4m12s
VACUUM        public.events               scanning heap            [███░░░░░░░░░░░]  22%   1m03s
```

Auto-refresh on the Activity tool's tick cadence (reuse `activityTick` /
`cycleActivityRefresh`, gate the tick on `levelProgress` like the lock tree
does). Empty state: "no operations in progress". `d` describes the target
relation (reuse `describeTarget`'s by-name path).

## Wiring outline (per CLAUDE.md "Adding a new entity")

- `levelProgress` enum in `app.go`; screen fields `progressRows []pg.ProgressRow`.
- `internal/pg/progress.go` + a `ProgressRow` type; a `ListProgress(ctx, db)`
  method over the extended `sqlDiagProgressAll`.
- Cmd `loadProgressCmd` in `cmds_maintenance.go` via the `query()` helper;
  `progressLoadedMsg` handled in `update_msgs.go`.
- Reuse `paintBar` / `barSegment` (`row.go`) for the per-row progress bar.
- `view_progress.go` renderer; dispatch in `view.go`; `p` key enabled on
  `levelMaintenance` in `keys.go`.
- Extend `onActivityTick` (or add a `progressTick`) to re-fire `loadProgressCmd`
  while `levelProgress` is on top.

## Effort

~1–2 days. All the hard SQL exists; it's a renderer + a live tick loop, both of
which have close templates (lock tree, activity).

## Risks

- Low. `pg_stat_progress_*` rows vanish the instant an operation finishes, so the
  list will flicker empty at completion — acceptable, but worth a brief "just
  finished" grace note if it feels abrupt.
- `basebackup` has no `relid`; the relation column is blank for it (already
  handled in the SQL).
