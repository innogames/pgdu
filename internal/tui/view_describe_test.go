package tui

import (
	"strings"
	"testing"
	"time"

	"pgdu/internal/pg"
)

func TestRenderDescribeInfoVariants(t *testing.T) {
	m := &Model{width: 160}
	for _, tc := range []struct {
		kind pg.DescribeKind
		want []string
		skip []string
	}{
		{pg.DescribeTable, []string{"Describe table reference", "covers", "hit", "cache footprint", "toggle detail mode"}, []string{"idx_tup_read"}},
		{pg.DescribeIndex, []string{"Describe index reference", "covers", "idx_tup_read", "partial predicate"}, []string{"cache footprint", "toggle detail mode"}},
	} {
		s := &screen{level: levelDescribe, desc: describeState{info: &pg.Description{Kind: tc.kind}}}
		if !m.hasInfoOverlay(s) {
			t.Fatalf("describe level has no ? overlay")
		}
		out := m.renderInfoOverlay(s, 200)
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%v: missing %q", tc.kind, w)
			}
		}
		for _, w := range tc.skip {
			if strings.Contains(out, w) {
				t.Errorf("%v: unexpected %q", tc.kind, w)
			}
		}
	}
}

func TestDescribeIndexCoverage(t *testing.T) {
	idx := pg.DescribeIndexDef{Predicate: "(opened = true)", EstEntries: 950}
	if pct, ok := idx.CoveredPct(1000); !ok || pct != 95 {
		t.Fatalf("CoveredPct = %v,%v", pct, ok)
	}
	if _, ok := idx.CoveredPct(0); ok {
		t.Fatal("empty table should not estimate")
	}
	if _, ok := (pg.DescribeIndexDef{EstEntries: -1}).CoveredPct(1000); ok {
		t.Fatal("unanalyzed index should not estimate")
	}
	if pct, _ := (pg.DescribeIndexDef{EstEntries: 1200}).CoveredPct(1000); pct != 100 {
		t.Fatalf("drifted estimates should clamp to 100, got %v", pct)
	}
	if got := describeIndexCoverage(0, false); !strings.Contains(got, "?") {
		t.Fatalf("missing estimate should render ?, got %q", got)
	}
	if got := describeIndexCoverage(12.34, true); !strings.Contains(got, "~12.3%") {
		t.Fatalf("got %q", got)
	}
}

func TestDescribeIndexIdle(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	for _, tc := range []struct {
		name string
		idx  pg.DescribeIndexDef
		want bool
	}{
		{"fresh", pg.DescribeIndexDef{Scans: 5, LastScan: at(35 * time.Second)}, false},
		{"just under", pg.DescribeIndexDef{Scans: 5, LastScan: at(time.Hour - time.Second)}, false},
		{"stale", pg.DescribeIndexDef{Scans: 5, LastScan: at(5 * time.Hour)}, true},
		{"never scanned, flagged unused", pg.DescribeIndexDef{Scans: 0}, false},
		{"never scanned primary", pg.DescribeIndexDef{Scans: 0, IsPrimary: true}, true},
	} {
		if got := describeIndexIdle(tc.idx, now); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
