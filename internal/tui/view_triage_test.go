package tui

import (
	"strings"
	"testing"

	"pgdu/internal/pg"
)

func TestTriageItemsCollapsesOK(t *testing.T) {
	results := []pg.TriageResult{
		{Check: "invalid indexes", Severity: pg.SevCrit, Detail: "1 index invalid", DiagKey: "index_invalid"},
		{Check: "blocked backends", Severity: pg.SevWarn, Detail: "1 waiting", Target: pg.TriageTargetLockTree},
		{Check: "cache hit ratio", Severity: pg.SevOK, Detail: "99.3%"},
		{Check: "wraparound", Severity: pg.SevOK, Detail: "41%"},
	}
	items := triageItems(results, nil, false)
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 (2 findings + 1 ok summary)", len(items))
	}
	if items[0].name != "invalid indexes" || items[1].name != "blocked backends" {
		t.Errorf("finding rows out of order: %q, %q", items[0].name, items[1].name)
	}
	if _, ok := items[0].data.(pg.TriageResult); !ok {
		t.Errorf("finding row must carry its TriageResult for the Enter drill")
	}
	sum := items[2]
	if _, ok := sum.data.(triageOKSummary); !ok {
		t.Errorf("ok summary row must carry the triageOKSummary marker (Enter toggles the fold)")
	}
	if !strings.Contains(sum.name, "2 check(s) ok") {
		t.Errorf("summary name = %q, want a 2-checks-ok count", sum.name)
	}
	if !strings.Contains(sum.detail, "cache hit ratio") || !strings.Contains(sum.detail, "wraparound") {
		t.Errorf("summary detail should list the ok checks, got %q", sum.detail)
	}
}

func TestTriageItemsAllOK(t *testing.T) {
	items := triageItems([]pg.TriageResult{
		{Check: "a", Severity: pg.SevOK},
		{Check: "b", Severity: pg.SevOK},
	}, nil, false)
	if len(items) != 1 {
		t.Fatalf("got %d items, want just the summary row", len(items))
	}
}

func TestTriageItemsShowOK(t *testing.T) {
	results := []pg.TriageResult{
		{Check: "invalid indexes", Severity: pg.SevCrit, DiagKey: "index_invalid"},
		{Check: "cache hit ratio", Severity: pg.SevOK, Detail: "99.3%", DiagKey: "database_stats"},
		{Check: "wraparound", Severity: pg.SevOK, Detail: "41%"},
	}
	items := triageItems(results, []string{"deadlocks"}, true)
	if len(items) != 5 {
		t.Fatalf("got %d items, want 5 (1 finding + summary + 2 ok + 1 pending)", len(items))
	}
	if _, ok := items[1].data.(triageOKSummary); !ok || items[1].detail != "" {
		t.Errorf("summary row must precede the unfolded checks without the name list, got %+v", items[1])
	}
	for i, want := range []string{"cache hit ratio", "wraparound"} {
		it := items[2+i]
		r, ok := it.data.(pg.TriageResult)
		if !ok || r.Check != want || it.name != want || it.detail != r.Detail {
			t.Errorf("unfolded row %d = %+v, want ok check %q carrying its TriageResult", i, it, want)
		}
	}
	if _, ok := items[4].data.(triagePending); !ok {
		t.Errorf("pending rows must stay last, got %+v", items[4])
	}
}

func TestToggleTriageOKKeepsCursor(t *testing.T) {
	m := &Model{width: 120}
	s := &screen{level: levelTriage, loaded: true, triage: triageState{results: []pg.TriageResult{
		{Check: "deadlocks", Severity: pg.SevWarn},
		{Check: "a", Severity: pg.SevOK},
		{Check: "b", Severity: pg.SevOK},
	}}}
	m.rebuildTriageItems(s)
	s.cursor = 1 // the summary row
	m.toggleTriageOK(s)
	if !s.triage.showOK || len(s.items) != 4 {
		t.Fatalf("toggle did not unfold: showOK=%v items=%d", s.triage.showOK, len(s.items))
	}
	if s.cursor != 1 {
		t.Errorf("cursor moved to %d, want to stay on the summary row", s.cursor)
	}
	s.cursor = 3 // "b"
	m.toggleTriageOK(s)
	if s.triage.showOK || len(s.items) != 2 || s.cursor != 1 {
		t.Errorf("fold back: showOK=%v items=%d cursor=%d, want collapsed with cursor clamped to the summary row",
			s.triage.showOK, len(s.items), s.cursor)
	}
}

func TestRenderTriageList(t *testing.T) {
	m := &Model{width: 120}
	s := &screen{
		level:  levelTriage,
		loaded: true,
		triage: triageState{results: []pg.TriageResult{
			{Check: "idle-in-xact", Severity: pg.SevCrit, Detail: "pid 8123 idle 11m", DiagKey: "idle_in_xact_holders"},
			{Check: "blocked backends", Severity: pg.SevWarn, Detail: "2 waiting", Target: pg.TriageTargetLockTree},
			{Check: "cache hit ratio", Severity: pg.SevOK, Detail: "99.3%"},
		}}}
	s.items = triageItems(s.triage.results, nil, false)
	out := stripANSI(m.renderTriageList(s, 20))

	for _, want := range []string{
		"1 critical", "1 warning(s)",
		"✗ idle-in-xact", "▲ blocked backends",
		"↵ lock tree", "↵ Idle-in-transaction lock holders",
		"1 check(s) ok", "cache hit ratio", "↵ expand",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered triage list missing %q:\n%s", want, out)
		}
	}
	s.triage.showOK = true
	s.items = triageItems(s.triage.results, nil, true)
	out = stripANSI(m.renderTriageList(s, 20))
	for _, want := range []string{"ok: shown", "↵ collapse", "● cache hit ratio", "99.3%"} {
		if !strings.Contains(out, want) {
			t.Errorf("unfolded triage list missing %q:\n%s", want, out)
		}
	}
}

func TestTriageTargetLabel(t *testing.T) {
	if got := triageTargetLabel(pg.TriageResult{Target: pg.TriageTargetLockTree}); got != "lock tree" {
		t.Errorf("lock tree label = %q", got)
	}
	if got := triageTargetLabel(pg.TriageResult{Target: pg.TriageTargetMaintenance}); got != "system overview" {
		t.Errorf("maintenance label = %q", got)
	}
	if got := triageTargetLabel(pg.TriageResult{Target: pg.TriageTargetActivity}); got != "activity" {
		t.Errorf("activity label = %q", got)
	}
	if got := triageTargetLabel(pg.TriageResult{DiagKey: "nope"}); got != "diagnostic" {
		t.Errorf("unknown key fallback = %q", got)
	}
}
