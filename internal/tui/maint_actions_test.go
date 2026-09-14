package tui

import (
	"strings"
	"testing"

	"pgdu/internal/pg"
)

// adviceScreen is an overview whose recommendations are injected directly, so
// the action-row tests do not depend on the rule table.
func adviceScreen(advice ...pg.Advice) *screen {
	s := &screen{level: levelMaintenance, tool: toolMaintenance, db: "postgres", loaded: true}
	s.maintenance.info = overviewInfo()
	s.maintenance.advice = pg.AdviceSet(advice)
	return s
}

func TestMaintActionRows(t *testing.T) {
	s := adviceScreen(
		pg.Advice{Key: "lock_waits", Level: pg.AdviceCrit, Target: pg.AdviceTargetLockTree},
		pg.Advice{Key: "shared_buffers", Level: pg.AdviceInfo}, // inline only: not an action row
		pg.Advice{Key: "log_checkpoints", Level: pg.AdviceInfo, Setting: "log_checkpoints", Fix: "x", Target: pg.AdviceTargetSettings},
	)
	rows := s.maintenance.actionRows()
	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r.key()
	}
	if want := "lock_waits,log_checkpoints,statements,qualstats,tablestats,tablestats-all"; strings.Join(got, ",") != want {
		t.Errorf("action rows = %v, want %s", got, want)
	}
	for i, want := range []struct {
		label string
		ok    bool
	}{{"lock tree", true}, {"settings", true}, {"reset stats", true}, {"reset stats", true}, {"reset stats", true}, {"reset stats", true}} {
		if label, ok := rows[i].enterLabel(); label != want.label || ok != want.ok {
			t.Errorf("row %d enterLabel = %q,%v, want %q,%v", i, label, ok, want.label, want.ok)
		}
	}
}

func TestRefreshAdviceKeepsCursorByKey(t *testing.T) {
	s := overviewScreen(overviewInfo())
	info := s.maintenance.info
	info.Settings["track_io_timing"] = "off"
	info.Settings["log_checkpoints"] = "off"
	s.maintenance.refreshAdvice()
	rows := s.maintenance.actionRows()
	if len(rows) != 6 {
		t.Fatalf("expected 2 recommendations + 4 reset rows, got %d", len(rows))
	}
	// Cursor on log_checkpoints (the second recommendation); a new, worse
	// finding lands above it and the cursor stays on log_checkpoints.
	s.maintenance.setCursor(1, rows)
	if !s.maintenance.follow || s.maintenance.cursorKey != "log_checkpoints" {
		t.Fatalf("setCursor: follow=%v key=%q", s.maintenance.follow, s.maintenance.cursorKey)
	}
	info.Host.SwapFree = 6 << 30
	s.maintenance.refreshAdvice()
	rows = s.maintenance.actionRows()
	if rows[s.maintenance.cursor].key() != "log_checkpoints" || s.maintenance.cursor != 2 {
		t.Errorf("cursor after refresh = %d (%s), want 2 (log_checkpoints)", s.maintenance.cursor, rows[s.maintenance.cursor].key())
	}
	// The row under the cursor vanishes: same index, re-keyed to whatever
	// moved up into it.
	info.Settings["log_checkpoints"] = "on"
	s.maintenance.refreshAdvice()
	rows = s.maintenance.actionRows()
	if s.maintenance.cursor != 2 || s.maintenance.cursorKey != rows[2].key() || rows[2].key() != "statements" {
		t.Errorf("cursor after its row vanished = %d/%q, want 2/statements", s.maintenance.cursor, s.maintenance.cursorKey)
	}
	// And a cursor past the end of a shrunken list is clamped.
	s.maintenance.cursor, s.maintenance.cursorKey = 99, ""
	s.maintenance.refreshAdvice()
	if s.maintenance.cursor != len(rows)-1 {
		t.Errorf("cursor past the end = %d, want %d", s.maintenance.cursor, len(rows)-1)
	}
}

func TestMaintEnterDispatch(t *testing.T) {
	s := adviceScreen(
		pg.Advice{Key: "lock_waits", Level: pg.AdviceCrit, Target: pg.AdviceTargetLockTree},
		pg.Advice{Key: "schema_bloat_index", Level: pg.AdviceWarn, Target: pg.AdviceTargetDiagnostic, DiagKey: "bloat_index", DB: "shop"},
		pg.Advice{Key: "wal_buffers", Level: pg.AdviceWarn, Setting: "wal_buffers", Target: pg.AdviceTargetSettings},
		pg.Advice{Key: "swap", Level: pg.AdviceWarn},
	)
	m := newTestModel(s)

	s.maintenance.cursor = 5
	m.handleMaintenanceEnter(s)
	if s.maintenance.pendingReset != "qualstats" || len(m.stack) != 2 {
		t.Errorf("reset row must arm the confirm, got pending=%q stack=%d", s.maintenance.pendingReset, len(m.stack))
	}
	s.maintenance.pendingReset = ""

	s.maintenance.cursor = 0
	m.handleMaintenanceEnter(s)
	if top := m.top(); top.level != levelLockTree || top.tool != toolActivity || top.db != "postgres" {
		t.Errorf("lock-tree row pushed %+v", top)
	}
	m.stack = m.stack[:2]

	s.maintenance.cursor = 1
	m.handleMaintenanceEnter(s)
	if top := m.top(); top.level != levelDiagnosticResult || top.diag == nil || top.diag.Key != "bloat_index" || top.db != "shop" {
		t.Errorf("diagnostic row pushed %+v", top)
	}
	m.stack = m.stack[:2]

	s.maintenance.cursor = 2
	m.handleMaintenanceEnter(s)
	if top := m.top(); top.level != levelSettings || top.filter != "wal_buffers" {
		t.Errorf("settings row pushed %+v (filter %q)", top, top.filter)
	}
	m.stack = m.stack[:2]

	s.maintenance.cursor = 3
	m.handleMaintenanceEnter(s)
	if len(m.stack) != 2 || s.maintenance.pendingReset != "" {
		t.Errorf("a recommendation without a target must do nothing, stack=%d", len(m.stack))
	}
	if label, ok := enterLabel(s); ok || label != "" {
		t.Errorf("enterLabel on a target-less row = %q,%v, want disabled", label, ok)
	}
}

// The recommendation rows carry the cursor and the drill mark, the panel
// opens the page, and the stats-reset rows close it.
func TestRenderMaintenanceRecommendationCursor(t *testing.T) {
	s := adviceScreen(
		pg.Advice{Key: "lock_waits", Level: pg.AdviceCrit, Current: "3", Reason: "3 backend(s) waiting on locks", Target: pg.AdviceTargetLockTree},
		pg.Advice{Key: "swap", Level: pg.AdviceWarn, Current: "2.00 GB", Reason: "swap in use"},
	)
	s.maintenance.cursor = 0
	m := &Model{width: 200}
	out := stripANSI(m.renderMaintenance(s, 200))
	rec := strings.Index(out, " recommendations ")
	srv := strings.Index(out, " server ")
	stats := strings.Index(out, " statistics ")
	if rec < 0 || srv < 0 || rec > srv {
		t.Fatalf("recommendations must precede the sections\n%s", out)
	}
	if stats < 0 || stats < strings.Index(out, " schema health") {
		t.Fatalf("the statistics block must close the page\n%s", out)
	}
	if !strings.Contains(out, "▶ ↵ ! lock_waits 3") {
		t.Errorf("cursor row must carry the marker and the drill glyph\n%s", out)
	}
	if !strings.Contains(out, "\n    ~ swap 2.00 GB") {
		t.Errorf("a row without a target has no drill glyph\n%s", out)
	}
	if strings.Count(out, "▶") != 1 {
		t.Errorf("exactly one cursor marker expected\n%s", out)
	}
	// The cursor moves on to the first reset row, below every section.
	s.maintenance.cursor = 2
	out = stripANSI(m.renderMaintenance(s, 300))
	if i := strings.Index(out, "▶ pg_stat_statements"); i < 0 || i < stats {
		t.Errorf("cursor on the first reset row must sit in the statistics block\n%s", out)
	}
}

// A finding's fix line is shown under the cursor row only, so the panel stays
// one line per finding until a row is picked.
func TestRenderMaintenanceFixLineFollowsCursor(t *testing.T) {
	s := adviceScreen(
		pg.Advice{Key: "wal_buffers", Level: pg.AdviceWarn, Setting: "wal_buffers", Current: "64MB", Suggested: "128MB",
			Reason: "stalls", Fix: "ALTER SYSTEM SET wal_buffers = '128MB';", Target: pg.AdviceTargetSettings},
		pg.Advice{Key: "max_wal_size", Level: pg.AdviceWarn, Setting: "max_wal_size", Current: "1GB", Suggested: "4GB",
			Reason: "wal-driven", Fix: "ALTER SYSTEM SET max_wal_size = '4GB';", Target: pg.AdviceTargetSettings},
	)
	m := &Model{width: 200}
	s.maintenance.cursor = 0
	out := stripANSI(m.renderMaintenance(s, 200))
	if !strings.Contains(out, "ALTER SYSTEM SET wal_buffers") || strings.Contains(out, "ALTER SYSTEM SET max_wal_size") {
		t.Errorf("only the cursor row shows its fix\n%s", out)
	}
	s.maintenance.cursor = 1
	out = stripANSI(m.renderMaintenance(s, 200))
	if strings.Contains(out, "ALTER SYSTEM SET wal_buffers") || !strings.Contains(out, "ALTER SYSTEM SET max_wal_size") {
		t.Errorf("moving the cursor moves the fix line\n%s", out)
	}
}

// ↑↓ keep the cursor row in view on a short terminal; the marker must be
// inside the rendered window after a follow.
func TestRenderMaintenanceFollowsCursor(t *testing.T) {
	advice := make([]pg.Advice, 0, 12)
	for i := range 12 {
		advice = append(advice, pg.Advice{Key: "k" + string(rune('a'+i)), Level: pg.AdviceWarn, Reason: "r",
			Fix: "ALTER SYSTEM SET x = 'y';"})
	}
	s := adviceScreen(advice...)
	rows := s.maintenance.actionRows()
	s.maintenance.setCursor(len(advice)-1, rows) // the last recommendation
	m := &Model{width: 200}
	out := stripANSI(m.renderMaintenance(s, 10))
	if !strings.Contains(out, "▶") || s.offset == 0 || s.maintenance.follow {
		t.Errorf("follow must scroll the cursor row into a 10-line window (offset %d, follow %v)\n%s", s.offset, s.maintenance.follow, out)
	}
	// The fix line under the cursor row is in view too.
	if !strings.Contains(out, "ALTER SYSTEM SET x") {
		t.Errorf("the cursor row's fix line must be in view\n%s", out)
	}
	// Without follow, an explicit scroll is left alone.
	s.offset = 0
	out = stripANSI(m.renderMaintenance(s, 10))
	if strings.Contains(out, "▶") || s.offset != 0 {
		t.Errorf("no follow: window must stay where it was (offset %d)", s.offset)
	}
	if s.maintenance.cursorLine <= 0 {
		t.Errorf("cursorLine must be recorded, got %d", s.maintenance.cursorLine)
	}
}

func TestRenderMaintenanceSchemaHealth(t *testing.T) {
	s := overviewScreen(overviewInfo())
	m := &Model{width: 200}
	s.maintenance.schema = &pg.SchemaHealth{
		DB:             "postgres",
		StaleStats:     pg.SchemaCheck{Rows: 2, Top: []string{"public.a", "public.b"}},
		TableBloat:     pg.SchemaCheck{Rows: 1, Bytes: 3 << 30, Top: []string{"public.events"}},
		InvalidIndexes: pg.SchemaCheck{Err: errTest("permission denied")},
	}
	s.maintenance.refreshAdvice()
	out := stripANSI(m.renderMaintenance(s, 300))
	if !strings.Contains(squashSpaces(out), "schema health (postgres) ~ ✓ sequences · fk without index · index bloat · duplicate indexes") {
		t.Errorf("the clean checks must fold into the title\n%s", out)
	}
	for _, want := range []string{
		// The findings and the error keep their rows.
		"stale statistics", "2 tables", "public.a, public.b",
		"table bloat", "1 table · ~3.00 GB wasted",
		"invalid indexes", "could not evaluate", "permission denied",
		"~ schema_bloat_table 3.00 GB", "~ schema_stale_stats 2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("schema section lacks %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "loading…") || strings.Contains(out, "refreshing") || strings.Contains(out, "none past 30%") {
		t.Errorf("landed sweep must not show a loading state or a folded row\n%s", out)
	}
	m.maintVerbose = true
	if out := stripANSI(m.renderMaintenance(s, 300)); !strings.Contains(out, ovRow("sequences", "none past 30% of their range")) {
		t.Errorf("verbose shows the clean checks as rows\n%s", out)
	}
	m.maintVerbose = false
	s.maintenance.schemaLoading = true
	out = stripANSI(m.renderMaintenance(s, 300))
	if !strings.Contains(out, "refreshing…") || !strings.Contains(out, "2 tables") {
		t.Errorf("a re-sweep keeps the old result and says refreshing\n%s", out)
	}
}

// The overview's own re-sample must never drop the sweep, and a sweep for
// another database is ignored.
func TestMaintLoadsKeepSchema(t *testing.T) {
	s := overviewScreen(overviewInfo())
	m := newTestModel(s)
	health := &pg.SchemaHealth{DB: "postgres", InvalidIndexes: pg.SchemaCheck{Rows: 1, Top: []string{"public.x"}}}
	m.onMaintSchemaLoaded(maintSchemaLoadedMsg{db: "postgres", health: health})
	if s.maintenance.schema != health || s.maintenance.advice.Find("schema_index_invalid") == nil {
		t.Fatalf("sweep did not land: schema=%v advice=%v", s.maintenance.schema, s.maintenance.advice)
	}
	m.onMaintLoaded(maintLoadedMsg{db: "postgres", info: overviewInfo()})
	if s.maintenance.schema != health || s.maintenance.advice.Find("schema_index_invalid") == nil {
		t.Error("re-sampling the snapshot must keep the sweep and its advice")
	}
	m.onMaintSchemaLoaded(maintSchemaLoadedMsg{db: "other", health: &pg.SchemaHealth{DB: "other"}})
	if s.maintenance.schema != health {
		t.Error("a sweep for another database must be ignored")
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
