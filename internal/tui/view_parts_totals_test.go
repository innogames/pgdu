package tui

import (
	"strings"
	"testing"

	"pgdu/internal/pg"
)

// The Σ bloat percentage is waste over the parts that were measured. TOAST has
// no estimator, so dividing by the total would report a near-zero percentage
// for a table whose TOAST dwarfs its heap and indexes.
func TestPartsTotalsBloatPctExcludesUnmeasured(t *testing.T) {
	const mb = int64(1) << 20
	s := &screen{level: levelParts, tool: toolDisk, items: []item{
		{data: pg.Part{Kind: pg.PartToast, SizeBytes: 54490 * mb}, size: 54490 * mb},
		{data: pg.Part{Kind: pg.PartHeap, SizeBytes: 381 * mb}, size: 381 * mb, bloat: 31 * mb, hasBloat: true},
		{data: pg.Part{Kind: pg.PartIndex, SizeBytes: 373 * mb}, size: 373 * mb, bloat: 123 * mb, hasBloat: true},
	}}
	m := newTestModel(s)
	got := squashSpaces(m.renderPartsTotals(s))
	// 154 MB wasted across the 754 MB that was measured.
	if !strings.Contains(got, "20%") {
		t.Errorf("Σ footer = %q, want a 20%% bloat figure", got)
	}
}
