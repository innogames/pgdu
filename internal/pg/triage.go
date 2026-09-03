package pg

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

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
	TriageTargetActivity                        // push the live pg_stat_activity list
	TriageTargetPgBouncer                       // push the pgbouncer tool (instance list)
)

// TriageResult is one line of the health-triage report.
type TriageResult struct {
	Check    string
	Severity Severity
	Detail   string
	DiagKey  string // diagnostic to drill into when Target == TriageTargetDiagnostic
	DB       string // database that diagnostic runs against; empty = the default
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
	wraparoundCritFrac = 0.95
	wraparoundWarnFrac = 0.80

	// blocked backends: a lock wait measured in tens of seconds is past
	// "normal contention" and worth a look. XactAgeMs is the transaction age,
	// not the exact wait duration — an upper bound, good enough for triage.
	blockedCritXactMs = 30_000

	// idle-in-transaction: a transaction that goes idle for a second or two
	// between statements is normal churn (connection poolers do it constantly),
	// so warn only once the oldest open idle transaction has held its
	// snapshot/locks past idleXactWarnSecs; 5 minutes escalates to critical,
	// where it is clearly blocking vacuum and inviting bloat.
	idleXactWarnSecs = 60
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

	// sequences: consumed_pct is the fraction of the sequence's own range
	// (start_value → the bound it moves toward) handed out. Below 80% there is
	// nothing to do, so that is the warn floor; at 95% exhaustion is close
	// enough (inserts fail at 100%) that a type/cycle decision is overdue.
	seqCritPct = 95
	seqWarnPct = 80

	// replication slots: when the server can't report safe_wal_size, cap
	// retained WAL at a fixed budget instead. An inactive slot is normal churn
	// (a consumer reconnecting); one with no consumer for an hour is stale —
	// nothing is draining it and it retains WAL forever, so it escalates to
	// critical. inactive_since only exists on PG17+; older servers fall back
	// to the plain "inactive" warning.
	slotRetainedCapBytes  = 16 << 30
	slotStaleInactiveSecs = 3600

	// temp files / deadlocks: cumulative since the last stats reset, so only
	// large absolute numbers are meaningful.
	deadlocksWarn = 10
	deadlocksCrit = 100
	tempBytesWarn = 10 << 30
	tempBytesCrit = 100 << 30

	// connection saturation: fraction of max_connections in use. Past ~80% a
	// spike risks "too many clients"; superuser-reserved slots are the last line.
	connSaturationWarnFrac = 0.80
	connSaturationCritFrac = 0.95

	// checkpoints: a high share of "requested" (as opposed to timed) checkpoints
	// means WAL volume keeps hitting max_wal_size before checkpoint_timeout —
	// max_wal_size is too small. The floor keeps a freshly-started cluster (where
	// the first checkpoint is often requested) green until there's a real sample.
	checkpointReqWarnFrac = 0.30
	checkpointReqCritFrac = 0.50
	checkpointMinTotal    = 10

	// prepared (2PC) transactions: any open prepared xact pins the xmin horizon
	// and delays autovacuum, so its mere presence is a warning; one left open for
	// minutes is a coordinator that forgot to COMMIT/ROLLBACK.
	preparedXactCritSecs = 300

	// rollback ratio: a high share of transactions rolling back can mean app
	// errors or serialization failures. Gated on a minimum volume so a nearly
	// idle database (a handful of rollbacks) never trips it.
	rollbackWarnFrac = 0.25
	rollbackCritFrac = 0.50
	rollbackMinXacts = 1000

	// stats extensions: at .max the extension evicts entries (pg_stat_statements
	// deallocates ~5% at a time), so the counters the top-queries tool reads
	// silently stop being cumulative. The notice level only colours the system
	// overview's bar; triage warns at the warn level. Exported so the overview's
	// colouring and this check agree.
	ExtCapacityNoticeFrac = 0.70
	ExtCapacityWarnFrac   = 0.90

	// index bloat: any bloat_index row is already >50% bloated and >10 MB, but
	// REINDEX CONCURRENTLY fixes it online, so it stays a warning until the
	// wasted total is large enough to hurt cache and disk.
	indexBloatCritBytes = 1 << 30

	// replication: replay_lag reads NULL (→0) while a replica is idle and caught
	// up, so bytes-behind is the second signal for one that stopped reporting
	// lag. A non-streaming state (catchup, backup) is transient and only warns.
	replLagWarnSecs      = 60
	replLagCritSecs      = 300
	replByteLagCritBytes = 1 << 30
	// standby side: the primary sends keepalives every wal_sender_timeout/2 even
	// when idle, so a receiver that has heard nothing for a minute is stalled.
	// No receiver at all only warns — log-shipping standbys legitimately have none.
	walReceiverStaleWarnSecs = 60
	walReceiverStaleCritSecs = 300

	// long-running transaction: any open client xact pins the xmin horizon
	// (blocks vacuum, invites bloat, drags wraparound). Half an hour is past any
	// OLTP request; three hours is a forgotten job or a stuck client.
	longXactWarnSecs = 1800
	longXactCritSecs = 3 * 3600

	// pgbouncer: brief queueing is how transaction pooling works; a client
	// waiting a full second means the pool is undersized or its servers are stuck.
	pgbWaitWarnSecs = 1
	pgbWaitCritSecs = 10

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
// Per-database checks (sequences, stale statistics, FK indexes, table/index
// bloat, invalid indexes) run against the default database only — sweeping
// every database would multiply the fan-out by the cluster's database count and
// blow the budget. Their detail lines name the database they looked at.
func (c *Client) Triage(ctx context.Context) []TriageResult {
	type check struct {
		name    string
		target  TriageTarget
		diagKey string
		db      string
		run     func(ctx context.Context) (Severity, string, error)
	}

	db := c.DefaultDB()

	// Shared inputs, each an expensive one-shot, are fetched once here and handed
	// to the pure graders below — several checks read the same MaintenanceInfo or
	// database_stats view, and calling those N times would multiply the load on a
	// server we may already suspect is unwell.
	var (
		info    *MaintenanceInfo
		infoErr error
		dbStats *DiagResult
		dbErr   error
	)
	var pre sync.WaitGroup
	pre.Go(func() {
		cctx, cancel := context.WithTimeout(ctx, triageCheckTimeout)
		defer cancel()
		info, infoErr = c.Maintenance(cctx, "")
		// Maintenance is best-effort: it absorbs every sub-query failure and
		// returns a zero struct with a nil error when the connection is dead.
		// version() is always populated over a live connection, so an empty
		// Version means "unreachable" — degrade every MaintenanceInfo-backed
		// check uniformly instead of reporting false greens.
		if infoErr == nil && (info == nil || info.Version == "") {
			infoErr = errors.New("server unreachable")
		}
	})
	pre.Go(func() {
		cctx, cancel := context.WithTimeout(ctx, triageCheckTimeout)
		defer cancel()
		dbStats, dbErr = c.runTriageDiag(cctx, "", "database_stats")
	})
	pre.Wait()

	// mgrade/dgrade adapt a pure grader over a shared input into a check.run,
	// short-circuiting to "could not evaluate" when that input failed to load.
	mgrade := func(g func(*MaintenanceInfo) (Severity, string, error)) func(context.Context) (Severity, string, error) {
		return func(context.Context) (Severity, string, error) {
			if infoErr != nil {
				return 0, "", infoErr
			}
			return g(info)
		}
	}
	dgrade := func(g func(*DiagResult) (Severity, string, error)) func(context.Context) (Severity, string, error) {
		return func(context.Context) (Severity, string, error) {
			if dbErr != nil {
				return 0, "", dbErr
			}
			return g(dbStats)
		}
	}

	// The wraparound figure is cluster-wide but its drill-down (per-table freeze
	// ages) is per-database, so it opens in whichever database holds the oldest
	// datfrozenxid rather than in the default one.
	wraparoundDB := ""
	if infoErr == nil {
		wraparoundDB = info.XidAgeDB
	}

	checks := []check{
		{"wraparound", TriageTargetDiagnostic, "wraparound_tables", wraparoundDB, mgrade(wraparoundGrade)},
		{"WAL archiver", TriageTargetMaintenance, "", "", mgrade(archiverGrade)},
		{"replication lag", TriageTargetMaintenance, "", "", mgrade(replicationGrade)},
		{"connection saturation", TriageTargetMaintenance, "", "", mgrade(connSaturationGrade)},
		{"pgbouncer waits", TriageTargetPgBouncer, "", "", c.triagePgBouncer},
		{"checkpoint pressure", TriageTargetMaintenance, "", "", mgrade(checkpointGrade)},
		{"prepared transactions", TriageTargetMaintenance, "", "", mgrade(preparedXactGrade)},
		{"extension capacity", TriageTargetMaintenance, "", "", mgrade(extCapacityGrade)},
		{"blocked backends", TriageTargetLockTree, "", "", c.triageBlocked},
		{"long-running transaction", TriageTargetActivity, "", "", mgrade(longXactGrade)},
		{"idle-in-xact", TriageTargetDiagnostic, "idle_in_xact_holders", "", c.triageIdleInXact},
		{"replication slots", TriageTargetDiagnostic, "replication_slots", "", c.triageReplicationSlots},
		{"cache hit ratio", TriageTargetDiagnostic, "database_stats", "", dgrade(cacheHitGrade)},
		{"SLRU pressure", TriageTargetDiagnostic, "slru_stats", "", c.triageSLRU},
		{"deadlocks", TriageTargetDiagnostic, "database_stats", "", dgrade(deadlockGrade)},
		{"temp files", TriageTargetDiagnostic, "database_stats", "", dgrade(tempFilesGrade)},
		{"rollback ratio", TriageTargetDiagnostic, "database_stats", "", dgrade(rollbackGrade)},
		{"sequence exhaustion", TriageTargetDiagnostic, "sequences", db, func(ctx context.Context) (Severity, string, error) {
			return c.triageSequences(ctx, db)
		}},
		{"stale statistics", TriageTargetDiagnostic, "stale_statistics", db, func(ctx context.Context) (Severity, string, error) {
			return c.triageStaleStats(ctx, db)
		}},
		{"FK missing index", TriageTargetDiagnostic, "fk_missing_index", db, func(ctx context.Context) (Severity, string, error) {
			return c.triageFKMissingIndex(ctx, db)
		}},
		{"table bloat", TriageTargetDiagnostic, "bloat_table", db, func(ctx context.Context) (Severity, string, error) {
			return c.triageTableBloat(ctx, db)
		}},
		{"index bloat", TriageTargetDiagnostic, "bloat_index", db, func(ctx context.Context) (Severity, string, error) {
			return c.triageIndexBloat(ctx, db)
		}},
		{"invalid indexes", TriageTargetDiagnostic, "index_invalid", db, func(ctx context.Context) (Severity, string, error) {
			return c.triageInvalidIndexes(ctx, db)
		}},
	}

	results := make([]TriageResult, len(checks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, triageFanout)
	for i, chk := range checks {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
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
				DB:       chk.db,
				Target:   chk.target,
			}
		})
	}
	wg.Wait()

	sort.SliceStable(results, func(a, b int) bool {
		return results[a].Severity > results[b].Severity
	})
	return results
}

func wraparoundGrade(info *MaintenanceInfo) (Severity, string, error) {
	// A zero FreezeMaxAge means the settings read failed even though the server
	// is reachable, so degrade honestly instead of reporting a green 0%.
	if info.FreezeMaxAge <= 0 {
		return 0, "", errors.New("autovacuum_freeze_max_age unavailable")
	}
	sev := wraparoundSeverity(info.XidAge, info.FreezeMaxAge)
	pct := 100 * float64(info.XidAge) / float64(info.FreezeMaxAge)
	detail := fmt.Sprintf("oldest datfrozenxid %.0f%% of the way to a forced anti-wraparound autovacuum", pct)
	if info.XidAgeDB != "" {
		detail += " (in " + info.XidAgeDB + ")"
	}
	return sev, detail, nil
}

// archiverGrade flags a stalled WAL archiver: pg_wal fills up silently when
// archiving fails, so any failure count is critical.
func archiverGrade(info *MaintenanceInfo) (Severity, string, error) {
	sev := archiverSeverity(info.ArchiveFailed)
	if sev == SevOK {
		return sev, "no WAL archive failures", nil
	}
	detail := fmt.Sprintf("%d WAL archive failure(s)", info.ArchiveFailed)
	if info.ArchiveLastFailed != "" {
		detail += ", last " + info.ArchiveLastFailed
	}
	return sev, detail, nil
}

func archiverSeverity(failed int64) Severity {
	if failed > 0 {
		return SevCrit
	}
	return SevOK
}

func connSaturationGrade(info *MaintenanceInfo) (Severity, string, error) {
	if info.MaxConns <= 0 {
		return 0, "", errors.New("max_connections unavailable")
	}
	used := 0
	for _, n := range info.ConnByState {
		used += n
	}
	sev := connSaturationSeverity(used, info.MaxConns)
	pct := 100 * float64(used) / float64(info.MaxConns)
	return sev, fmt.Sprintf("%d of %d connections in use (%.0f%%)", used, info.MaxConns, pct), nil
}

func connSaturationSeverity(used, maxConns int) Severity {
	if maxConns <= 0 {
		return SevOK
	}
	frac := float64(used) / float64(maxConns)
	switch {
	case frac >= connSaturationCritFrac:
		return SevCrit
	case frac >= connSaturationWarnFrac:
		return SevWarn
	}
	return SevOK
}

// checkpointGrade watches the share of checkpoints forced by WAL volume rather
// than checkpoint_timeout — a high share means max_wal_size is too small.
func checkpointGrade(info *MaintenanceInfo) (Severity, string, error) {
	total := info.CheckpointsTimed + info.CheckpointsReq
	if total == 0 {
		return SevOK, "no checkpoints recorded yet", nil
	}
	sev := checkpointSeverity(info.CheckpointsReq, total)
	pct := 100 * float64(info.CheckpointsReq) / float64(total)
	return sev, fmt.Sprintf("%d of %d checkpoints forced by WAL volume (%.0f%%)",
		info.CheckpointsReq, total, pct), nil
}

func checkpointSeverity(requested, total int64) Severity {
	if total < checkpointMinTotal {
		return SevOK
	}
	frac := float64(requested) / float64(total)
	switch {
	case frac >= checkpointReqCritFrac:
		return SevCrit
	case frac >= checkpointReqWarnFrac:
		return SevWarn
	}
	return SevOK
}

// preparedXactGrade flags open two-phase-commit transactions, which pin the
// xmin horizon and stall autovacuum until they are committed or rolled back.
func preparedXactGrade(info *MaintenanceInfo) (Severity, string, error) {
	if info.PreparedXacts == 0 {
		return SevOK, "no prepared transactions", nil
	}
	sev := preparedXactSeverity(info.OldestPrepSec)
	return sev, fmt.Sprintf("%d prepared transaction(s), oldest %s",
		info.PreparedXacts, triageDuration(info.OldestPrepSec)), nil
}

// preparedXactSeverity is only called when at least one prepared xact exists, so
// the presence alone earns a warning and age escalates it.
func preparedXactSeverity(oldestSecs float64) Severity {
	if oldestSecs > preparedXactCritSecs {
		return SevCrit
	}
	return SevWarn
}

// extCapacityGrade warns when a shared-memory stats extension is close to its
// .max. Extensions that aren't installed, or aren't preloaded (Max unknown),
// have nothing to grade.
func extCapacityGrade(info *MaintenanceInfo) (Severity, string, error) {
	graded := make([]ExtCapacity, 0, 2)
	for _, e := range []ExtCapacity{info.Statements, info.Qualstats} {
		if e.Installed && e.Max > 0 {
			graded = append(graded, e)
		}
	}
	if len(graded) == 0 {
		return SevOK, "no stats extension installed", nil
	}
	sort.SliceStable(graded, func(a, b int) bool { return graded[a].FillRatio() > graded[b].FillRatio() })
	worst := SevOK
	parts := make([]string, 0, len(graded))
	for _, e := range graded {
		if sev := extCapacitySeverity(e.FillRatio()); sev > worst {
			worst = sev
		}
		parts = append(parts, fmt.Sprintf("%s %.0f%% full (%d/%d)", e.Name, 100*e.FillRatio(), e.Used, e.Max))
	}
	return worst, strings.Join(parts, ", "), nil
}

// extCapacitySeverity has no critical level: a full extension degrades its
// statistics, it does not endanger the server.
func extCapacitySeverity(ratio float64) Severity {
	if ratio >= ExtCapacityWarnFrac {
		return SevWarn
	}
	return SevOK
}

// replicationGrade grades streaming replication from whichever side this server
// is on: on a primary the worst replica (state, replay lag, bytes behind), on a
// standby the WAL receiver's liveness.
func replicationGrade(info *MaintenanceInfo) (Severity, string, error) {
	if info.InRecovery {
		wr := info.WalReceiver
		if wr == nil {
			return walReceiverSeverity(false, "", 0), "standby: no WAL receiver running", nil
		}
		age := wr.LastMsgAge.Seconds()
		sev := walReceiverSeverity(true, wr.Status, age)
		return sev, "standby: WAL receiver " + wr.Status + ", last message from primary " +
			triageDuration(age) + " ago", nil
	}
	if len(info.Replicas) == 0 {
		return SevOK, "no streaming replicas", nil
	}
	worst := SevOK
	var worstRep ReplicaStat
	var maxLag float64
	for _, r := range info.Replicas {
		lag := r.ReplayLag.Seconds()
		if lag > maxLag {
			maxLag = lag
		}
		if sev := replicaSeverity(r.State, lag, r.ByteLag); sev > worst {
			worst, worstRep = sev, r
		}
	}
	if worst == SevOK {
		return worst, fmt.Sprintf("%d replica(s) streaming, max replay lag %s",
			len(info.Replicas), triageDuration(maxLag)), nil
	}
	name := worstRep.AppName
	if name == "" {
		name = worstRep.ClientAddr
	}
	return worst, fmt.Sprintf("%d replica(s), worst %s: %s, replay lag %s, %s behind",
		len(info.Replicas), name, worstRep.State,
		triageDuration(worstRep.ReplayLag.Seconds()), humanize.Bytes(worstRep.ByteLag)), nil
}

func replicaSeverity(state string, replayLagSecs float64, byteLag int64) Severity {
	switch {
	case replayLagSecs >= replLagCritSecs || byteLag >= replByteLagCritBytes:
		return SevCrit
	case replayLagSecs >= replLagWarnSecs || state != "streaming":
		return SevWarn
	}
	return SevOK
}

func walReceiverSeverity(present bool, status string, lastMsgSecs float64) Severity {
	switch {
	case !present:
		return SevWarn
	case lastMsgSecs >= walReceiverStaleCritSecs:
		return SevCrit
	case lastMsgSecs >= walReceiverStaleWarnSecs || status != "streaming":
		return SevWarn
	}
	return SevOK
}

// longXactGrade flags the oldest open client transaction: whatever it is doing,
// it pins the xmin horizon, so vacuum cannot reclaim anything newer than its
// snapshot for as long as it stays open. Idle-in-transaction holders have their
// own check; this one also catches transactions that are actively working.
func longXactGrade(info *MaintenanceInfo) (Severity, string, error) {
	secs := info.LongestXactSec
	sev := longXactSeverity(secs)
	if sev == SevOK {
		return sev, "longest open transaction " + triageDuration(secs), nil
	}
	return sev, "a transaction has been open for " + triageDuration(secs) + " (pins the xmin horizon)", nil
}

func longXactSeverity(secs float64) Severity {
	switch {
	case secs >= longXactCritSecs:
		return SevCrit
	case secs >= longXactWarnSecs:
		return SevWarn
	}
	return SevOK
}

// triagePgBouncer flags clients queued in any discovered pgbouncer instance
// for a server connection. Instances are probed concurrently and the worst
// one is reported; a fleet where every console is unreachable degrades to
// "could not evaluate" carrying the first error (with the login hint when
// that error is an auth rejection), not to a false green.
func (c *Client) triagePgBouncer(ctx context.Context) (Severity, string, error) {
	// Discovery itself is filesystem work that ignores ctx; honour the budget
	// explicitly so a cancelled report degrades this check like every other.
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	insts := c.DiscoverPgBouncers(ctx)
	if len(insts) == 0 {
		return SevOK, "no pgbouncer instance found", nil
	}
	probes := make([]PgBouncerProbe, len(insts))
	var wg sync.WaitGroup
	for i := range insts {
		wg.Go(func() {
			pctx, cancel := context.WithTimeout(ctx, pgbDialTimeout)
			defer cancel()
			probes[i] = c.PgBouncerProbe(pctx, insts[i])
		})
	}
	wg.Wait()
	return worstPgBouncerProbe(insts, probes)
}

func worstPgBouncerProbe(insts []PgBouncerInstance, probes []PgBouncerProbe) (Severity, string, error) {
	sev := SevOK
	detail := ""
	reachable := 0
	var firstErr error
	for i, pr := range probes {
		if pr.Err != nil {
			if firstErr == nil {
				firstErr = pr.Err
				if pr.AuthErr {
					firstErr = fmt.Errorf("%w — %s", pr.Err, PgBouncerAuthHint(insts[i], pr.User))
				}
			}
			continue
		}
		reachable++
		s := pgbouncerSeverity(pr.Totals.ClWaiting, pr.Totals.MaxWaitSec)
		if s > sev || detail == "" {
			sev = s
			if pr.Totals.ClWaiting == 0 {
				detail = "no clients waiting"
			} else {
				detail = fmt.Sprintf("%s: %d client(s) waiting for a server connection, longest %s",
					insts[i].Name, pr.Totals.ClWaiting, triageDuration(pr.Totals.MaxWaitSec))
			}
		}
	}
	if reachable == 0 {
		return 0, "", fmt.Errorf("%d instance(s) found, console unreachable: %w", len(insts), firstErr)
	}
	if sev == SevOK {
		detail = fmt.Sprintf("no clients waiting in %d instance(s)", reachable)
	}
	return sev, detail, nil
}

func pgbouncerSeverity(waiting int, maxWaitSecs float64) Severity {
	switch {
	case waiting == 0:
		return SevOK
	case maxWaitSecs >= pgbWaitCritSecs:
		return SevCrit
	case maxWaitSecs >= pgbWaitWarnSecs:
		return SevWarn
	}
	return SevOK
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
	oldest := diagMax(res, "xact_age_secs")
	sev := idleInXactSeverity(len(res.Rows), oldest)
	if len(res.Rows) == 0 {
		return sev, "no idle-in-transaction backends", nil
	}
	return sev, fmt.Sprintf("%d backend(s) idle in transaction (oldest %s)",
		len(res.Rows), triageDuration(oldest)), nil
}

func idleInXactSeverity(count int, oldestSecs float64) Severity {
	switch {
	case count == 0:
		return SevOK
	case oldestSecs > idleXactCritSecs:
		return SevCrit
	case oldestSecs > idleXactWarnSecs:
		return SevWarn
	}
	// Backends are idle in transaction but none has been open long enough to
	// hurt — normal between-statement churn, not worth flagging.
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
		inactCol    = diagColIdx(res, "inactive_secs")

		inactive, stale, lost int
		oldestInactive        float64
		maxRetained           float64
		overCap               bool
	)
	for _, row := range res.Rows {
		if activeCol >= 0 && row[activeCol].Display == "f" {
			inactive++
			if secs, ok := diagNum(row, inactCol); ok {
				if secs > slotStaleInactiveSecs {
					stale++
				}
				if secs > oldestInactive {
					oldestInactive = secs
				}
			}
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
	sev := slotSeverity(inactive, stale, lost, overCap)
	detail := fmt.Sprintf("%d slot(s), max retained WAL %s", len(res.Rows), humanize.Bytes(int64(maxRetained)))
	if inactive > 0 {
		detail = fmt.Sprintf("%d of %d slot(s) inactive, max retained WAL %s",
			inactive, len(res.Rows), humanize.Bytes(int64(maxRetained)))
	}
	if stale > 0 {
		detail += fmt.Sprintf(", %d stale (no consumer for %s)", stale, triageDuration(oldestInactive))
	}
	if lost > 0 {
		detail += fmt.Sprintf(", %d lost/unreserved", lost)
	}
	return sev, detail, nil
}

func slotSeverity(inactive, stale, lost int, overCap bool) Severity {
	switch {
	case lost > 0 || overCap || stale > 0:
		return SevCrit
	case inactive > 0:
		return SevWarn
	}
	return SevOK
}

func cacheHitGrade(res *DiagResult) (Severity, string, error) {
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

func deadlockGrade(res *DiagResult) (Severity, string, error) {
	deadlocks := diagSum(res, "deadlocks")
	sev := deadlockSeverity(deadlocks)
	if deadlocks == 0 {
		return sev, "no deadlocks since stats reset", nil
	}
	return sev, fmt.Sprintf("%d deadlock(s) since stats reset", int64(deadlocks)), nil
}

func deadlockSeverity(deadlocks float64) Severity {
	switch {
	case deadlocks >= deadlocksCrit:
		return SevCrit
	case deadlocks >= deadlocksWarn:
		return SevWarn
	}
	return SevOK
}

func tempFilesGrade(res *DiagResult) (Severity, string, error) {
	tempBytes := diagSum(res, "temp_bytes")
	sev := tempBytesSeverity(tempBytes)
	return sev, humanize.Bytes(int64(tempBytes)) + " spilled to temp files since stats reset", nil
}

func tempBytesSeverity(tempBytes float64) Severity {
	switch {
	case tempBytes >= tempBytesCrit:
		return SevCrit
	case tempBytes >= tempBytesWarn:
		return SevWarn
	}
	return SevOK
}

// rollbackGrade reports the database with the worst rollback ratio, ignoring
// databases below a minimum transaction volume so a quiet cluster stays green.
func rollbackGrade(res *DiagResult) (Severity, string, error) {
	commitCol := diagColIdx(res, "commits")
	rollbackCol := diagColIdx(res, "rollbacks")
	dbCol := diagColIdx(res, "database")
	worstFrac, worstDB, seen := 0.0, "", false
	for _, row := range res.Rows {
		commits, ok1 := diagNum(row, commitCol)
		rollbacks, ok2 := diagNum(row, rollbackCol)
		if !ok1 || !ok2 {
			continue
		}
		total := commits + rollbacks
		if total < rollbackMinXacts {
			continue
		}
		seen = true
		if frac := rollbacks / total; frac > worstFrac {
			worstFrac = frac
			if dbCol >= 0 {
				worstDB = row[dbCol].Display
			}
		}
	}
	if !seen {
		return SevOK, "not enough transactions to judge rollback ratio", nil
	}
	sev := rollbackSeverity(worstFrac)
	if worstDB != "" {
		return sev, fmt.Sprintf("worst rollback ratio %.1f%% (%s)", 100*worstFrac, worstDB), nil
	}
	return sev, fmt.Sprintf("worst rollback ratio %.1f%%", 100*worstFrac), nil
}

func rollbackSeverity(frac float64) Severity {
	switch {
	case frac >= rollbackCritFrac:
		return SevCrit
	case frac >= rollbackWarnFrac:
		return SevWarn
	}
	return SevOK
}

func (c *Client) triageSequences(ctx context.Context, db string) (Severity, string, error) {
	res, err := c.runTriageDiag(ctx, db, "sequences")
	if err != nil {
		return 0, "", err
	}
	maxPct := diagMax(res, "consumed_pct")
	sev := sequenceSeverity(maxPct)
	return sev, fmt.Sprintf("most-consumed sequence at %.1f%% of its range (in %s)", maxPct, db), nil
}

func sequenceSeverity(maxConsumedPct float64) Severity {
	switch {
	case maxConsumedPct >= seqCritPct:
		return SevCrit
	case maxConsumedPct >= seqWarnPct:
		return SevWarn
	}
	return SevOK
}

// triageTableBloat leans on the bloat_table diagnostic's own server-side filter:
// any row it returns is already past "heavily bloated and large", so row count
// is the signal. Reclaiming table bloat needs VACUUM FULL / pg_repack (a rewrite),
// so it grades critical outright.
func (c *Client) triageTableBloat(ctx context.Context, db string) (Severity, string, error) {
	res, err := c.runTriageDiag(ctx, db, "bloat_table")
	if err != nil {
		return 0, "", err
	}
	n := len(res.Rows)
	if n == 0 {
		return SevOK, fmt.Sprintf("no heavily bloated tables (in %s)", db), nil
	}
	return SevCrit, fmt.Sprintf("%d table(s) heavily bloated, ~%s wasted (in %s)",
		n, humanize.Bytes(int64(diagSum(res, "bloat_bytes"))), db), nil
}

// triageIndexBloat is the index sibling; see indexBloatCritBytes for why it
// grades by wasted bytes rather than by presence.
func (c *Client) triageIndexBloat(ctx context.Context, db string) (Severity, string, error) {
	res, err := c.runTriageDiag(ctx, db, "bloat_index")
	if err != nil {
		return 0, "", err
	}
	n := len(res.Rows)
	wasted := diagSum(res, "bloat_bytes")
	sev := indexBloatSeverity(n, wasted)
	if n == 0 {
		return sev, fmt.Sprintf("no heavily bloated indexes (in %s)", db), nil
	}
	return sev, fmt.Sprintf("%d index(es) heavily bloated, ~%s wasted (in %s)",
		n, humanize.Bytes(int64(wasted)), db), nil
}

func indexBloatSeverity(n int, wastedBytes float64) Severity {
	switch {
	case n == 0:
		return SevOK
	case wastedBytes >= indexBloatCritBytes:
		return SevCrit
	}
	return SevWarn
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

// triageStaleStats leans on the stale_statistics diagnostic's own server-side
// filter (rows modified since ANALYZE outgrow live rows): any returned row is
// already a table the planner is reasoning about with stale statistics. Stale
// stats degrade plans rather than break the server, so it grades as a warning.
func (c *Client) triageStaleStats(ctx context.Context, db string) (Severity, string, error) {
	res, err := c.runTriageDiag(ctx, db, "stale_statistics")
	if err != nil {
		return 0, "", err
	}
	if len(res.Rows) == 0 {
		return SevOK, fmt.Sprintf("no tables with stale planner statistics (in %s)", db), nil
	}
	return SevWarn, fmt.Sprintf("%d table(s) with stale planner statistics (in %s)",
		len(res.Rows), db), nil
}

// triageFKMissingIndex flags foreign keys whose referencing columns have no
// supporting index — a footgun for cascading updates/deletes and join plans.
// It is a performance risk, not an outage, so it grades as a warning.
func (c *Client) triageFKMissingIndex(ctx context.Context, db string) (Severity, string, error) {
	res, err := c.runTriageDiag(ctx, db, "fk_missing_index")
	if err != nil {
		return 0, "", err
	}
	if len(res.Rows) == 0 {
		return SevOK, fmt.Sprintf("all foreign keys have a supporting index (in %s)", db), nil
	}
	return SevWarn, fmt.Sprintf("%d foreign key(s) without a supporting index (in %s)",
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
	return colIndex(res.Columns, name)
}

// diagNum reads the numeric value of row[idx], false when the column is
// missing or the cell carries no number (NULL, text).
func diagNum(row []DiagCell, idx int) (float64, bool) {
	if idx < 0 || idx >= len(row) || !row[idx].HasNum {
		return 0, false
	}
	return row[idx].Num, true
}

// diagSum totals a named column over every row; cells without a number
// (missing column, NULL, text) contribute nothing.
func diagSum(res *DiagResult, col string) float64 {
	idx := diagColIdx(res, col)
	var sum float64
	for _, row := range res.Rows {
		if v, ok := diagNum(row, idx); ok {
			sum += v
		}
	}
	return sum
}

// diagMax is diagSum's maximum sibling; with no numeric cells it returns 0.
func diagMax(res *DiagResult, col string) float64 {
	idx := diagColIdx(res, col)
	var maxV float64
	for _, row := range res.Rows {
		if v, ok := diagNum(row, idx); ok && v > maxV {
			maxV = v
		}
	}
	return maxV
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
