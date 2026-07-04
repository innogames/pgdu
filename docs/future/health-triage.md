# Future: one-key health triage report

Status: implemented ("Health triage" tool on the root picker → levelTriage; see internal/pg/triage.go).

## Motivation

A DBA opening pgdu on an unfamiliar or misbehaving server wants a single "what's
wrong right now?" answer before drilling into any one tool. Today that means
running eight diagnostics by hand. A one-key triage runs a curated battery
concurrently and returns a red/yellow/green checklist, each line drilling into
the diagnostic (or tool) that backs it.

## The battery

Reuse existing diagnostics/queries so the thresholds live in one place:

| Check                    | Source                                              | Red when …                          |
|--------------------------|-----------------------------------------------------|-------------------------------------|
| Transaction wraparound   | `sqlMaintWraparound` / per-table `FreezeFrac`       | age > 80% of autovacuum_freeze_max  |
| Blocked backends         | `ListLockWaiters` (D1)                              | any backend waiting > 30s           |
| Idle-in-transaction      | `idle_in_xact_holders` diagnostic                   | oldest xact > 5m                    |
| Replication lag / slots  | `replication_slots` diagnostic                      | inactive slot or retained WAL > cap |
| Cache hit ratio          | `database_stats` diagnostic                         | hit% < 90                           |
| SLRU pressure            | `slru_stats` diagnostic                             | any hit% < 90 with heavy reads      |
| Sequence exhaustion      | `sequences` diagnostic                              | consumed% > 80                      |
| Bloat (top-N)            | `bloat_table` / `bloat_index`                       | any > 50% and large                 |
| Invalid indexes          | new small query on `pg_index.indisvalid = false`    | any exist                           |
| Temp-file / deadlocks    | `database_stats`                                    | rising deadlocks / large temp_bytes |

## UX sketch

Selecting "Health triage" on `levelTools` runs the battery and pushes a
`levelTriage` screen:

```
● wraparound        oldest datfrozenxid 41% of freeze_max          ok
▲ blocked backends  2 backends waiting (longest 48s)               warn → ↵ lock tree
✗ idle-in-xact      pid 8123 idle in transaction 11m               crit → ↵ diagnostic
● cache hit ratio   99.3%                                          ok
✗ invalid indexes   1 index left INVALID by a failed build         crit → ↵ describe
```

Enter drills into the backing diagnostic/tool for the selected line. Sort by
severity; green lines collapse to a summary count so the eye lands on red first.

## Wiring outline

- `internal/pg/triage.go`: a `Triage(ctx)` that fans the checks out concurrently
  (each is an existing `RunDiagnostic` / method call) under one budget, returning
  `[]TriageResult{Check, Severity, Detail, DiagKey}`.
- `levelTriage` enum; `view_triage.go` renderer (reuse `stateStyle`-like
  severity colours and the graded styles from `styles.go`).
- `toolTriage` entry on the root tool picker → `toolEntryScreen` pushes
  `levelTriage`; Enter maps `DiagKey` back to a `diagnosticResultScreen` push
  (or the lock tree for the blocked-backends line).

## Effort

~4–5 days. The value is in curating thresholds, not new plumbing — every check
already has a query. Do it after the graded-kind work (bucket A4) so triage lines
reuse the same colour language.

## Risks

- Threshold opinions invite bikeshedding; keep them as named constants in
  `triage.go` with a comment justifying each, and treat them as tunable.
- Concurrency under one 30s budget: cap the fan-out and let a slow/failed check
  degrade to a "could not evaluate" line rather than failing the whole report.
