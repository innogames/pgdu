package pg

import "testing"

func TestHeapKeyProjection(t *testing.T) {
	for _, tc := range []struct {
		name string
		cols []string
		want string
	}{
		{"no primary key", nil, ""},
		{"single column", []string{"id"},
			`left(src."id"::text, 64)`},
		// A composite key renders as a tuple literal so it reads the way the
		// user would write it: WHERE (tenant_id, id) = (7, 42).
		{"composite", []string{"tenant_id", "id"},
			`'(' || concat_ws(', ', left(src."tenant_id"::text, 64), left(src."id"::text, 64)) || ')'`},
		// Identifiers come from the catalog, but quoting still has to survive
		// the pathological ones.
		{"quoted identifier", []string{`we"ird`},
			`left(src."we""ird"::text, 64)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := heapKeyProjection("src", tc.cols); got != tc.want {
				t.Errorf("heapKeyProjection(%q) = %q, want %q", tc.cols, got, tc.want)
			}
		})
	}
}

func TestParseTidText(t *testing.T) {
	for _, tc := range []struct {
		in       string
		blk, off int32
		ok       bool
	}{
		{"(0,50)", 0, 50, true},
		{"(1382,4)", 1382, 4, true},
		// nbtree markers: a pivot or posting tuple's offset word carries flag
		// bits, not an address — callers reject these on btAltTIDOffsetBase.
		{"(0,8193)", 0, 8193, true},
		{"", 0, 0, false},
		{"pivot", 0, 0, false},
		{"(0)", 0, 0, false},
	} {
		t.Run(tc.in, func(t *testing.T) {
			blk, off, ok := parseTidText(tc.in)
			if ok != tc.ok || blk != tc.blk || off != tc.off {
				t.Errorf("parseTidText(%q) = (%d, %d, %v), want (%d, %d, %v)",
					tc.in, blk, off, ok, tc.blk, tc.off, tc.ok)
			}
		})
	}
}
