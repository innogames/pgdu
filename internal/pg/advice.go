package pg

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"pgdu/internal/humanize"
)

// AdviceLevel grades one system-overview recommendation. Crit is reserved for
// conditions that break or endanger the server (wraparound, a stuck archiver,
// a lost slot); performance findings stay Warn, and Info carries the purely
// informational notes (a shared_buffers share outside the usual band).
type AdviceLevel int

const (
	AdviceInfo AdviceLevel = iota
	AdviceWarn
	AdviceCrit
)

// AdviceTarget names the screen Enter opens from a recommendation row on the
// system overview: the diagnostic listing the offenders, the live view behind
// a session finding, or the settings browser for a GUC.
type AdviceTarget int

const (
	AdviceTargetNone       AdviceTarget = iota // nothing to open; Enter is disabled on the row
	AdviceTargetDiagnostic                     // the registry diagnostic DiagKey, run in DB
	AdviceTargetLockTree                       // the live lock tree
	AdviceTargetActivity                       // the live pg_stat_activity list
	AdviceTargetSettings                       // the pg_settings browser, filtered to Setting
)

// Advice is one recommendation the system overview derives from a
// MaintenanceInfo snapshot. Sections render Reason as the inline coloured note
// next to the metric it belongs to (looked up by Key), and the recommendations
// panel lists the same values with the concrete change — so the two can never
// disagree. Setting/Current/Suggested/Fix are empty for advice that has no
// single knob to turn.
type Advice struct {
	Key       string // stable id sections look up: "huge_pages", "max_wal_size", …
	Level     AdviceLevel
	Setting   string // GUC or host knob; "" for operational advice such as swap
	Current   string // human current value
	Suggested string // human suggested value; "" when none
	Reason    string // one short clause, used verbatim as the inline note
	Fix       string // copyable ALTER SYSTEM / sysctl line; "" when none
	// Target is where Enter on the recommendation row leads; DiagKey and DB
	// name the diagnostic (and the database it runs in) for AdviceTargetDiagnostic.
	Target  AdviceTarget
	DiagKey string
	DB      string
}

// AdviceSet is the ordered result of MaintAdvice: Crit first, then Warn, then
// Info, ties by Key.
type AdviceSet []Advice

// Find returns the advice with the given key, or nil.
func (a AdviceSet) Find(key string) *Advice {
	for i := range a {
		if a[i].Key == key {
			return &a[i]
		}
	}
	return nil
}

// Actionable is the subset the recommendations panel lists: everything graded
// Warn or Crit, plus Info items that come with a concrete Fix. Informational
// notes without a knob stay inline only, so the panel reads as a to-do list.
func (a AdviceSet) Actionable() AdviceSet {
	var out AdviceSet
	for _, ad := range a {
		if ad.Level >= AdviceWarn || ad.Fix != "" {
			out = append(out, ad)
		}
	}
	return out
}

// Overview thresholds, named so the opinions live in one place and can be
// tuned without hunting through rule code. Cumulative counters (deadlocks,
// temp bytes) are graded as per-day rates over the window since stats_reset,
// so a long-lived cluster is not punished for its uptime.
const (
	// shared_buffers outside 15–40 % of host RAM is not wrong, but is worth a
	// glance: below it the OS cache does the work, above it double-caching
	// eats the memory the planner assumes is page cache.
	sharedBuffersMinFrac = 0.15
	sharedBuffersMaxFrac = 0.40

	// effective_cache_size is only a planner hint, but one set at a fraction
	// of the real shared_buffers + page cache makes it undervalue index scans.
	ecsMinFracOfCache = 0.50

	// work_mem × max_connections is the theoretical worst case (each backend
	// may use several work_mem per query); past half of RAM it is a real
	// OOM path.
	workMemExposureWarnFrac = 0.50

	// A few dozen MB in swap is the kernel parking idle pages, not pressure;
	// only swap worth a twentieth of RAM says memory is actually short.
	swapWarnFracOfRAM = 0.05

	// Dirty buffers: a quarter of the pool waiting to be flushed means the
	// checkpointer / bgwriter are not keeping up with the write rate.
	bufDirtyWarnFrac = 0.25

	// Usage-count histogram shape: mostly hot with almost nothing evictable
	// means the working set does not fit; a big cold tail means it fits with
	// room to spare.
	bufHotTightFrac  = 0.70
	bufColdTightFrac = 0.10
	bufColdSlackFrac = 0.40

	// checkpoint_completion_target below 0.9 bunches checkpoint writes into
	// the first part of every interval for no benefit on modern storage.
	cctRecommended = 0.9

	// Average checkpoint interval under half of checkpoint_timeout means WAL
	// volume, not the timer, triggers checkpoints: max_wal_size is too small.
	checkpointIntervalWarnFrac = 0.50

	// A second of fsync per checkpoint is storage struggling to sync.
	checkpointSyncWarnMs = 1000

	// Dirty-buffer writes done by client backends stall the query that had to
	// evict; more than a tenth of all relation writes is the bgwriter falling
	// behind. The floor keeps a fresh cluster's handful of writes from grading.
	backendWriteShareWarnFrac = 0.10
	writeSplitMinWrites       = 1000

	// A sweep stopped at bgwriter_lru_maxpages wrote exactly that many pages;
	// when those capped sweeps account for this share of everything the
	// bgwriter cleaned, its per-round budget is the limit, not the demand.
	bgwriterCappedFrac = 0.25

	// Full-page images are the WAL cost of frequent checkpoints; flag the share
	// only once the record count is meaningful.
	fpiShareWarnFrac = 0.50
	fpiMinRecords    = 1000

	// More than this many tables already past their vacuum trigger means
	// autovacuum has a backlog, not a blip.
	autovacBacklogWarn = 20

	// A statement running for half a minute is worth a look on an OLTP
	// system; exported so the overview's grading matches this constant.
	LongQueryWarnSecs = 30

	// StatsFreshWindow: cumulative table counters younger than this are still
	// warming up — dead-tuple and scan ratios are not yet meaningful.
	StatsFreshWindow = 24 * time.Hour

	// wraparound: an age *at* autovacuum_freeze_max_age is not a fault — it is
	// exactly the trigger for the routine anti-wraparound autovacuum Postgres
	// relies on, so a busy cluster cycles every table through 100 % every few
	// days. Grading that as danger makes the check permanently red and trains
	// operators to ignore it. Freezing is only behind once a whole forced cycle
	// came and went without relfrozenxid advancing, i.e. past twice the limit.
	freezeBehindWarnFactor = 2.0
	// Real danger starts at vacuum_failsafe_age, where VACUUM drops its cost
	// delay and skips index cleanup to catch up; half of it is the last
	// comfortable warning before the 2^31 hard stop at which the server refuses
	// writes altogether.
	failsafeCritFrac = 0.50
	// vacuum_failsafe_age is PG14+; without it fall back to a fixed age rather
	// than to a fraction of an unknown.
	failsafeAgeFallback = 1_000_000_000
	// hardXIDLimit is 2^31-1, the age at which the server stops accepting
	// writes — the denominator that makes a critical message concrete. It is
	// the right limit for multixacts too: both wrap limits are computed half
	// the 32-bit space away from the oldest value (xidWrapLimit /
	// multiWrapLimit), so the usable distance is 2^31 in both counters.
	hardXIDLimit = 2_147_483_647

	// The band that used to be graded warn/crit (0.80–0.95 of freeze_max_age)
	// is where an operator most needs to be told the state is *normal*, so an
	// age approaching the routine trigger carries an explanatory Info note.
	// Below that there is nothing to say and the check stays silent.
	freezeRoutineNoticeFrac = 0.75

	// xmin horizon: vacuum cannot freeze anything newer than the oldest live
	// xmin, so a pinned horizon caps how far *any* vacuum can advance
	// relfrozenxid. That makes it the leading indicator — it moves days before
	// the ages do. A quarter of freeze_max_age already eats a quarter of the
	// budget; past half, freezing stalls cluster-wide.
	xminHorizonWarnFrac = 0.25
	xminHorizonCritFrac = 0.50

	// lock waits: any backend stuck on a lock is worth a look; one that has
	// waited half a minute is past ordinary contention.
	lockWaitCritSecs = 30

	// idle-in-transaction: a second or two between statements is normal churn
	// (poolers do it constantly); a minute holds locks and vacuum's horizon for
	// real, five minutes is a stuck client.
	idleXactWarnSecs = 60
	idleXactCritSecs = 300

	// long-running transaction: whatever it does, an open transaction pins the
	// xmin horizon. Half an hour is past any OLTP request; three hours is a
	// forgotten job.
	longXactWarnSecs = 1800
	longXactCritSecs = 3 * 3600

	// prepared (2PC) transactions pin the horizon by their mere existence; one
	// open for minutes is a coordinator that forgot to COMMIT/ROLLBACK PREPARED.
	preparedXactCritSecs = 300

	// connection saturation: past ~80 % of max_connections a spike yields
	// "too many clients"; the superuser-reserved slots are the last line.
	connSaturationWarnFrac = 0.80
	connSaturationCritFrac = 0.95

	// checkpoints: a high share of "requested" (as opposed to timed) checkpoints
	// means WAL volume keeps hitting max_wal_size before checkpoint_timeout. The
	// floor keeps a freshly started cluster green until there is a real sample.
	checkpointReqWarnFrac = 0.30
	checkpointReqCritFrac = 0.50
	checkpointMinTotal    = 10

	// stats extensions: at .max the extension evicts entries (pg_stat_statements
	// deallocates ~5 % at a time) and the counters the top-queries tool reads
	// silently stop being cumulative. Exported so the capacity bars colour
	// exactly where the advice fires.
	ExtCapacityNoticeFrac = 0.70
	ExtCapacityWarnFrac   = 0.90

	// replication slots: retained WAL past the slot's remaining safe_wal_size
	// (or past a fixed budget when max_slot_wal_keep_size is unlimited) means
	// invalidation is near; an inactive slot is normal churn, one with no
	// consumer for an hour retains WAL for nobody.
	slotRetainedCapBytes  = 16 << 30
	slotStaleInactiveSecs = 3600

	// replication: replay_lag reads NULL while a replica is idle and caught up,
	// so bytes-behind is the second signal. A non-streaming state (catchup,
	// backup) is transient and only warns.
	replLagWarnSecs      = 60
	replLagCritSecs      = 300
	replByteLagCritBytes = 1 << 30
	// standby side: the primary sends keepalives every wal_sender_timeout/2
	// even when idle, so a receiver that heard nothing for a minute is stalled.
	walReceiverStaleWarnSecs = 60
	walReceiverStaleCritSecs = 300

	// cache hit ratio: below ~90 % the working set clearly does not fit
	// shared_buffers; below 95 % it is starting to slip. Only graded once the
	// counters have seen real block traffic.
	cacheHitWarnPct   = 90
	cacheHitNoticePct = 95
	cacheHitMinBlocks = 100_000

	// SLRU caches: a poor hit ratio only matters once the cache sees real
	// traffic; the read floor keeps byte-sized test clusters green.
	slruHitWarnPct     = 90
	slruWarnReadsFloor = 1_000
	// slruAutoDivisor/Min/Max is how the server sizes an SLRU whose *_buffers
	// GUC is 0: shared_buffers / 512 clamped to [16, 1024] blocks.
	slruAutoDivisor = 512
	slruAutoMin     = 16
	slruAutoMax     = 1024

	// deadlocks / temp files: cumulative counters graded as a per-day average
	// over each database's own stats window (floored at one day in SQL so a
	// fresh reset does not extrapolate an hour's burst). A deadlock a day is a
	// lock-ordering bug that keeps biting; ten a day is transactions failing
	// routinely. Temp spill at 10 GB/day means work_mem is undersized for a
	// recurring query.
	deadlocksWarnPerDay = 1
	deadlocksCritPerDay = 10
	tempBytesWarnPerDay = 10 << 30

	// rollback ratio: a quarter of transactions rolling back is application
	// errors or serialization failures; gated on a minimum volume so a nearly
	// idle database never trips it.
	rollbackWarnFrac = 0.25
	rollbackMinXacts = 1000

	// sequences: consumed_pct is the fraction of the sequence's own range handed
	// out. Below 80 % there is nothing to do; at 95 % exhaustion (inserts fail at
	// 100 %) is close enough that the type/cycle decision is overdue.
	seqWarnPct = 80
	seqCritPct = 95
)

// MaintRates are per-minute rates between two consecutive overview samples.
// OK is false when there is no usable pair: no previous sample, a window under
// a second, or a counter that went backwards (a stats reset in between), in
// which case every rate is zero and callers show cumulative values only.
type MaintRates struct {
	OK     bool
	Window time.Duration

	ReadsPerMin       float64
	WritesPerMin      float64
	EvictionsPerMin   float64
	XactsPerMin       float64 // commits + rollbacks over pg_stat_database
	WALBytesPerMin    float64 // from the LSN delta, so it includes replayed WAL on a standby
	TempBytesPerMin   float64
	CheckpointsPerMin float64
	// ReplicaBytesPerMin is each replica's replay_lsn advance, keyed by
	// ReplicaStat.Key(); a replica seen in only one sample has no entry.
	ReplicaBytesPerMin map[string]float64
}

// ComputeMaintRates derives MaintRates from two samples of the same cluster.
func ComputeMaintRates(prev, cur *MaintenanceInfo) MaintRates {
	if prev == nil || cur == nil {
		return MaintRates{}
	}
	w := cur.SampledAt.Sub(prev.SampledAt)
	if w < time.Second {
		return MaintRates{}
	}
	r := MaintRates{OK: true, Window: w}
	mins := w.Minutes()
	per := func(a, b int64) float64 {
		d := b - a
		if d < 0 {
			r.OK = false
			return 0
		}
		return float64(d) / mins
	}
	if prev.IO.HasData && cur.IO.HasData {
		r.ReadsPerMin = per(prev.IO.Reads, cur.IO.Reads)
		r.WritesPerMin = per(prev.IO.Writes, cur.IO.Writes)
		r.EvictionsPerMin = per(prev.IO.Evictions, cur.IO.Evictions)
	}
	if prev.WAL.CurrentLSNBytes > 0 && cur.WAL.CurrentLSNBytes > 0 {
		r.WALBytesPerMin = per(prev.WAL.CurrentLSNBytes, cur.WAL.CurrentLSNBytes)
	}
	r.TempBytesPerMin = per(prev.TempBytes, cur.TempBytes)
	r.XactsPerMin = per(prev.XactCommit+prev.XactRollback, cur.XactCommit+cur.XactRollback)
	if prev.Checkpointer.HasData && cur.Checkpointer.HasData {
		r.CheckpointsPerMin = per(prev.Checkpointer.Timed+prev.Checkpointer.Requested,
			cur.Checkpointer.Timed+cur.Checkpointer.Requested)
	}
	if len(prev.Replicas) > 0 && len(cur.Replicas) > 0 {
		before := make(map[string]int64, len(prev.Replicas))
		for _, p := range prev.Replicas {
			before[p.Key()] = p.ReplayLSNBytes
		}
		r.ReplicaBytesPerMin = make(map[string]float64, len(cur.Replicas))
		for _, c := range cur.Replicas {
			if b, ok := before[c.Key()]; ok && b > 0 && c.ReplayLSNBytes > 0 {
				r.ReplicaBytesPerMin[c.Key()] = per(b, c.ReplayLSNBytes)
			}
		}
	}
	if !r.OK {
		return MaintRates{}
	}
	return r
}

// RecommendedMaxWALSize sizes max_wal_size so that the timer, not WAL volume,
// triggers checkpoints: a requested checkpoint fires once the WAL written
// since the last one reaches max_wal_size / (1 + checkpoint_completion_target)
// (CalculateCheckpointSegments in xlog.c), so the budget must cover
// rate × timeout × (1 + cct). The result is rounded up to whole GiB with a
// 1 GiB floor (the default). Zero when the inputs are unknown.
func RecommendedMaxWALSize(bytesPerSec float64, timeoutSecs int64, cct float64) int64 {
	if bytesPerSec <= 0 || timeoutSecs <= 0 {
		return 0
	}
	if cct <= 0 {
		cct = cctRecommended
	}
	need := int64(bytesPerSec * float64(timeoutSecs) * (1 + cct))
	const gib = 1 << 30
	if need <= gib {
		return gib
	}
	return (need + gib - 1) / gib * gib
}

// MaintAdvice evaluates every overview rule against one snapshot plus, once it
// has landed, the per-database schema sweep (nil while it is still loading).
// Host-relative rules need info.Host (only set when pgdu runs on the database
// host) and stay silent otherwise; every other rule degrades the same way on
// missing data.
func MaintAdvice(info *MaintenanceInfo, schema *SchemaHealth) AdviceSet {
	if info == nil {
		return nil
	}
	var out AdviceSet
	add := func(a Advice) {
		// A GUC recommendation always has somewhere to go: the settings
		// browser, filtered to the knob it names.
		if a.Target == AdviceTargetNone && a.Setting != "" {
			a.Target = AdviceTargetSettings
		}
		out = append(out, a)
	}

	adviseSafety(info, add)
	adviseHostMemory(info, add)
	adviseBufferCache(info, add)
	adviseCheckpoints(info, add)
	adviseWraparound(info, add)
	adviseHorizon(info, add)
	adviseAutovacuum(info, add)
	adviseSessions(info, add)
	adviseOperational(info, add)
	adviseCounters(info, add)
	adviseReplication(info, add)
	adviseObservability(info, add)
	adviseSchema(schema, add)

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Level != out[j].Level {
			return out[i].Level > out[j].Level
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func adviseHostMemory(info *MaintenanceInfo, add func(Advice)) {
	host := info.Host
	if host.Total <= 0 {
		return // not on the database host (or /proc unreadable): nothing host-relative to say
	}

	if hp := info.Settings["huge_pages"]; (hp == "try" || hp == "on") && host.HugePagesTotal == 0 {
		// A warning, not critical: the server runs fine on 4 kB pages, it
		// just pays TLB pressure and page-table memory for a large pool.
		a := Advice{
			Key: "huge_pages", Level: AdviceWarn, Setting: "huge_pages", Current: hp,
			Reason: "none allocated on the host — silently running on 4 kB pages",
		}
		if n := info.Tuning.ShmemHugePages; n > 0 {
			a.Reason = fmt.Sprintf("none allocated — set vm.nr_hugepages ≥ %d", n)
			a.Suggested = fmt.Sprintf("vm.nr_hugepages=%d", n)
			a.Fix = fmt.Sprintf("sysctl -w vm.nr_hugepages=%d   # host-side; then restart postgres", n)
		}
		add(a)
	}

	if used := host.SwapUsed(); float64(used) > swapWarnFracOfRAM*float64(host.Total) {
		add(Advice{
			Key: "swap", Level: AdviceWarn, Current: humanize.Bytes(used),
			Reason: fmt.Sprintf("swap in use (%.0f%% of RAM) — Postgres memory may be paged out",
				100*float64(used)/float64(host.Total)),
		})
	}

	sb, sbOK := info.SettingBytes["shared_buffers"]
	if sbOK && sb > 0 {
		frac := float64(sb) / float64(host.Total)
		if frac < sharedBuffersMinFrac || frac > sharedBuffersMaxFrac {
			add(Advice{
				Key: "shared_buffers", Level: AdviceInfo, Setting: "shared_buffers",
				Current: info.Settings["shared_buffers"],
				Reason:  fmt.Sprintf("%.0f%% of host RAM (15–40%% is the usual band)", frac*100),
			})
		}
	}

	// The trigger is what the host actually caches right now; the suggestion
	// is the ⅔-of-RAM rule of thumb rather than that momentary figure, which
	// a bulk load or a neighbouring process can move by gigabytes.
	if ecs, ok := info.SettingBytes["effective_cache_size"]; ok && sbOK && host.Cached > 0 {
		actual := sb + host.Cached
		if float64(ecs) < ecsMinFracOfCache*float64(actual) {
			sugg := max(host.Total*2/3/(1<<30), 1) << 30 // whole GiB, rounded down
			add(Advice{
				Key: "effective_cache_size", Level: AdviceWarn, Setting: "effective_cache_size",
				Current: info.Settings["effective_cache_size"], Suggested: gucBytes(sugg),
				Reason: fmt.Sprintf("planner assumes %s of cache, host has ~%s (shared_buffers + page cache); ⅔ of RAM is the rule of thumb",
					humanize.Bytes(ecs), humanize.Bytes(actual)),
				Fix: alterReload("effective_cache_size", gucBytes(sugg)),
			})
		}
	}

	if wm, ok := info.SettingBytes["work_mem"]; ok && wm > 0 && info.MaxConns > 0 {
		theoretical := wm * int64(info.MaxConns)
		if float64(theoretical) > workMemExposureWarnFrac*float64(host.Total) {
			sugg := max(host.Total/4/int64(info.MaxConns)/(1<<20), 1) << 20 // whole MiB, rounded down
			reason := fmt.Sprintf("work_mem × max_connections = %s (%.0f%% of RAM)",
				humanize.Bytes(theoretical), 100*float64(theoretical)/float64(host.Total))
			if n := info.TotalConns(); n > 0 {
				reason += fmt.Sprintf(", × %d in use = %s", n, humanize.Bytes(wm*int64(n)))
			}
			add(Advice{
				Key: "work_mem", Level: AdviceWarn, Setting: "work_mem",
				Current: info.Settings["work_mem"], Suggested: gucBytes(sugg),
				Reason: reason, Fix: alterReload("work_mem", gucBytes(sugg)),
			})
		}
	}
}

func adviseBufferCache(info *MaintenanceInfo, add func(Advice)) {
	bc := info.BufCache
	if bc.HasData && bc.DirtyFrac() > bufDirtyWarnFrac {
		add(Advice{
			Key: "buffercache_dirty", Level: AdviceWarn,
			Reason: fmt.Sprintf("%.0f%% of used buffers dirty — checkpointer/bgwriter behind", bc.DirtyFrac()*100),
		})
	}
	if cold, hot, ok := bc.UsageFracs(); ok {
		switch {
		case hot >= bufHotTightFrac && cold < bufColdTightFrac:
			add(Advice{Key: "buffercache_tight", Level: AdviceInfo,
				Reason: "cache tight — working set exceeds shared_buffers"})
		case cold > bufColdSlackFrac:
			add(Advice{Key: "buffercache_slack", Level: AdviceInfo,
				Reason: "cache has slack — shared_buffers is not the constraint"})
		}
	}
	if info.IO.HasData && info.IO.BackendFsyncs > 0 {
		add(Advice{
			Key: "backend_fsyncs", Level: AdviceCrit, Current: strconv.FormatInt(info.IO.BackendFsyncs, 10),
			Reason: "fsyncs issued by backends — checkpointer can't keep up",
		})
	}
	// Two signals on the same knob: backends writing dirty pages themselves is
	// the bgwriter visibly behind (warn); sweeps routinely stopping at the cap
	// is it being held back before that shows (info). One advice either way.
	a := Advice{Key: "bgwriter_lru_maxpages", Setting: "bgwriter_lru_maxpages",
		Current: info.Settings["bgwriter_lru_maxpages"]}
	if frac, ok := info.IOSplit.ClientWriteFrac(); ok && frac > backendWriteShareWarnFrac &&
		info.IOSplit.CheckpointerWrites+info.IOSplit.BgwriterWrites+info.IOSplit.ClientWrites >= writeSplitMinWrites {
		a.Level = AdviceWarn
		a.Reason = fmt.Sprintf("%.0f%% of dirty-page writes done by backends themselves", frac*100)
	} else if bg, cur := info.Bgwriter, info.Tuning.BgwriterLRUMaxpages; cur > 0 && bg.BuffersClean > 0 &&
		float64(bg.MaxwrittenClean*cur)/float64(bg.BuffersClean) > bgwriterCappedFrac {
		a.Level = AdviceInfo
		a.Reason = fmt.Sprintf("%s sweeps stopped at the %d-page cap — %.0f%% of what it cleaned",
			compactCount(bg.MaxwrittenClean), cur, 100*float64(bg.MaxwrittenClean*cur)/float64(bg.BuffersClean))
	}
	if a.Reason != "" {
		if cur := info.Tuning.BgwriterLRUMaxpages; cur > 0 && cur < 1000 {
			sugg := min(cur*4, 1000)
			a.Suggested = strconv.FormatInt(sugg, 10)
			a.Fix = alterReload("bgwriter_lru_maxpages", strconv.FormatInt(sugg, 10))
		}
		add(a)
	}
}

func adviseCheckpoints(info *MaintenanceInfo, add func(Advice)) {
	cp := info.Checkpointer
	if cct := info.Tuning.CheckpointCompletion; cct > 0 && cct < cctRecommended {
		add(Advice{
			Key: "checkpoint_completion_target", Level: AdviceInfo, Setting: "checkpoint_completion_target",
			Current: info.Settings["checkpoint_completion_target"], Suggested: "0.9",
			Reason: fmt.Sprintf("writes bunched into the first %.0f%% of each interval; 0.9 spreads them", cct*100),
			Fix:    alterReload("checkpoint_completion_target", "0.9"),
		})
	}

	timeout := info.Tuning.CheckpointTimeoutSecs
	interval, haveInterval := info.AvgCheckpointInterval()
	frequent := haveInterval && timeout > 0 && interval < time.Duration(checkpointIntervalWarnFrac*float64(timeout))*time.Second

	if cp.HasData {
		total := cp.Timed + cp.Requested
		level := AdviceInfo
		var reasons []string
		if total >= checkpointMinTotal {
			frac := float64(cp.Requested) / float64(total)
			switch {
			case frac >= checkpointReqCritFrac:
				level = AdviceCrit
			case frac >= checkpointReqWarnFrac:
				level = AdviceWarn
			}
			if level > AdviceInfo {
				reasons = append(reasons, fmt.Sprintf("%.0f%% of checkpoints WAL-driven", frac*100))
			}
		}
		if frequent {
			level = max(level, AdviceWarn)
			reasons = append(reasons, fmt.Sprintf("checkpoint every %s (timeout %s)",
				roundDuration(interval), roundDuration(time.Duration(timeout)*time.Second)))
		}
		if level > AdviceInfo {
			a := Advice{
				Key: "max_wal_size", Level: level, Setting: "max_wal_size",
				Current: info.Settings["max_wal_size"], Reason: strings.Join(reasons, ", "),
			}
			// The since-reset average is the stable input; the two-sample
			// session rate swings with whatever ran in the last minute. When
			// checkpoints are WAL-driven yet the average says the current size
			// should suffice, the load is bursty and the average undersizes it —
			// fall back to the classic step of doubling.
			cur := info.SettingBytes["max_wal_size"]
			rec := int64(0)
			if rate, ok := info.WALBytesPerSecSinceReset(); ok {
				rec = RecommendedMaxWALSize(rate, timeout, info.Tuning.CheckpointCompletion)
			}
			if rec <= cur && cur > 0 {
				rec = cur * 2
			}
			if rec > cur {
				a.Suggested = gucBytes(rec)
				a.Fix = alterReload("max_wal_size", gucBytes(rec))
			}
			add(a)
		}
		if syncMs, ok := info.AvgSyncPerCheckpointMs(); ok && syncMs > checkpointSyncWarnMs {
			add(Advice{
				Key: "checkpoint_sync", Level: AdviceWarn,
				Reason: fmt.Sprintf("avg %.0f ms fsync per checkpoint — storage is slow to sync", syncMs),
			})
		}
	}

	if info.WAL.HasData && info.WAL.BuffersFull > 0 {
		a := Advice{
			Key: "wal_buffers", Level: AdviceWarn, Setting: "wal_buffers",
			Current: info.Settings["wal_buffers"],
			Reason:  compactCount(info.WAL.BuffersFull) + " stalls waiting for WAL buffer space",
		}
		if cur, ok := info.SettingBytes["wal_buffers"]; ok && cur > 0 {
			sugg := int64(64 << 20)
			if cur >= sugg {
				sugg = cur * 2
			}
			a.Suggested = gucBytes(sugg)
			a.Fix = alterRestart("wal_buffers", gucBytes(sugg))
		}
		add(a)
	}

	if frac, ok := info.WAL.FPIFrac(); ok && frac > fpiShareWarnFrac && info.WAL.Records >= fpiMinRecords && frequent {
		a := Advice{
			Key: "wal_fpi", Level: AdviceInfo,
			Reason: fmt.Sprintf("%.0f%% of WAL records are full-page images — frequent checkpoints inflate WAL", frac*100),
		}
		// Compression is the knob that shrinks the images themselves; only
		// offer it when it is actually off.
		if info.Settings["wal_compression"] == "off" {
			a.Setting, a.Current, a.Suggested = "wal_compression", "off", "on"
			a.Reason += "; wal_compression shrinks them"
			a.Fix = alterReload("wal_compression", "on")
		}
		add(a)
	}
}

func adviseAutovacuum(info *MaintenanceInfo, add func(Advice)) {
	av, t := info.Autovac, info.Tuning
	if t.AutovacMaxWorkers > 0 && av.WorkersBusy >= t.AutovacMaxWorkers && t.EffectiveAutovacCostDelayMs() > 0 {
		limit := t.EffectiveAutovacCostLimit()
		a := Advice{
			Key: "autovacuum_cost", Level: AdviceWarn, Setting: "autovacuum_vacuum_cost_limit",
			Current: strconv.FormatInt(limit, 10),
			Reason: fmt.Sprintf("all %d workers busy with cost_delay %gms — raise cost_limit or lower the delay",
				t.AutovacMaxWorkers, t.EffectiveAutovacCostDelayMs()),
		}
		if sugg := min(limit*2, 10000); sugg > limit {
			a.Suggested = strconv.FormatInt(sugg, 10)
			a.Fix = alterReload("autovacuum_vacuum_cost_limit", strconv.FormatInt(sugg, 10))
		}
		add(a)
	}
	if av.OverThreshold > autovacBacklogWarn {
		reason := fmt.Sprintf("%d tables past their vacuum threshold", av.OverThreshold)
		if len(av.OverTop) > 0 {
			reason += " (" + strings.Join(av.OverTop, ", ") + ", …)"
		}
		add(Advice{Key: "autovacuum_backlog", Level: AdviceWarn, Reason: reason})
	}
}

func adviseSessions(info *MaintenanceInfo, add func(Advice)) {
	s := info.Sess
	if s.IdleXactPID == 0 || s.IdleXactSecs < idleXactWarnSecs {
		return
	}
	level := AdviceWarn
	if s.IdleXactSecs >= idleXactCritSecs {
		level = AdviceCrit
	}
	app := s.IdleXactApp
	if app == "" {
		app = "no application_name"
	}
	a := Advice{
		Key: "idle_in_transaction", Level: level, Setting: "idle_in_transaction_session_timeout",
		Current: info.Settings["idle_in_transaction_session_timeout"],
		Reason: fmt.Sprintf("pid %d (%s) idle in transaction for %s — holds locks and pins vacuum's horizon",
			s.IdleXactPID, app, roundDuration(time.Duration(s.IdleXactSecs)*time.Second)),
	}
	if info.Tuning.IdleInTxnTimeoutMs == 0 {
		a.Current = "0 (off)"
		a.Suggested = "10min"
		a.Fix = alterReload("idle_in_transaction_session_timeout", "10min")
	}
	add(a)
}

func adviseObservability(info *MaintenanceInfo, add func(Advice)) {
	if info.Statements.Installed {
		if info.Settings["pg_stat_statements.track"] == "none" {
			add(Advice{
				Key: "pg_stat_statements.track", Level: AdviceCrit, Setting: "pg_stat_statements.track",
				Current: "none", Suggested: "top", Reason: "installed but tracking nothing",
				Fix: alterReload("pg_stat_statements.track", "top"),
			})
		}
		if info.Statements.FillRatio() >= ExtCapacityWarnFrac {
			sugg := strconv.FormatInt(info.Statements.Max*2, 10)
			add(Advice{
				Key: "pg_stat_statements.max", Level: AdviceWarn, Setting: "pg_stat_statements.max",
				Current: strconv.FormatInt(info.Statements.Max, 10), Suggested: sugg,
				Reason: "entries being evicted — raise it or reset",
				Fix:    alterRestart("pg_stat_statements.max", sugg),
			})
		}
	}
	if v, ok := info.Settings["track_io_timing"]; ok && v == "off" {
		add(Advice{
			Key: "track_io_timing", Level: AdviceWarn, Setting: "track_io_timing",
			Current: "off", Suggested: "on", Reason: "read/write timings unavailable",
			Fix: alterReload("track_io_timing", "on"),
		})
	}
	if v, ok := info.Settings["log_checkpoints"]; ok && v == "off" {
		add(Advice{
			Key: "log_checkpoints", Level: AdviceInfo, Setting: "log_checkpoints",
			Current: "off", Suggested: "on", Reason: "checkpoint timings not logged",
			Fix: alterReload("log_checkpoints", "on"),
		})
	}
	if q := info.Qualstats; q.Installed && q.Max > 0 && q.FillRatio() >= ExtCapacityWarnFrac {
		sugg := strconv.FormatInt(q.Max*2, 10)
		add(Advice{
			Key: "pg_qualstats.max", Level: AdviceWarn, Setting: "pg_qualstats.max",
			Current: strconv.FormatInt(q.Max, 10), Suggested: sugg,
			Reason: "entries being dropped — new predicates are silently ignored",
			Fix:    alterRestart("pg_qualstats.max", sugg),
		})
	}
	if v, ok := info.Settings["log_lock_waits"]; ok && v == "off" {
		add(Advice{
			Key: "log_lock_waits", Level: AdviceInfo, Setting: "log_lock_waits",
			Current: "off", Suggested: "on", Reason: "lock waits past deadlock_timeout are not logged",
			Fix: alterReload("log_lock_waits", "on"),
		})
	}
	// Spills are only worth logging once there are some; the default -1 is
	// fine on a server that never spills.
	if v, ok := info.Settings["log_temp_files"]; ok && v == "-1" && info.TempBytes > 0 {
		add(Advice{
			Key: "log_temp_files", Level: AdviceInfo, Setting: "log_temp_files",
			Current: "-1", Suggested: "10MB", Reason: "temp-file spills are not logged, so the spilling queries stay anonymous",
			Fix: alterReload("log_temp_files", "10MB"),
		})
	}
}

// adviseSafety covers the settings that must never be off on a production
// server: each one silently trades durability or maintenance for speed.
func adviseSafety(info *MaintenanceInfo, add func(Advice)) {
	set := info.Settings
	if v := set["autovacuum"]; v != "" && v != "on" {
		add(Advice{
			Key: "autovacuum", Level: AdviceCrit, Setting: "autovacuum", Current: v, Suggested: "on",
			Reason: "autovacuum is off — dead tuples and wraparound age accumulate unchecked",
			Fix:    alterReload("autovacuum", "on"),
		})
	}
	if v := set["track_counts"]; v == "off" {
		add(Advice{
			Key: "track_counts", Level: AdviceCrit, Setting: "track_counts", Current: "off", Suggested: "on",
			Reason: "statistics collection is off — autovacuum has nothing to act on and pg_stat_* views stay empty",
			Fix:    alterReload("track_counts", "on"),
		})
	}
	if v := set["fsync"]; v == "off" {
		add(Advice{
			Key: "fsync", Level: AdviceCrit, Setting: "fsync", Current: "off", Suggested: "on",
			Reason: "commits are not durable — a crash or power loss corrupts the cluster",
			Fix:    alterReload("fsync", "on"),
		})
	}
	if v := set["full_page_writes"]; v == "off" {
		add(Advice{
			Key: "full_page_writes", Level: AdviceCrit, Setting: "full_page_writes", Current: "off", Suggested: "on",
			Reason: "torn pages after a crash cannot be repaired from WAL",
			Fix:    alterReload("full_page_writes", "on"),
		})
	}
	// Informational only: checksums are enabled offline (pg_checksums), so
	// there is no ALTER SYSTEM to offer.
	if v := set["data_checksums"]; v == "off" {
		add(Advice{
			Key: "data_checksums", Level: AdviceInfo, Setting: "data_checksums", Current: "off",
			Reason: "storage corruption goes undetected (enable offline with pg_checksums)",
		})
	}
}

// freezeGrade is what freezeLevel resolved about one age: the level, plus the
// two framings a message needs — the age as a multiple of its own
// *_freeze_max_age (the "is freezing keeping up" question) and as a share of
// the 2^31 hard limit (the "how much room is left" question).
type freezeGrade struct {
	Level   AdviceLevel
	Factor  float64 // age / maxAge; 2.5 = two and a half freeze_max_age cycles
	PctOf31 float64 // age as a percentage of 2^31-1
	Ok      bool    // false when either input is unknown, so nothing is graded
}

// worthSaying reports whether a graded age is worth a finding at all: any
// warn/crit tier, plus the Info band close to the routine freeze trigger where
// the point is to say out loud that the state is normal. A young age produces
// nothing.
func (g freezeGrade) worthSaying() bool {
	return g.Ok && (g.Level >= AdviceWarn || g.Factor >= freezeRoutineNoticeFrac)
}

// freezeLevel grades a transaction-ID (or multixact) age. Deliberately *not*
// against the distance to the next routine anti-wraparound autovacuum: that
// event is normal maintenance, not a fault. Warn once the age is a multiple of
// autovacuum_freeze_max_age (a forced cycle passed without advancing
// relfrozenxid), go critical only near the vacuum failsafe and the 2^31 stop.
// failsafeAge is 0 when vacuum_failsafe_age is unavailable (PG < 14).
func freezeLevel(age, maxAge, failsafeAge int64) freezeGrade {
	if maxAge <= 0 || age <= 0 {
		return freezeGrade{}
	}
	g := freezeGrade{
		Level:   AdviceInfo,
		Factor:  float64(age) / float64(maxAge),
		PctOf31: 100 * float64(age) / float64(hardXIDLimit),
		Ok:      true,
	}
	critAge := int64(failsafeCritFrac * float64(failsafeAge))
	if failsafeAge <= 0 {
		critAge = failsafeAgeFallback
	}
	switch {
	case age > critAge:
		g.Level = AdviceCrit
	case g.Factor > freezeBehindWarnFactor:
		g.Level = AdviceWarn
	}
	return g
}

// inDB is the " (in db)" tail cluster-wide advice uses to name the database a
// per-database finding came from.
func inDB(db string) string {
	if db == "" {
		return ""
	}
	return " (in " + db + ")"
}

// freezeReason composes the one-line note for a graded age. counter names the
// catalog column the age came from ("datfrozenxid"), guc its freeze limit, and
// db the database holding it. The Info wording exists to say out loud that a
// routine anti-wraparound autovacuum is not a problem — the check used to shout
// at exactly this state.
func freezeReason(g freezeGrade, age int64, counter, guc, db string) string {
	switch g.Level {
	case AdviceCrit:
		return fmt.Sprintf("%s age %s is %.0f%% of the 2^31 wraparound limit — VACUUM runs in failsafe mode "+
			"(no cost delay, no index cleanup) and the server stops accepting writes at the limit%s",
			counter, compactCount(age), g.PctOf31, inDB(db))
	case AdviceWarn:
		return fmt.Sprintf("%s age %s is %.1f× %s — a forced anti-wraparound cycle passed without advancing "+
			"relfrozenxid; check the xmin_horizon finding or a disabled/starved autovacuum%s",
			counter, compactCount(age), g.Factor, guc, inDB(db))
	default:
		return fmt.Sprintf("%s age %s is %.0f%% of %s — a routine anti-wraparound autovacuum is due%s",
			counter, compactCount(age), g.Factor*100, guc, inDB(db))
	}
}

// adviseWraparound grades how far behind freezing is, not how close the next
// routine anti-wraparound autovacuum is. Below the warn tier it still emits an
// Info note so the overview's age rows read explained rather than bare;
// Actionable() keeps Info-without-a-Fix out of the recommendations panel, so a
// healthy cluster shows nothing to do.
func adviseWraparound(info *MaintenanceInfo, add func(Advice)) {
	if g := freezeLevel(info.XidAge, info.FreezeMaxAge, info.FailsafeAge); g.worthSaying() {
		add(Advice{
			Key: "wraparound", Level: g.Level, Current: compactCount(info.XidAge),
			Reason: freezeReason(g, info.XidAge, "datfrozenxid", "autovacuum_freeze_max_age", info.XidAgeDB),
			Target: AdviceTargetDiagnostic, DiagKey: "wraparound_tables", DB: info.XidAgeDB,
		})
	}
	// Multixacts have their own 32-bit counter, their own freeze limit and
	// their own failsafe. Their limit defaults to twice the XID one while the
	// failsafe defaults to the same 1.6 B, so at stock settings the warn band
	// is narrow and an old multixact age reaches the critical tier directly.
	if g := freezeLevel(info.MxidAge, info.MxidFreezeMaxAge, info.MxidFailsafeAge); g.worthSaying() {
		add(Advice{
			Key: "mxid_wraparound", Level: g.Level, Current: compactCount(info.MxidAge),
			Reason: freezeReason(g, info.MxidAge, "datminmxid", "autovacuum_multixact_freeze_max_age", info.MxidAgeDB),
		})
	}
}

// adviseHorizon grades the oldest live xmin against autovacuum_freeze_max_age.
// This is the finding that actually means trouble: VACUUM cannot freeze a tuple
// newer than the oldest snapshot anyone still holds, so while the horizon is
// pinned no amount of vacuuming advances relfrozenxid anywhere in the cluster —
// the ages then climb on their own and the wraparound findings follow days later.
// The message names the holder because the fix is always "deal with that
// backend / slot / prepared transaction", never "vacuum harder".
func adviseHorizon(info *MaintenanceInfo, add func(Advice)) {
	h := info.Horizon
	maxAge := info.FreezeMaxAge
	if maxAge <= 0 {
		return
	}

	// pg_stat_activity hides other users' rows without pg_read_all_stats, so an
	// unreadable horizon is an unknown, not an all-clear. Say so rather than
	// staying silent — a false green here is what the old check trained people
	// to chase by hand.
	if h.Age <= 0 {
		if h.Restricted {
			add(Advice{
				Key: "xmin_horizon", Level: AdviceWarn, Current: "unknown",
				Reason: "could not read the oldest xmin: pg_stat_activity is filtered to your own backends " +
					"without pg_read_all_stats, and no replication slot or prepared transaction pins one — " +
					"grant pg_monitor to see whether another user's session holds the horizon",
			})
		}
		return
	}

	frac := float64(h.Age) / float64(maxAge)
	lvl := AdviceInfo
	switch {
	case frac > xminHorizonCritFrac:
		lvl = AdviceCrit
	case frac > xminHorizonWarnFrac:
		lvl = AdviceWarn
	}
	if lvl < AdviceWarn {
		return
	}

	holder := h.Holder
	if holder == "" {
		holder = "an unidentified " + h.Kind
	}
	reason := fmt.Sprintf("oldest xmin is %s transactions old (%.0f%% of autovacuum_freeze_max_age), held by %s — "+
		"VACUUM cannot freeze past it, so relfrozenxid stalls cluster-wide", compactCount(h.Age), frac*100, holder)
	if h.Restricted {
		reason += " — and pg_stat_activity is filtered without pg_read_all_stats, so an older xmin may be hidden"
	}

	a := Advice{Key: "xmin_horizon", Level: lvl, Current: compactCount(h.Age), Reason: reason}
	// Point Enter at whichever existing view lists the holder's kind, so the
	// finding leads straight to the row the operator has to act on.
	switch h.Kind {
	case horizonKindIdleXact:
		a.Target, a.DiagKey = AdviceTargetDiagnostic, "idle_in_xact_holders"
	case horizonKindSlot:
		a.Target, a.DiagKey = AdviceTargetDiagnostic, "replication_slots"
	case horizonKindBackend:
		a.Target = AdviceTargetActivity
	}
	add(a)
}

// adviseOperational grades the live operational rows: connection saturation,
// lock waits, the longest open transaction, prepared transactions, the WAL
// archiver and pending configuration.
func adviseOperational(info *MaintenanceInfo, add func(Advice)) {
	if info.MaxConns > 0 {
		used := info.TotalConns()
		frac := float64(used) / float64(info.MaxConns)
		lvl := AdviceInfo
		switch {
		case frac >= connSaturationCritFrac:
			lvl = AdviceCrit
		case frac >= connSaturationWarnFrac:
			lvl = AdviceWarn
		}
		if lvl >= AdviceWarn {
			add(Advice{
				Key: "max_connections", Level: lvl, Setting: "max_connections",
				Current: fmt.Sprintf("%d/%d", used, info.MaxConns),
				Reason:  fmt.Sprintf("%.0f%% of max_connections in use — pool connections rather than raising the limit", frac*100),
				Target:  AdviceTargetActivity,
			})
		}
	}

	if info.LockWaits > 0 || len(info.Blocked) > 0 {
		n := max(info.LockWaits, len(info.Blocked))
		longest := 0.0
		for _, b := range info.Blocked {
			longest = max(longest, b.WaitSec)
		}
		lvl := AdviceWarn
		if longest > lockWaitCritSecs {
			lvl = AdviceCrit
		}
		reason := fmt.Sprintf("%d backend(s) waiting on locks", n)
		if longest > 0 {
			reason += ", longest " + shortSecs(longest)
		}
		add(Advice{Key: "lock_waits", Level: lvl, Current: strconv.Itoa(n), Reason: reason, Target: AdviceTargetLockTree})
	}

	if secs := info.LongestXactSec; secs >= longXactWarnSecs {
		lvl := AdviceWarn
		if secs >= longXactCritSecs {
			lvl = AdviceCrit
		}
		add(Advice{
			Key: "long_xact", Level: lvl, Current: shortSecs(secs),
			Reason: "a transaction has been open for " + shortSecs(secs) + " — it pins the xmin horizon, vacuum cannot reclaim past it",
			Target: AdviceTargetActivity,
		})
	}

	if info.PreparedXacts > 0 {
		lvl := AdviceWarn
		if info.OldestPrepSec > preparedXactCritSecs {
			lvl = AdviceCrit
		}
		add(Advice{
			Key: "prepared_xacts", Level: lvl, Current: strconv.Itoa(info.PreparedXacts),
			Reason: fmt.Sprintf("%d prepared transaction(s), oldest %s — holds locks and the xmin horizon until COMMIT/ROLLBACK PREPARED",
				info.PreparedXacts, shortSecs(info.OldestPrepSec)),
		})
	}

	if info.ArchiveFailed > 0 {
		a := Advice{
			Key: "wal_archiver", Level: AdviceWarn, Current: compactCount(info.ArchiveFailed) + " failed",
			Reason: "archive failures since stats reset — archiving has succeeded since",
		}
		// A failure newer than the last success is an archiver that is stuck
		// right now: pg_wal grows until it gets through.
		if !info.ArchiveLastFailedTime.IsZero() && info.ArchiveLastFailedTime.After(info.ArchiveLastTime) {
			a.Level = AdviceCrit
			a.Reason = fmt.Sprintf("archiver stuck: %s failed %s ago and nothing has been archived since — pg_wal grows until it succeeds",
				info.ArchiveLastFailed, shortSecs(info.SampledAt.Sub(info.ArchiveLastFailedTime).Seconds()))
		}
		add(a)
	}
	// archive_command/archive_library are superuser-only GUCs; a missing key
	// means pgdu cannot see them, not that they are empty.
	cmd, cmdOK := info.Settings["archive_command"]
	lib, libOK := info.Settings["archive_library"]
	if mode := info.Settings["archive_mode"]; mode != "" && mode != "off" && cmdOK && libOK &&
		strings.TrimSpace(cmd) == "" && strings.TrimSpace(lib) == "" {
		add(Advice{
			Key: "archive_mode", Level: AdviceCrit, Setting: "archive_mode", Current: mode,
			Reason: "archive_command and archive_library are both empty — WAL is kept for an archiver that never runs",
		})
	}

	if info.PendingRestart > 0 {
		add(Advice{
			Key: "pending_restart", Level: AdviceWarn, Current: strconv.Itoa(info.PendingRestart),
			Reason: "setting(s) changed but waiting for a restart: " + strings.Join(info.PendingRestartSettings, ", "),
			Target: AdviceTargetSettings,
		})
	}
	if info.PendingReload > 0 {
		add(Advice{
			Key: "pending_reload", Level: AdviceInfo, Current: strconv.Itoa(info.PendingReload),
			Reason: "setting(s) changed but not yet reloaded: " + strings.Join(info.PendingReloadSettings, ", "),
			Fix:    "SELECT pg_reload_conf();",
			Target: AdviceTargetSettings,
		})
	}
}

// adviseCounters grades the cumulative pg_stat_database / pg_stat_slru figures.
func adviseCounters(info *MaintenanceInfo, add func(Advice)) {
	if info.CacheBlocks >= cacheHitMinBlocks && info.CacheHitRatio < cacheHitNoticePct {
		lvl := AdviceInfo
		if info.CacheHitRatio < cacheHitWarnPct {
			lvl = AdviceWarn
		}
		add(Advice{
			Key: "cache_hit", Level: lvl, Current: fmt.Sprintf("%.1f%%", info.CacheHitRatio),
			Reason: fmt.Sprintf("buffer cache hit ratio %.1f%% — the working set does not fit shared_buffers, or scans dominate", info.CacheHitRatio),
			Target: AdviceTargetDiagnostic, DiagKey: "database_stats",
		})
	}

	var worst *SLRUStat
	for i := range info.SLRU {
		s := &info.SLRU[i]
		if s.Reads < slruWarnReadsFloor || s.HitPct() >= slruHitWarnPct {
			continue
		}
		if worst == nil || s.HitPct() < worst.HitPct() {
			worst = s
		}
	}
	if worst != nil {
		a := Advice{
			Key: "slru", Level: AdviceWarn, Current: worst.Name,
			Reason: fmt.Sprintf("%s SLRU hit ratio %.0f%% over %s reads — its buffers are too small", worst.Name, worst.HitPct(), compactCount(worst.Reads)),
			Target: AdviceTargetDiagnostic, DiagKey: "slru_stats",
		}
		if guc := worst.BuffersGUC(); guc != "" {
			a.Setting = guc
			cur := info.Tuning.SLRUBuffers[guc]
			eff := cur
			if eff == 0 {
				eff = autoSLRUBuffers(info.SettingBytes["shared_buffers"])
			}
			if eff > 0 {
				const blk = 8 << 10
				a.Current = gucBytes(eff * blk)
				if cur == 0 {
					a.Current += " (auto)"
				}
				a.Suggested = gucBytes(eff * 2 * blk)
				a.Fix = alterRestart(guc, a.Suggested)
			}
		}
		add(a)
	}

	if info.Deadlocks > 0 && info.DeadlocksPerDay >= deadlocksWarnPerDay {
		lvl := AdviceWarn
		if info.DeadlocksPerDay >= deadlocksCritPerDay {
			lvl = AdviceCrit
		}
		add(Advice{
			Key: "deadlocks", Level: lvl, Current: compactCount(info.Deadlocks),
			Reason: fmt.Sprintf("~%s deadlocks/day since stats reset — a lock-ordering bug in the application that keeps biting", perDay(info.DeadlocksPerDay)),
			Target: AdviceTargetDiagnostic, DiagKey: "database_stats",
		})
	}
	if info.TempBytesPerDay >= tempBytesWarnPerDay {
		add(Advice{
			Key: "temp_files", Level: AdviceWarn, Current: humanize.Bytes(info.TempBytes),
			Reason: fmt.Sprintf("~%s/day spilled to temp files — work_mem is undersized for a recurring query", humanize.Bytes(int64(info.TempBytesPerDay))),
			Target: AdviceTargetDiagnostic, DiagKey: "database_stats",
		})
	}
	if total := info.XactCommit + info.XactRollback; total >= rollbackMinXacts {
		if frac := float64(info.XactRollback) / float64(total); frac >= rollbackWarnFrac {
			add(Advice{
				Key: "rollback_ratio", Level: AdviceWarn, Current: fmt.Sprintf("%.0f%%", frac*100),
				Reason: fmt.Sprintf("%.0f%% of transactions roll back — application errors or serialization failures", frac*100),
				Target: AdviceTargetDiagnostic, DiagKey: "database_stats",
			})
		}
	}
}

// autoSLRUBuffers is the server's sizing of an SLRU whose *_buffers GUC is 0;
// 0 when shared_buffers is unknown.
func autoSLRUBuffers(sharedBuffersBytes int64) int64 {
	if sharedBuffersBytes <= 0 {
		return 0
	}
	return min(max(sharedBuffersBytes/(8<<10)/slruAutoDivisor, slruAutoMin), slruAutoMax)
}

// adviseReplication grades streaming replication from whichever side this
// server is on — on a primary the worst replica and a missing synchronous
// standby, on a standby the WAL receiver and recovery conflicts — and the slots.
func adviseReplication(info *MaintenanceInfo, add func(Advice)) {
	if info.InRecovery {
		if wr := info.WalReceiver; wr == nil {
			add(Advice{
				Key: "wal_receiver", Level: AdviceWarn, Current: "none",
				Reason: "standby without a WAL receiver — not streaming from a primary (log shipping, or the connection is down)",
			})
		} else {
			age := wr.LastMsgAge.Seconds()
			lvl := AdviceInfo
			switch {
			case age >= walReceiverStaleCritSecs:
				lvl = AdviceCrit
			case age >= walReceiverStaleWarnSecs || wr.Status != "streaming":
				lvl = AdviceWarn
			}
			if lvl >= AdviceWarn {
				add(Advice{
					Key: "wal_receiver", Level: lvl, Current: wr.Status,
					Reason: "last message from the primary " + shortSecs(age) + " ago — the standby is falling behind",
				})
			}
		}
		if v, ok := info.Settings["hot_standby_feedback"]; ok && v == "off" && info.Conflicts > 0 {
			add(Advice{
				Key: "hot_standby_feedback", Level: AdviceWarn, Setting: "hot_standby_feedback", Current: "off", Suggested: "on",
				Reason: compactCount(info.Conflicts) + " queries cancelled by recovery conflicts — feedback makes the primary keep the rows they need",
				Fix:    alterReload("hot_standby_feedback", "on"),
			})
		}
	} else {
		worst, worstRep := AdviceInfo, ReplicaStat{}
		for _, r := range info.Replicas {
			lag := r.ReplayLag.Seconds()
			lvl := AdviceInfo
			switch {
			case lag >= replLagCritSecs || r.ByteLag >= replByteLagCritBytes:
				lvl = AdviceCrit
			case lag >= replLagWarnSecs || r.State != "streaming":
				lvl = AdviceWarn
			}
			if lvl > worst {
				worst, worstRep = lvl, r
			}
		}
		if worst >= AdviceWarn {
			name := worstRep.AppName
			if name == "" {
				name = worstRep.ClientAddr
			}
			add(Advice{
				Key: "replication_lag", Level: worst, Current: name,
				Reason: fmt.Sprintf("%s: %s, replay lag %s, %s behind", name, worstRep.State,
					shortSecs(worstRep.ReplayLag.Seconds()), humanize.Bytes(worstRep.ByteLag)),
			})
		}
		if names := strings.TrimSpace(info.Settings["synchronous_standby_names"]); names != "" &&
			!slices.ContainsFunc(info.Replicas, func(r ReplicaStat) bool { return r.SyncState == "sync" || r.SyncState == "quorum" }) {
			add(Advice{
				Key: "synchronous_standby_names", Level: AdviceCrit, Setting: "synchronous_standby_names", Current: names,
				Reason: "no connected standby is synchronous — every commit waits until one is",
			})
		}
	}
	adviseSlots(info, add)
}

// adviseSlots reports the worst replication slot: lost or unreserved WAL,
// retention past the slot's budget, or a consumer that has been gone too long.
func adviseSlots(info *MaintenanceInfo, add func(Advice)) {
	var (
		worst     AdviceLevel
		worstSlot *ReplSlotStat
		why       string
		inactive  int
	)
	for i := range info.ReplSlots {
		s := &info.ReplSlots[i]
		lvl, reason := AdviceInfo, ""
		if !s.Active {
			inactive++
			lvl, reason = AdviceWarn, "inactive"
			if s.InactiveSecs > slotStaleInactiveSecs {
				lvl, reason = AdviceCrit, "no consumer for "+shortSecs(s.InactiveSecs)
			}
		}
		switch {
		case s.WALStatus == "lost" || s.WALStatus == "unreserved":
			lvl, reason = AdviceCrit, "wal_status "+s.WALStatus
		case s.SafeWALBytes > 0 && s.RetainedBytes > s.SafeWALBytes:
			lvl, reason = AdviceCrit, fmt.Sprintf("only %s of headroom left before max_slot_wal_keep_size invalidates it", humanize.Bytes(s.SafeWALBytes))
		case s.SafeWALBytes <= 0 && s.RetainedBytes > slotRetainedCapBytes:
			lvl, reason = AdviceCrit, "no max_slot_wal_keep_size cap"
		}
		if lvl > worst || (lvl == worst && worstSlot != nil && s.RetainedBytes > worstSlot.RetainedBytes) {
			worst, worstSlot, why = lvl, s, reason
		}
	}
	if worst < AdviceWarn {
		return
	}
	a := Advice{
		Key: "replication_slots", Level: worst, Current: worstSlot.Name,
		Reason: fmt.Sprintf("%s: %s, %s of WAL retained", worstSlot.Name, why, humanize.Bytes(worstSlot.RetainedBytes)),
		Target: AdviceTargetDiagnostic, DiagKey: "replication_slots",
	}
	others := inactive
	if !worstSlot.Active {
		others--
	}
	if others > 0 {
		a.Reason += fmt.Sprintf("; %d more inactive", others)
	}
	if !worstSlot.Active {
		a.Fix = fmt.Sprintf("SELECT pg_drop_replication_slot('%s');   -- only if its consumer is gone for good", worstSlot.Name)
	}
	add(a)
}

// adviseSchema turns the per-database catalog sweep into recommendations. It
// is silent while the sweep is still loading (nil) and per check when that
// check could not be evaluated.
func adviseSchema(h *SchemaHealth, add func(Advice)) {
	if h == nil {
		return
	}
	tail := inDB(h.DB)
	diag := func(a Advice, key string) Advice {
		a.Target, a.DiagKey, a.DB = AdviceTargetDiagnostic, key, h.DB
		return a
	}
	if c := h.Sequences; c.Err == nil && c.MaxPct >= seqWarnPct {
		lvl := AdviceWarn
		if c.MaxPct >= seqCritPct {
			lvl = AdviceCrit
		}
		add(diag(Advice{
			Key: "schema_sequences", Level: lvl, Current: fmt.Sprintf("%.1f%%", c.MaxPct),
			Reason: fmt.Sprintf("%s at %.1f%% of its range%s — inserts fail at 100%%: migrate the column to bigint", c.topNames(), c.MaxPct, tail),
		}, "sequences"))
	}
	if c := h.StaleStats; c.Err == nil && c.Rows > 0 {
		add(diag(Advice{
			Key: "schema_stale_stats", Level: AdviceWarn, Current: strconv.Itoa(c.Rows),
			Reason: fmt.Sprintf("%d table(s) with stale planner statistics%s: %s — ANALYZE them", c.Rows, tail, c.topNames()),
		}, "stale_statistics"))
	}
	if c := h.FKMissingIndex; c.Err == nil && c.Rows > 0 {
		add(diag(Advice{
			Key: "schema_fk_index", Level: AdviceWarn, Current: strconv.Itoa(c.Rows),
			Reason: fmt.Sprintf("%d foreign key(s) without a supporting index%s: %s — cascading deletes and joins scan the whole table", c.Rows, tail, c.topNames()),
		}, "fk_missing_index"))
	}
	if c := h.TableBloat; c.Err == nil && c.Rows > 0 {
		add(diag(Advice{
			Key: "schema_bloat_table", Level: AdviceWarn, Current: humanize.Bytes(c.Bytes),
			Reason: fmt.Sprintf("%d heavily bloated table(s), ~%s wasted%s: %s — VACUUM FULL / pg_repack, then find what pins the xmin horizon", c.Rows, humanize.Bytes(c.Bytes), tail, c.topNames()),
		}, "bloat_table"))
	}
	if c := h.IndexBloat; c.Err == nil && c.Rows > 0 {
		add(diag(Advice{
			Key: "schema_bloat_index", Level: AdviceWarn, Current: humanize.Bytes(c.Bytes),
			Reason: fmt.Sprintf("%d heavily bloated index(es), ~%s wasted%s: %s — REINDEX INDEX CONCURRENTLY", c.Rows, humanize.Bytes(c.Bytes), tail, c.topNames()),
		}, "bloat_index"))
	}
	if c := h.InvalidIndexes; c.Err == nil && c.Rows > 0 {
		add(diag(Advice{
			Key: "schema_index_invalid", Level: AdviceWarn, Current: strconv.Itoa(c.Rows),
			Reason: fmt.Sprintf("%d INVALID index(es)%s: %s — maintained on every write, used by no query; REINDEX or DROP", c.Rows, tail, c.topNames()),
		}, "index_invalid"))
	}
	if c := h.DuplicateIndexes; c.Err == nil && c.Rows > 0 {
		add(diag(Advice{
			Key: "schema_index_duplicate", Level: AdviceWarn, Current: humanize.Bytes(c.Bytes),
			Reason: fmt.Sprintf("%d duplicate index group(s), ~%s redundant%s: %s — DROP INDEX CONCURRENTLY the copies", c.Rows, humanize.Bytes(c.Bytes), tail, c.topNames()),
		}, "index_show_duplicate"))
	}
}

// alterReload / alterRestart are the copyable fix lines: a reload-level GUC
// takes effect with pg_reload_conf(), a postmaster-level one after a restart.
func alterReload(setting, value string) string {
	return fmt.Sprintf("ALTER SYSTEM SET %s = '%s'; SELECT pg_reload_conf();", setting, value)
}

func alterRestart(setting, value string) string {
	return fmt.Sprintf("ALTER SYSTEM SET %s = '%s';   -- restart required", setting, value)
}

// gucBytes renders a byte count in the largest unit that divides it evenly, as
// a value ALTER SYSTEM accepts ("12GB", "512MB", "64kB") — unlike
// humanize.Bytes, which prints decimals Postgres would reject.
func gucBytes(n int64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return fmt.Sprintf("%dGB", n>>30)
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%dMB", n>>20)
	default:
		return fmt.Sprintf("%dkB", max(n>>10, 1))
	}
}

// compactCount prints a counter the way the TUI does (1.2k, 3.4M, 5.6G).
func compactCount(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fG", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return strconv.FormatInt(n, 10)
}

// roundDuration prints a duration at the precision a human reads it:
// whole seconds under a minute, minutes under an hour, else hours+minutes.
func roundDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// shortSecs renders seconds at one coarse unit — "48s", "11m", "3h", "34d" —
// for the one-line reasons where "3h02m" would be false precision.
func shortSecs(secs float64) string {
	d := time.Duration(secs * float64(time.Second))
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%.0fd", d.Hours()/24)
	case d >= time.Hour:
		return fmt.Sprintf("%.0fh", d.Hours())
	case d >= time.Minute:
		return fmt.Sprintf("%.0fm", d.Minutes())
	}
	return fmt.Sprintf("%.0fs", d.Seconds())
}

// perDay renders a per-day count: whole numbers once it is at least one a
// day, one decimal below that so "0.4" does not round to a misleading 0.
func perDay(n float64) string {
	if n >= 1 {
		return fmt.Sprintf("%.0f", n)
	}
	return fmt.Sprintf("%.1f", n)
}

// Exported mirrors of the thresholds the renderer grades rows with, so a
// metric is coloured exactly where the advice fires.
const (
	IdleXactWarnSecs      = idleXactWarnSecs
	IdleXactCritSecs      = idleXactCritSecs
	CheckpointReqWarnFrac = checkpointReqWarnFrac
	CheckpointReqCritFrac = checkpointReqCritFrac
)
