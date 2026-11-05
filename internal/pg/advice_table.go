package pg

import (
	"fmt"
	"sort"
	"strings"
)

// Per-table advice: the same Advice shape MaintAdvice produces, scoped to one
// relation, so the maintenance panel under the parts list can be a renderer
// rather than a second opinion. Before this existed the panel carried its own
// inline literals (dead ≥ 40 %, freeze ≥ 85 % of freeze_max_age) and graded a
// routine anti-wraparound vacuum as an emergency — exactly the state the
// cluster view calls normal.
//
// Deliberately kept out of MaintAdvice's set: these findings belong to one
// table and must never reach the overview's recommendations panel, so they
// carry no Fix and no Target.
const (
	// tableMinRows is the floor under which nothing is graded. Every ratio
	// here is meaningful only at scale: a 300-row table sitting at 45 % dead
	// is one deleted row away from 0 %, and autovacuum's own threshold
	// (50 rows + 20 %) would have fired long before the figure meant anything.
	tableMinRows = 10_000

	// tableBehindFactor is the multiple of a table's own autovacuum threshold
	// at which a counter earns a row. *At* the threshold nothing is wrong —
	// that is the moment a worker is supposed to pick the table up, and
	// saying so would fire on every busy table in the cluster. Twice it means
	// a worker was going to come and has not.
	tableBehindFactor = 2.0
	// tableLateFactor is where "not keeping up" becomes "not happening":
	// several cycles of work owed with nothing collecting it.
	tableLateFactor = 4.0

	// tableDeadShareWarn / tableDeadShareCrit grade the dead rows as a share
	// of the table, which is the half of the story a threshold ratio cannot
	// tell: a table can sit under its trigger and still be mostly dead rows
	// if the trigger was raised by a reloption.
	tableDeadShareWarn = 0.20
	tableDeadShareCrit = 0.40
)

// TableAdvice grades one table's maintenance state. The result is ordered the
// way MaintAdvice's is (Crit, Warn, Info, then by key) and is empty for a
// healthy table, which is what lets the panel fold everything into one line.
//
// Keys are the stable ids the renderer looks up: "table_dead_rows",
// "table_analyze_due", "table_never_analyzed", "table_autovacuum_off",
// "table_freeze".
func TableAdvice(st *TableMaintStats) AdviceSet {
	if st == nil {
		return nil
	}
	var out AdviceSet
	add := func(a Advice) { out = append(out, a) }

	// A table nothing will maintain is worth saying at any size; the ratios
	// below are not.
	adviseTableAutovacuum(st, add)
	if st.NLive+st.NDead >= tableMinRows || st.RelTuples >= tableMinRows {
		adviseTableDead(st, add)
		adviseTableAnalyze(st, add)
	}
	adviseTableFreeze(st, add)

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Level != out[j].Level {
			return out[i].Level > out[j].Level
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// adviseTableAutovacuum reports a table autovacuum will never touch. Off by
// reloption is the critical one: a deliberate per-table exemption outlives
// whoever set it, and nothing else on these screens says so. A cluster-wide
// autovacuum=off is already the overview's finding, so here it is only a
// warning — the table is not the problem.
func adviseTableAutovacuum(st *TableMaintStats, add func(Advice)) {
	if st.AutovacuumEnabled() {
		return
	}
	if !optBool(ParseRelOptions(st.RelOptions), "autovacuum_enabled", true) {
		add(Advice{
			Key: "table_autovacuum_off", Level: AdviceCrit,
			Setting: "autovacuum_enabled", Current: "off (reloption)",
			Reason: "autovacuum_enabled=false on this table — nothing vacuums or analyzes it until that is reverted",
		})
		return
	}
	add(Advice{
		Key: "table_autovacuum_off", Level: AdviceWarn,
		Setting: "autovacuum", Current: "off (cluster)",
		Reason: "autovacuum is off cluster-wide — this table is maintained by hand",
	})
}

// adviseTableDead grades dead tuples two ways and keeps the worse: against the
// table's own vacuum threshold (is autovacuum keeping up) and as a share of
// the table (is the state itself bad). The two disagree exactly when a
// reloption has moved the threshold, which is when the second one earns its
// keep.
func adviseTableDead(st *TableMaintStats, add func(Advice)) {
	if st.NDead <= 0 {
		return
	}
	level := AdviceInfo
	var factor float64
	if trig, ok := st.VacuumTriggerAt(); ok && trig > 0 {
		factor = float64(st.NDead) / float64(trig)
		switch {
		case factor >= tableLateFactor:
			level = AdviceCrit
		case factor >= tableBehindFactor:
			level = AdviceWarn
		}
	}
	share := st.DeadShare()
	switch {
	case share >= tableDeadShareCrit:
		level = max(level, AdviceCrit)
	case share >= tableDeadShareWarn:
		level = max(level, AdviceWarn)
	}
	if level == AdviceInfo {
		return
	}
	// The percentage cell next to this note already says the share, so the
	// share-only wording spends its words on the consequence instead.
	reason := "dead rows are most of the table, yet it is still under its own autovacuum threshold — only a VACUUM by hand will reclaim them"
	if factor == 0 {
		reason = "dead rows are most of the table; without statistics its autovacuum threshold cannot even be computed"
	}
	if factor >= tableBehindFactor {
		reason = fmt.Sprintf("%s dead rows is %.1f× this table's autovacuum threshold — a worker was due and has not come",
			compactCount(st.NDead), factor)
		if share >= tableDeadShareWarn {
			reason += fmt.Sprintf(" (%.0f%% of the table)", share*100)
		}
	}
	add(Advice{Key: "table_dead_rows", Level: level,
		Current: compactCount(st.NDead), Reason: reason})
}

// adviseTableAnalyze covers the two ways statistics go wrong: never gathered
// at all, and gathered so long ago that the planner is working from a
// different table.
func adviseTableAnalyze(st *TableMaintStats, add func(Advice)) {
	if st.RelTuples < 0 || (st.LastAnalyze == nil && st.LastAutoanalyze == nil) {
		// Without statistics reltuples is -1, every trigger above is
		// uncomputable, and every estimate on these screens — column sizes,
		// the tables list's bloat — is a guess.
		add(Advice{Key: "table_never_analyzed", Level: AdviceWarn,
			Current: "never analyzed",
			Reason:  "never analyzed — every row estimate and bloat figure on these screens is a guess"})
		return
	}
	trig, ok := st.AnalyzeTriggerAt()
	if !ok || trig <= 0 || st.NModSinceAnalyze <= 0 {
		return
	}
	factor := float64(st.NModSinceAnalyze) / float64(trig)
	level := AdviceInfo
	switch {
	case factor >= tableLateFactor:
		level = AdviceCrit
	case factor >= tableBehindFactor:
		level = AdviceWarn
	}
	if level == AdviceInfo {
		return
	}
	add(Advice{Key: "table_analyze_due", Level: level,
		Current: compactCount(st.NModSinceAnalyze),
		Reason: fmt.Sprintf("%s rows changed since the last analyze, %.1f× the threshold — the planner is costing a table that no longer exists",
			compactCount(st.NModSinceAnalyze), factor)})
}

// adviseTableFreeze grades relfrozenxid through the cluster's own freezeLevel,
// so a table can never read as an emergency on a screen the overview calls
// routine. A partitioned parent has no storage and no meaningful relfrozenxid.
func adviseTableFreeze(st *TableMaintStats, add func(Advice)) {
	if st.RelKind == "p" {
		return
	}
	g := freezeLevel(st.FrozenXIDAge, st.FreezeMaxAge, st.FailsafeAge)
	if !g.worthSaying() {
		return
	}
	reason := fmt.Sprintf("relfrozenxid age is %.0f%% of autovacuum_freeze_max_age — a routine anti-wraparound vacuum is due",
		g.Factor*100)
	switch g.Level {
	case AdviceCrit:
		reason = fmt.Sprintf("relfrozenxid age is %.0f%% of the 2^31 wraparound limit — VACUUM runs in failsafe mode and writes stop at the limit",
			g.PctOf31)
	case AdviceWarn:
		reason = fmt.Sprintf("relfrozenxid age is %.1f× autovacuum_freeze_max_age — a forced cycle passed without advancing it",
			g.Factor)
	}
	add(Advice{Key: "table_freeze", Level: g.Level,
		Current: compactCount(st.FrozenXIDAge), Reason: reason})
}

// RelOptionsNote renders reloptions for display, dropping the one the panel
// already grades on its own row so the line carries only what nothing else
// says.
func RelOptionsNote(opts []string) string {
	var keep []string
	for _, o := range opts {
		if strings.HasPrefix(o, "autovacuum_enabled=") {
			continue // already its own finding
		}
		keep = append(keep, o)
	}
	return strings.Join(keep, " · ")
}
