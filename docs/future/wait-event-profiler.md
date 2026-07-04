# Future: wait-event sampling profiler ("poor man's ASH")

Status: design only (not implemented).

## Motivation

`pg_stat_activity.wait_event` tells you what each backend is blocked on *right
now*, but a single glance misses the pattern. Oracle's ASH and pg_wait_sampling
answer "where did time actually go?" by sampling wait events over a window. pgdu
already polls `pg_stat_activity` on a timer for the Activity tool — piggybacking
a wait-event histogram on that loop turns those samples into a cheap, no-extension
time profile: "62% LWLock:WALWrite, 20% IO:DataFileRead, 12% running, 6% Lock".

## Data sources

No new query in the simple form: reuse each Activity refresh's
`pg_stat_activity` rows. On every tick, bucket each non-idle backend by
`wait_event_type:wait_event` (or "CPU/active" when running with no wait) into a
ring buffer of counts. The histogram is `Σ samples per class ÷ total samples`
over the retained window.

For higher fidelity later, an optional dedicated query
`SELECT wait_event_type, wait_event, count(*) FROM pg_stat_activity WHERE state
!= 'idle' GROUP BY 1,2` decouples sampling cadence from the table refresh.

## UX sketch

`W` on `levelActivity` opens `levelWaitProfile`: a horizontal stacked bar (the
window's wait-class mix) over a ranked list, plus a sparkline-per-class of the
last N buckets so you can see a spike arrive.

```
window: last 5m · 300 samples · 1s cadence
[████████████ WALWrite ██████ DataFileRead ███ CPU ██ Lock ░ other]

LWLock:WALWrite      61%  ▁▂▃▅▇▇▅▃   WAL flush contention
IO:DataFileRead      19%  ▁▁▂▂▃▂▁▁   heap/index reads from disk
(running, no wait)   12%  ▃▃▂▃▄▃▂▃
Lock:transactionid    6%  ▁▁▂▁▁▁▁▁   row-lock waits
```

## Wiring outline

- Ring buffer on the `Model` (not `screen`, since it accumulates across the tool's
  lifetime): `waitSamples []waitBucket` with a fixed capacity and a head index.
- Hook the accumulation into `onActivityLoaded` (each tick already delivers the
  rows) — no new command needed for the simple form.
- `levelWaitProfile` enum + `view_waitprofile.go` (reuse `paintBar` for the
  stacked class bar; a tiny sparkline helper for the per-class trend).
- `W` key enabled on `levelActivity`; the profile keeps updating on the same
  Activity tick while open.

## Effort

~2–3 days for the piggybacked version; +1 for the dedicated-query / sparkline
polish.

## Risks

- **Fidelity is bounded by cadence.** At 2s ticks you sample coarsely; short
  spikes between ticks are invisible. Label the window honestly ("300 samples @
  1s") so nobody reads it as continuous ASH. Encourage a faster cadence (500ms,
  already supported) while profiling.
- **Buffer sizing.** A 5-minute window at 500ms is 600 buckets × a handful of
  classes — trivial memory, but make the retention explicit and bounded so it
  can't grow unbounded on a long-lived session.
- Sampling bias: a backend that waits *between* ticks never appears. Acceptable
  for a "poor man's" profiler; note it in the `?` overlay.
