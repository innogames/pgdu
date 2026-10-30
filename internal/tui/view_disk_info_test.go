package tui

import (
	"strings"
	"testing"
)

// The disk chain was the last tool without a ? reference. Every level that
// claims one must render a non-empty body with the dismissal header, or the
// key opens a blank panel.
func TestDiskInfoOverlays(t *testing.T) {
	m := &Model{}
	for _, l := range diskInfoLevels {
		s := &screen{level: l, tool: toolDisk}
		if !m.hasInfoOverlay(s) {
			t.Errorf("%v: hasInfoOverlay is false, so ? never reaches renderDiskInfo", levelLabel(l))
			continue
		}
		body := m.renderDiskInfo(l, 40)
		if !strings.Contains(body, "to dismiss") {
			t.Errorf("%v: overlay has no header", levelLabel(l))
		}
		if len(strings.TrimSpace(body)) < 200 {
			t.Errorf("%v: overlay body is empty or near-empty", levelLabel(l))
		}
		if got := strings.Count(body, "\n"); got < 40 {
			t.Errorf("%v: overlay is %d lines, want padding to the full 40", levelLabel(l), got)
		}
	}
}

// Each maintenance op must name a distinct statement and label — they share
// one confirm slot and one output pane, so a collision would run or announce
// the wrong thing.
func TestMaintOps(t *testing.T) {
	seenStmt, seenLabel := map[string]bool{}, map[string]bool{}
	for _, op := range []maintOp{maintVacuum, maintFreeze, maintAnalyze} {
		stmt := op.statement("public.t")
		if stmt == "" || seenStmt[stmt] {
			t.Errorf("%s: statement %q is empty or a duplicate", op, stmt)
		}
		if !strings.HasSuffix(stmt, "public.t") {
			t.Errorf("%s: statement does not end in the relation: %q", op, stmt)
		}
		seenStmt[stmt] = true
		if l := op.label(); l == "" || seenLabel[l] {
			t.Errorf("%s: label %q is empty or a duplicate", op, l)
		} else {
			seenLabel[l] = true
		}
	}
	// FREEZE must not silently degrade to a plain vacuum.
	if !strings.Contains(maintFreeze.statement("t"), "FREEZE") {
		t.Error("the freeze op does not pass FREEZE")
	}
	// An unknown op falls back to the ordinary vacuum rather than running
	// something unexpected.
	if maintOp("nonsense").statement("t") != maintVacuum.statement("t") {
		t.Error("an unknown op must fall back to the plain vacuum statement")
	}
}
