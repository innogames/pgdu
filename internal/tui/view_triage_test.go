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
	items := triageItems(results)
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
	if sum.data != nil {
		t.Errorf("ok summary row must carry no TriageResult (inert on Enter)")
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
	})
	if len(items) != 1 {
		t.Fatalf("got %d items, want just the summary row", len(items))
	}
}

func TestRenderTriageList(t *testing.T) {
	m := &Model{width: 120}
	s := &screen{
		level:  levelTriage,
		loaded: true,
		triageResults: []pg.TriageResult{
			{Check: "idle-in-xact", Severity: pg.SevCrit, Detail: "pid 8123 idle 11m", DiagKey: "idle_in_xact_holders"},
			{Check: "blocked backends", Severity: pg.SevWarn, Detail: "2 waiting", Target: pg.TriageTargetLockTree},
			{Check: "cache hit ratio", Severity: pg.SevOK, Detail: "99.3%"},
		},
	}
	s.items = triageItems(s.triageResults)
	out := stripANSI(m.renderTriageList(s, 20))

	for _, want := range []string{
		"1 critical", "1 warning(s)",
		"✗ idle-in-xact", "▲ blocked backends",
		"↵ lock tree", "↵ Idle-in-transaction lock holders",
		"1 check(s) ok", "cache hit ratio",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered triage list missing %q:\n%s", want, out)
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
	if got := triageTargetLabel(pg.TriageResult{DiagKey: "nope"}); got != "diagnostic" {
		t.Errorf("unknown key fallback = %q", got)
	}
}
