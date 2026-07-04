package pg

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"golang.org/x/sync/errgroup"

	"pgdu/internal/humanize"
)

// Severity grades one triage check: green / yellow / red.
type Severity int

const (
	SevOK Severity = iota
	SevWarn
	SevCrit
)

// TriageTarget says which screen Enter should drill into for a triage line.
type TriageTarget int

const (
	TriageTargetDiagnostic  TriageTarget = iota // push the diagnostic named by DiagKey
	TriageTargetLockTree                        // push the live lock tree
	TriageTargetMaintenance                     // push the maintenance/system overview
)

// TriageResult is one line of the health-triage report.
type TriageResult struct {
	Check    string
	Severity Severity
	Detail   string
	DiagKey  string // diagnostic to drill into when Target == TriageTargetDiagnostic
	Target   TriageTarget
}

// Triage thresholds. All are deliberately named constants so opinions live in
// one place and can be tuned without hunting through check code. Where a check
// reads cumulative counters (deadlocks, temp_bytes) the thresholds are generous
// because there is no baseline to compute a rate against.
const (
	// wraparound: autovacuum starts aggressive freezing at
	// autovacuum_freeze_max_age; being most of the way there means autovacuum
	// is not keeping up.
	wraparoundCritFrac = 0.80
	wraparoundWarnFrac = 0.50

	// blocked backends: a lock wait measured in tens of seconds is past
	// "normal contention" and worth a look. XactAgeMs is the transaction age,
	// not the exact wait duration — an upper bound, good enough for triage.
	blockedCritXactMs = 30_000

	// idle-in-transaction: 5 minutes of holding locks/snapshot while idle
	// blocks vacuum and invites bloat.
	idleXactCritSecs = 300

	// cache hit ratio: below ~90% the working set clearly doesn't fit in
	// shared_buffers; below warn it's starting to slip.
	cacheHitCritPct = 90
	cacheHitWarnPct = 95

	// SLRU: a poor hit ratio only matters once the cache sees real traffic;
	// the read floors keep byte-sized test clusters green.
	slruHitCritPct     = 90
	slruCritReadsFloor = 10_000
	slruWarnReadsFloor = 1_000

	// sequences: consumed_pct is fraction of max_value handed out; past 80%
	// exhaustion is on the horizon and a type/cycle decision is due.
	seqCritPct = 80
	seqWarnPct = 60

	// replication slots: when the server can't report safe_wal_size, cap
	// retained WAL at a fixed budget instead.
	slotRetainedCapBytes = 16 << 30

	// temp files / deadlocks: cumulative since the last stats reset, so only
	// large absolute numbers are meaningful.
	deadlocksWarn = 1
	deadlocksCrit = 100
	tempBytesWarn = 10 << 30
	tempBytesCrit = 100 << 30

	// fan-out: enough parallelism to finish fast without stampeding a server
	// that is already unwell; each check also gets its own sub-budget so one
	// hung catalog query degrades to "could not evaluate" instead of eating
	// the whole report's time.
	triageFanout       = 6
	triageCheckTimeout = 10 * time.Second
)

// Triage runs the curated health battery concurrently and returns one line per
// check, sorted most-severe first. A failed or slow check degrades to a
// SevWarn "could not evaluate" line; Triage itself never fails.
//
// Per-database checks (sequences, bloat, invalid indexes) run against the
// default database only — sweeping every database would multiply the fan-out
// by the cluster's database count and blow the budget. Their detail lines name
// the database they looked at.
func (c *Client) Triage(ctx context.Context) []TriageResult {
	type check struct {
		name    string
		target  TriageTarget
		diagKey string
		run     func(ctx context.Context) (Severity, string, error)
	}

	db := c.DefaultDB()
	checks := []check{
		{"wraparound", TriageTargetMaintenance, "", c.triageWraparound},
		{"blocked backends", TriageTargetLockTree, "", c.triageBlocked},
		{"idle-in-xact", TriageTargetDiagnostic, "idle_in_xact_holders", c.triageIdleInXact},
		{"replication slots", TriageTargetDiagnostic, "replication_slots", c.triageReplicationSlots},
		{"cache hit ratio", TriageTargetDiagnostic, "database_stats", c.triageCacheHit},
		{"SLRU pressure", TriageTargetDiagnostic, "slru_stats", c.triageSLRU},
		{"temp files / deadlocks", TriageTargetDiagnostic, "database_stats", c.triageTempDeadlocks},
		{"sequence exhaustion", TriageTargetDiagnostic, "sequences", func(ctx context.Context) (Severity, string, error) {
			return c.triageSequences(ctx, db)
		}},
		{"bloat", TriageTargetDiagnostic, "bloat_table", func(ctx context.Context) (Severity, string, error) {
			return c.triageBloat(ctx, db)
		}},
		{"invalid indexes", TriageTargetDiagnostic, "index_invalid", func(ctx context.Context) (Severity, string, error) {
			return c.triageInvalidIndexes(ctx, db)
		}},
	}

	results := make([]TriageResult, len(checks))
	g := new(errgroup.Group)
	g.SetLimit(triageFanout)
	for i, chk := range checks {
		g.Go(func() error {
			cctx, cancel := context.WithTimeout(ctx, triageCheckTimeout)
			defer cancel()
			sev, detail, err := chk.run(cctx)
			if err != nil {
				sev, detail = SevWarn, "could not evaluate: "+err.Error()
			}
			results[i] = TriageResult{
				Check:    chk.name,
				Severity: sev,
				Detail:   detail,
				DiagKey:  chk.diagKey,
				Target:   chk.target,
			}
			return nil
		})
	}
	_ = g.Wait()

	sort.SliceStable(results, func(a, b int) bool {
		return results[a].Severity > results[b].Severity
	})
	return results
}

func (c *Client) triageWraparound(ctx context.Context) (Severity, string, error) {
	info, err := c.Maintenance(ctx, "")
	if err != nil {
		return 0, "", err
	}
	// Maintenance is best-effort and absorbs sub-query failures; a zero
	// FreezeMaxAge means the settings read itself failed, so degrade honestly
	// instead of reporting a green 0%.
	if info.FreezeMaxAge <= 0 {
		return 0, "", errors.New("autovacuum_freeze_max_age unavailable")
	}
	sev := wraparoundSeverity(info.XidAge, info.FreezeMaxAge)
	pct := 100 * float64(info.XidAge) / float64(info.FreezeMaxAge)
	return sev, fmt.Sprintf("oldest datfrozenxid at %.0f%% of autovacuum_freeze_max_age", pct), nil
}

func wraparoundSeverity(xidAge, freezeMaxAge int64) Severity {
	if freezeMaxAge <= 0 {
		return SevOK
	}
	frac := float64(xidAge) / float64(freezeMaxAge)
	switch {
	case frac > wraparoundCritFrac:
		return SevCrit
	case frac > wraparoundWarnFrac:
		return SevWarn
	}
	return SevOK
}

func (c *Client) triageBlocked(ctx context.Context) (Severity, string, error) {
	nodes, err := c.ListLockWaiters(ctx, "")
	if err != nil {
		return 0, "", err
	}
	waiting, longestMs := 0, float64(0)
	for _, n := range nodes {
		if !n.Waiting() {
			continue
		}
		waiting++
		if n.XactAgeMs > longestMs {
			longestMs = n.XactAgeMs
		}
	}
	sev := blockedSeverity(waiting, longestMs)
	if waiting == 0 {
		return sev, "no lock waits", nil
	}
	return sev, fmt.Sprintf("%d backend(s) waiting on locks (longest xact %s)",
		waiting, triageDuration(longestMs/1000)), nil
}

func blockedSeverity(waiting int, longestMs float64) Severity {
	switch {
	case waiting > 0 && longestMs > blockedCritXactMs:
		return SevCrit
	case waiting > 0:
		return SevWarn
	}
	return SevOK
}

func (c *Client) triageIdleInXact(ctx context.Context) (Severity, string, error) {
	res, err := c.runTriageDiag(ctx, "", "idle_in_xact_holders")
	if err != nil {
		return 0, "", err
	}
	ageCol := diagColIdx(res, "xact_age_secs")
	oldest := float64(0)
	for _, row := range res.Rows {
		if v, ok := diagNum(row, ageCol); ok && v > oldest {
			oldest = v
		}
	}
	sev := idleInXactSeverity(len(res.Rows), oldest)
	if len(res.Rows) == 0 {
		return sev, "no idle-in-transaction backends", nil
	}
	return sev, fmt.Sprintf("%d backend(s) idle in transaction (oldest %s)",
		len(res.Rows), triageDuration(oldest)), nil
}

func idleInXactSeverity(count int, oldestSecs float64) Severity {
	switch {
	case count > 0 && oldestSecs > idleXactCritSecs:
		return SevCrit
	case count > 0:
		return SevWarn
	}
	return SevOK
}

func (c *Client) triageReplicationSlots(ctx context.Context) (Severity, string, error) {
	res, err := c.runTriageDiag(ctx, "", "replication_slots")
	if err != nil {
		return 0, "", err
	}
	if len(res.Rows) == 0 {
		return SevOK, "no replication slots", nil
	}
	var (
		activeCol   = diagColIdx(res, "active")
		statusCol   = diagColIdx(res, "wal_status")
		retainedCol = diagColIdx(res, "retained_wal_bytes")
		safeCol     = diagColIdx(res, "safe_wal_size")

		inactive, lost int
		maxRetained    float64
		overCap        bool
	)
	for _, row := range res.Rows {
		if activeCol >= 0 && row[activeCol].Display == "f" {
			inactive++
		}
		if statusCol >= 0 {
			switch row[statusCol].Display {
			case "lost", "unreserved":
				lost++
			}
		}
		retained, ok := diagNum(row, retainedCol)
		if !ok {
			continue
		}
		if retained > maxRetained {
			maxRetained = retained
		}
		budget := float64(slotRetainedCapBytes)
		if safe, ok := diagNum(row, safeCol); ok && safe > 0 {
			budget = safe
		}
		if retained > budget {
			overCap = true
		}
	}
	sev := slotSeverity(inactive, lost, overCap)
	detail := fmt.Sprintf("%d slot(s), max retained WAL %s", len(res.Rows), humanize.Bytes(int64(maxRetained)))
	if inactive > 0 {
		detail = fmt.Sprintf("%d of %d slot(s) inactive, max retained WAL %s",
			inactive, len(res.Rows), humanize.Bytes(int64(maxRetained)))
	}
	if lost > 0 {
		detail += fmt.Sprintf(", %d lost/unreserved", lost)
	}
	return sev, detail, nil
}

func slotSeverity(inactive, lost int, overCap bool) Severity {
	switch {
	case lost > 0 || overCap:
		return SevCrit
	case inactive > 0:
		return SevWarn
	}
	return SevOK
}

func (c *Client) triageCacheHit(ctx context.Context) (Severity, string, error) {
	res, err := c.runTriageDiag(ctx, "", "database_stats")
	if err != nil {
		return 0, "", err
	}
	hitCol := diagColIdx(res, "hit_pct")
	dbCol := diagColIdx(res, "database")
	minHit, worstDB, seen := 100.0, "", false
	for _, row := range res.Rows {
		v, ok := diagNum(row, hitCol)
		if !ok { // NULL hit_pct: no block traffic yet, nothing to grade
			continue
		}
		seen = true
		if v < minHit {
			minHit = v
			if dbCol >= 0 {
				worstDB = row[dbCol].Display
			}
		}
	}
	if !seen {
		return SevOK, "no block traffic yet", nil
	}
	sev := cacheHitSeverity(minHit)
	if worstDB != "" {
		return sev, fmt.Sprintf("worst %.1f%% (%s)", minHit, worstDB), nil
	}
	return sev, fmt.Sprintf("worst %.1f%%", minHit), nil
}

func cacheHitSeverity(minHitPct float64) Severity {
	switch {
	case minHitPct < cacheHitCritPct:
		return SevCrit
	case minHitPct < cacheHitWarnPct:
		return SevWarn
	}
	return SevOK
}

func (c *Client) triageSLRU(ctx context.Context) (Severity, string, error) {
	res, err := c.runTriageDiag(ctx, "", "slru_stats")
	if err != nil {
		return 0, "", err
	}
	hitCol := diagColIdx(res, "hit_pct")
	readCol := diagColIdx(res, "blks_read")
	nameCol := diagColIdx(res, "name")
	worst, worstName := SevOK, ""
	for _, row := range res.Rows {
		hit, hok := diagNum(row, hitCol)
		reads, rok := diagNum(row, readCol)
		if !hok || !rok {
			continue
		}
		if sev := slruSeverity(hit, reads); sev > worst {
			worst = sev
			if nameCol >= 0 {
				worstName = row[nameCol].Display
			}
		}
	}
	if worst == SevOK {
		return worst, "all SLRU caches healthy", nil
	}
	return worst, fmt.Sprintf("%s cache under pressure (hit ratio below %d%% with heavy reads)",
		worstName, slruHitCritPct), nil
}

func slruSeverity(hitPct, blksRead float64) Severity {
	switch {
	case hitPct < slruHitCritPct && blksRead >= slruCritReadsFloor:
		return SevCrit
	case hitPct < slruHitCritPct && blksRead >= slruWarnReadsFloor:
		return SevWarn
	}
	return SevOK
}

func (c *Client) triageTempDeadlocks(ctx context.Context) (Severity, string, error) {
	res, err := c.runTriageDiag(ctx, "", "database_stats")
	if err != nil {
		return 0, "", err
	}
	dlCol := diagColIdx(res, "deadlocks")
	tmpCol := diagColIdx(res, "temp_bytes")
	var deadlocks, tempBytes float64
	for _, row := range res.Rows {
		if v, ok := diagNum(row, dlCol); ok {
			deadlocks += v
		}
		if v, ok := diagNum(row, tmpCol); ok {
			tempBytes += v
		}
	}
	sev := tempDeadlockSeverity(deadlocks, tempBytes)
	if sev == SevOK {
		return sev, "no deadlocks, little temp-file spill", nil
	}
	return sev, fmt.Sprintf("%d deadlock(s), %s spilled to temp files since stats reset",
		int64(deadlocks), humanize.Bytes(int64(tempBytes))), nil
}

func tempDeadlockSeverity(deadlocks, tempBytes float64) Severity {
	switch {
	case deadlocks >= deadlocksCrit || tempBytes >= tempBytesCrit:
		return SevCrit
	case deadlocks >= deadlocksWarn || tempBytes >= tempBytesWarn:
		return SevWarn
	}
	return SevOK
}

func (c *Client) triageSequences(ctx context.Context, db string) (Severity, string, error) {
	res, err := c.runTriageDiag(ctx, db, "sequences")
	if err != nil {
		return 0, "", err
	}
	pctCol := diagColIdx(res, "consumed_pct")
	maxPct := float64(0)
	for _, row := range res.Rows {
		if v, ok := diagNum(row, pctCol); ok && v > maxPct {
			maxPct = v
		}
	}
	sev := sequenceSeverity(maxPct)
	return sev, fmt.Sprintf("most-consumed sequence at %.1f%% of its range (in %s)", maxPct, db), nil
}

func sequenceSeverity(maxConsumedPct float64) Severity {
	switch {
	case maxConsumedPct > seqCritPct:
		return SevCrit
	case maxConsumedPct > seqWarnPct:
		return SevWarn
	}
	return SevOK
}

// triageBloat leans on the bloat queries' own server-side filters: any row they
// return is already past "50% bloated and large", so row count is the signal.
func (c *Client) triageBloat(ctx context.Context, db string) (Severity, string, error) {
	tables, err := c.runTriageDiag(ctx, db, "bloat_table")
	if err != nil {
		return 0, "", err
	}
	indexes, err := c.runTriageDiag(ctx, db, "bloat_index")
	if err != nil {
		return 0, "", err
	}
	nt, ni := len(tables.Rows), len(indexes.Rows)
	if nt == 0 && ni == 0 {
		return SevOK, fmt.Sprintf("no heavily bloated tables or indexes (in %s)", db), nil
	}
	return SevCrit, fmt.Sprintf("%d table(s), %d index(es) heavily bloated (in %s)", nt, ni, db), nil
}

func (c *Client) triageInvalidIndexes(ctx context.Context, db string) (Severity, string, error) {
	res, err := c.runTriageDiag(ctx, db, "index_invalid")
	if err != nil {
		return 0, "", err
	}
	if len(res.Rows) == 0 {
		return SevOK, fmt.Sprintf("no invalid indexes (in %s)", db), nil
	}
	return SevCrit, fmt.Sprintf("%d index(es) left INVALID by a failed concurrent build (in %s)",
		len(res.Rows), db), nil
}

// runTriageDiag runs a registry diagnostic by key so the SQL and column
// definitions stay single-sourced in diagnostic_defs.go.
func (c *Client) runTriageDiag(ctx context.Context, db, key string) (*DiagResult, error) {
	d, ok := DiagnosticByKey(key)
	if !ok {
		return nil, fmt.Errorf("unknown diagnostic %q", key)
	}
	return c.RunDiagnostic(ctx, db, d)
}

// diagColIdx finds a column by name, -1 when absent.
func diagColIdx(res *DiagResult, name string) int {
	for i, col := range res.Columns {
		if col.Name == name {
			return i
		}
	}
	return -1
}

// diagNum reads the numeric value of row[idx], false when the column is
// missing or the cell carries no number (NULL, text).
func diagNum(row []DiagCell, idx int) (float64, bool) {
	if idx < 0 || idx >= len(row) || !row[idx].HasNum {
		return 0, false
	}
	return row[idx].Num, true
}

// triageDuration renders seconds the way the report reads them: "48s", "11m",
// "3h".
func triageDuration(secs float64) string {
	d := time.Duration(secs * float64(time.Second))
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%.0fh", d.Hours())
	case d >= time.Minute:
		return fmt.Sprintf("%.0fm", d.Minutes())
	}
	return fmt.Sprintf("%.0fs", d.Seconds())
}
