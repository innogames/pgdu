package pg

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestParseSizePretty(t *testing.T) {
	cases := []struct {
		in     string
		want   float64
		wantOK bool
	}{
		{"0 bytes", 0, true},
		{"512 bytes", 512, true},
		{"9832 kB", 9832 * 1024, true},
		{"97 MB", 97 * 1024 * 1024, true},
		{"5.5 GB", 5.5 * 1024 * 1024 * 1024, true},
		// not pg_size_pretty output — must not be mistaken for a size
		{"", 0, false},
		{"Y", 0, false},
		{"123", 0, false},
		{"5 apples", 0, false},
		{"game_conversation_message", 0, false},
		{"MB", 0, false},
	}
	for _, c := range cases {
		got, ok := parseSizePretty(c.in)
		if ok != c.wantOK || (ok && got != c.want) {
			t.Errorf("parseSizePretty(%q) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

// "97 MB" must sort above "9832 kB" — the bug being fixed: string columns
// produced by pg_size_pretty used to sort lexically, ignoring the unit.
func TestSizePrettyOrdering(t *testing.T) {
	mb97, _ := parseSizePretty("97 MB")
	kb9832, _ := parseSizePretty("9832 kB")
	if !(mb97 > kb9832) {
		t.Errorf("expected 97 MB (%v) > 9832 kB (%v)", mb97, kb9832)
	}
}

func TestFormatDiagValue(t *testing.T) {
	cases := []struct {
		name       string
		in         any
		want       string
		wantNum    float64
		wantHasNum bool
	}{
		{"nil", nil, "—", 0, false},
		{"bool true", true, "t", 0, false},
		{"bool false", false, "f", 0, false},
		{"int64", int64(42), "42", 42, true},
		{"int32", int32(7), "7", 7, true},
		{"float trims zeros", float64(12.0), "12", 12, true},
		{"float one decimal", float64(3.10), "3.1", 3.1, true},
		{"plain string", "hello", "hello", 0, false},
		{"size pretty string sorts numeric", "97 MB", "97 MB", 97 * 1024 * 1024, true},
		{"bytes slice", []byte("raw"), "raw", 0, false},
		{"oid uint32", uint32(16384), "16384", 16384, true},
		{"string slice", []string{"a", "b"}, "a, b", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatDiagValue(c.in, DiagText)
			if got.Display != c.want || got.HasNum != c.wantHasNum || (got.HasNum && got.Num != c.wantNum) {
				t.Errorf("formatDiagValue(%v) = %+v, want Display=%q Num=%v HasNum=%v",
					c.in, got, c.want, c.wantNum, c.wantHasNum)
			}
		})
	}
}

func TestFormatDiagInterval(t *testing.T) {
	cases := []struct {
		name string
		iv   pgtype.Interval
		want string
	}{
		{"zero", pgtype.Interval{Valid: true}, "0s"},
		{"months not whole years", pgtype.Interval{Months: 14, Valid: true}, "14mo"},
		{"whole years", pgtype.Interval{Months: 24, Valid: true}, "2y"},
		{"days", pgtype.Interval{Days: 3, Valid: true}, "3d"},
		{"sub-day time", pgtype.Interval{Microseconds: 90_000_000, Valid: true}, "1m30s"},
		{"days plus zero time omits 0s", pgtype.Interval{Days: 5, Valid: true}, "5d"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatDiagInterval(c.iv); got != c.want {
				t.Errorf("formatDiagInterval(%+v) = %q, want %q", c.iv, got, c.want)
			}
		})
	}
}

// A column the name heuristic left as text but whose values are pg_size_pretty
// strings must promote to DiagBytes (not DiagInt), so its cells and Σ footer
// humanize in the same units — the fix for a footer showing a raw byte sum.
func TestPromotedNumericKind(t *testing.T) {
	cases := []struct {
		in   any
		want DiagColumnKind
	}{
		{"306 MB", DiagBytes},
		{"9832 kB", DiagBytes},
		{"0 bytes", DiagBytes},
		{"game_conversation_message", DiagInt}, // non-size string that reached the numeric-promotion path
		{int64(42), DiagInt},
		{3.14, DiagInt},
	}
	for _, c := range cases {
		if got := promotedNumericKind(c.in); got != c.want {
			t.Errorf("promotedNumericKind(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestColKindFromName(t *testing.T) {
	cases := []struct {
		name string
		want DiagColumnKind
	}{
		{"cache_hit_pct", DiagPercent},
		{"dead_ratio", DiagPercent},
		{"percent_used", DiagPercent},
		{"total_bytes", DiagBytes},
		// "_mb" is NOT DiagBytes: DiagBytes humanizes the value as raw bytes,
		// so a megabyte-scaled column would be off by 1024². Queries emit raw
		// bytes with a "bytes" suffix instead.
		{"size_mb", DiagText},
		{"relname", DiagText},
	}
	for _, c := range cases {
		if got := colKindFromName(c.name); got != c.want {
			t.Errorf("colKindFromName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
