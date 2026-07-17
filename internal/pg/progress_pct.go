package pg

import "strings"

// The per-phase counters in every pg_stat_progress_* view reset at each phase
// change, so a naive done/total bar snaps back to zero mid-operation. The span
// maps below pin each phase (as spelled by its progress view) to a fixed slice
// of an overall 0–100% bar, and spanPoint places the phase's own fraction
// inside that slice — turning the per-phase counters into one left-to-right
// pass. The weights are guesses, not measurements — they only need to be
// monotonic and roughly plausible, so the block-proportional scans get the big
// slices and the sort/wait/cleanup phases get slivers.

// reindexPhaseSpan covers pg_stat_progress_create_index — CREATE INDEX and
// REINDEX CONCURRENTLY report into the same view, including the btree build
// subphases.
var reindexPhaseSpan = map[string][2]float64{
	"initializing":                            {0, 1},
	"waiting for writers before build":        {1, 2},
	"building index":                          {2, 65}, // AMs without subphase reporting
	"building index: initializing":            {2, 3},
	"building index: scanning table":          {3, 35},
	"building index: sorting live tuples":     {35, 40},
	"building index: loading tuples in tree":  {40, 65},
	"waiting for writers before validation":   {65, 66},
	"index validation: scanning index":        {66, 74},
	"index validation: sorting tuples":        {74, 77},
	"index validation: scanning table":        {77, 95},
	"waiting for old snapshots":               {95, 97},
	"waiting for readers before marking dead": {97, 98},
	"waiting for readers before dropping":     {98, 100},
}

// vacuumPhaseSpan covers pg_stat_progress_vacuum. heap_blks_scanned and
// heap_blks_vacuumed are cumulative across index-cleanup cycles, so a
// re-entered scan/vacuum phase resumes where it left off inside its span; the
// per-cycle reset of indexes_processed is absorbed by the caller's clamp.
var vacuumPhaseSpan = map[string][2]float64{
	"initializing":             {0, 1},
	"scanning heap":            {1, 60},
	"vacuuming indexes":        {60, 80},
	"vacuuming heap":           {80, 92},
	"cleaning up indexes":      {92, 95},
	"truncating heap":          {95, 99},
	"performing final cleanup": {99, 100},
}

// analyzePhaseSpan covers pg_stat_progress_analyze. The two acquiring phases
// share a span — a plain table runs the first, a partitioned parent only the
// inherited one — and the computing phases report no counters, so they pin to
// their span starts.
var analyzePhaseSpan = map[string][2]float64{
	"initializing":                    {0, 1},
	"acquiring sample rows":           {1, 75},
	"acquiring inherited sample rows": {1, 75},
	"computing statistics":            {75, 95},
	"computing extended statistics":   {95, 99},
	"finalizing analyze":              {99, 100},
}

// clusterPhaseSpan covers pg_stat_progress_cluster (CLUSTER and VACUUM FULL).
// Only the seq-scan phase has a block total; the two scan strategies are
// alternatives, so they share a span, and the tuple-counted phases after them
// pin to their span starts (sqlProgressBase zeroes their counters).
var clusterPhaseSpan = map[string][2]float64{
	"initializing":             {0, 1},
	"seq scanning heap":        {1, 40},
	"index scanning heap":      {1, 40},
	"sorting tuples":           {40, 50},
	"writing new heap":         {50, 70},
	"swapping relation files":  {70, 75},
	"rebuilding index":         {75, 98},
	"performing final cleanup": {98, 100},
}

// basebackupPhaseSpan covers pg_stat_progress_basebackup. Streaming is where
// the bytes move, so it takes nearly the whole bar; a backup taken with
// --no-estimate-size has no backup_total and sits at the streaming span start.
var basebackupPhaseSpan = map[string][2]float64{
	"initializing":                        {0, 1},
	"waiting for checkpoint to finish":    {1, 2},
	"estimating backup size":              {2, 3},
	"streaming database files":            {3, 97},
	"waiting for wal archiving to finish": {97, 99},
	"transferring wal files":              {99, 100},
}

// commandPhaseSpans keys the span maps by the command tag sqlProgressBase puts
// on each row. COPY is deliberately absent — it has no phases, so its raw
// counters already are the overall progress.
var commandPhaseSpans = map[string]map[string][2]float64{
	"CREATE INDEX": reindexPhaseSpan,
	"VACUUM":       vacuumPhaseSpan,
	"ANALYZE":      analyzePhaseSpan,
	"CLUSTER":      clusterPhaseSpan,
	"BASE BACKUP":  basebackupPhaseSpan,
}

// spanPoint places a phase's own done/total fraction inside the phase's
// [start,end] slice of the overall bar. A zero total pins to the span start —
// totals are estimates and read 0 both on phase transitions and in phases
// that report no counter at all.
func spanPoint(span [2]float64, done, total int64) float64 {
	frac := 0.0
	if total > 0 {
		frac = min(float64(done)/float64(total), 1)
	}
	return span[0] + frac*(span[1]-span[0])
}

// OverallPct composes the row's per-phase counters into one 0..100 estimate
// via its command's phase-span map, or -1 for a phase we can't map (new PG
// version, unknown AM) — callers hold the bar where it was rather than
// jumping. Commands without a span map fall back to the raw Pct(). Like
// ReindexProgress.OverallPct, the result is not monotonic on its own (VACUUM
// repeats its index passes every cycle), so callers must also clamp it
// across polls.
func (r ProgressRow) OverallPct() float64 {
	spans, ok := commandPhaseSpans[r.Command]
	if !ok {
		return r.Pct()
	}
	span, ok := spans[r.Phase]
	if !ok {
		// An unmapped AM-specific subphase still bounds us to the build slice.
		if r.Command != "CREATE INDEX" || !strings.HasPrefix(r.Phase, "building index:") {
			return -1
		}
		span = spans["building index"]
	}
	return spanPoint(span, r.Done, r.Total)
}
