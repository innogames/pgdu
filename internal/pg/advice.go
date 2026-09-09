package pg

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"pgdu/internal/humanize"
)

// AdviceLevel grades one system-overview recommendation. It is deliberately
// not the triage Severity: the overview also carries purely informational
// notes (a shared_buffers share outside the usual band), and the triage
// renderer assumes its enum has exactly three graded values.
type AdviceLevel int

const (
	AdviceInfo AdviceLevel = iota
	AdviceWarn
	AdviceCrit
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

// Overview thresholds. Like the triage constants they are named so the
// opinions live in one place; the checkpoint, idle-in-xact and extension
// capacity figures are shared with triage.go so the two screens agree.
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
	WALBytesPerMin    float64 // from the LSN delta, so it includes replayed WAL on a standby
	TempBytesPerMin   float64
	CheckpointsPerMin float64
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
	if prev.Checkpointer.HasData && cur.Checkpointer.HasData {
		r.CheckpointsPerMin = per(prev.Checkpointer.Timed+prev.Checkpointer.Requested,
			cur.Checkpointer.Timed+cur.Checkpointer.Requested)
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

// MaintAdvice evaluates every overview rule against one snapshot. Host-relative
// rules need info.Host (only set when pgdu runs on the database host) and stay
// silent otherwise; every other rule degrades the same way on missing data.
func MaintAdvice(info *MaintenanceInfo) AdviceSet {
	if info == nil {
		return nil
	}
	var out AdviceSet
	add := func(a Advice) { out = append(out, a) }

	adviseHostMemory(info, add)
	adviseBufferCache(info, add)
	adviseCheckpoints(info, add)
	adviseAutovacuum(info, add)
	adviseSessions(info, add)
	adviseObservability(info, add)

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
		a := Advice{
			Key: "huge_pages", Level: AdviceCrit, Setting: "huge_pages", Current: hp,
			Reason: "none allocated on the host — silently running on 4 kB pages",
		}
		if n := info.Tuning.ShmemHugePages; n > 0 {
			a.Reason = fmt.Sprintf("none allocated — set vm.nr_hugepages ≥ %d", n)
			a.Suggested = fmt.Sprintf("vm.nr_hugepages=%d", n)
			a.Fix = fmt.Sprintf("sysctl -w vm.nr_hugepages=%d   # host-side; then restart postgres", n)
		}
		add(a)
	}

	if used := host.SwapUsed(); used > 0 {
		add(Advice{
			Key: "swap", Level: AdviceWarn, Current: humanize.Bytes(used),
			Reason: "swap in use — Postgres memory may be paged out",
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

	if ecs, ok := info.SettingBytes["effective_cache_size"]; ok && sbOK && host.Cached > 0 {
		actual := sb + host.Cached
		if float64(ecs) < ecsMinFracOfCache*float64(actual) {
			sugg := max(actual/(1<<30), 1) << 30 // whole GiB, rounded down
			add(Advice{
				Key: "effective_cache_size", Level: AdviceWarn, Setting: "effective_cache_size",
				Current: info.Settings["effective_cache_size"], Suggested: gucBytes(sugg),
				Reason: fmt.Sprintf("planner assumes %s of cache, host has ~%s (shared_buffers + page cache)",
					humanize.Bytes(ecs), humanize.Bytes(actual)),
				Fix: alterReload("effective_cache_size", gucBytes(sugg)),
			})
		}
	}

	if wm, ok := info.SettingBytes["work_mem"]; ok && wm > 0 && info.MaxConns > 0 {
		theoretical := wm * int64(info.MaxConns)
		if float64(theoretical) > workMemExposureWarnFrac*float64(host.Total) {
			sugg := max(host.Total/4/int64(info.MaxConns)/(1<<20), 1) << 20 // whole MiB, rounded down
			add(Advice{
				Key: "work_mem", Level: AdviceWarn, Setting: "work_mem",
				Current: info.Settings["work_mem"], Suggested: gucBytes(sugg),
				Reason: fmt.Sprintf("work_mem × max_connections = %s (%.0f%% of RAM)",
					humanize.Bytes(theoretical), 100*float64(theoretical)/float64(host.Total)),
				Fix: alterReload("work_mem", gucBytes(sugg)),
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
	if frac, ok := info.IOSplit.ClientWriteFrac(); ok && frac > backendWriteShareWarnFrac &&
		info.IOSplit.CheckpointerWrites+info.IOSplit.BgwriterWrites+info.IOSplit.ClientWrites >= writeSplitMinWrites {
		a := Advice{
			Key: "bgwriter_lru_maxpages", Level: AdviceWarn, Setting: "bgwriter_lru_maxpages",
			Current: info.Settings["bgwriter_lru_maxpages"],
			Reason:  fmt.Sprintf("%.0f%% of dirty-page writes done by backends themselves", frac*100),
		}
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
		add(Advice{
			Key: "wal_fpi", Level: AdviceInfo,
			Reason: fmt.Sprintf("%.0f%% of WAL records are full-page images — frequent checkpoints inflate WAL", frac*100),
		})
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

// Exported mirrors of the triage thresholds the overview grades with, so the
// renderer colours a metric exactly where the health check would flag it.
const (
	IdleXactWarnSecs      = idleXactWarnSecs
	IdleXactCritSecs      = idleXactCritSecs
	CheckpointReqWarnFrac = checkpointReqWarnFrac
	CheckpointReqCritFrac = checkpointReqCritFrac
)
