package diagres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestFormatValue(t *testing.T) {
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
			got := FormatValue(c.in, KindText)
			if got.Display != c.want || got.HasNum != c.wantHasNum || (got.HasNum && got.Num != c.wantNum) {
				t.Errorf("FormatValue(%v) = %+v, want Display=%q Num=%v HasNum=%v",
					c.in, got, c.want, c.wantNum, c.wantHasNum)
			}
		})
	}
}

func TestFormatInterval(t *testing.T) {
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
			if got := formatInterval(c.iv); got != c.want {
				t.Errorf("formatInterval(%+v) = %q, want %q", c.iv, got, c.want)
			}
		})
	}
}

// A column the name heuristic left as text but whose values are pg_size_pretty
// strings must promote to KindBytes (not KindInt), so its cells and Σ footer
// humanize in the same units — the fix for a footer showing a raw byte sum.
func TestPromotedNumericKind(t *testing.T) {
	cases := []struct {
		in   any
		want Kind
	}{
		{"306 MB", KindBytes},
		{"9832 kB", KindBytes},
		{"0 bytes", KindBytes},
		{"game_conversation_message", KindInt}, // non-size string that reached the numeric-promotion path
		{int64(42), KindInt},
		{3.14, KindInt},
	}
	for _, c := range cases {
		if got := PromotedNumericKind(c.in); got != c.want {
			t.Errorf("PromotedNumericKind(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestKindFromName(t *testing.T) {
	cases := []struct {
		name string
		want Kind
	}{
		{"cache_hit_pct", KindPercent},
		{"dead_ratio", KindPercent},
		{"percent_used", KindPercent},
		{"total_bytes", KindBytes},
		// "_mb" is NOT KindBytes: KindBytes humanizes the value as raw bytes,
		// so a megabyte-scaled column would be off by 1024². Queries emit raw
		// bytes with a "bytes" suffix instead.
		{"size_mb", KindText},
		{"relname", KindText},
	}
	for _, c := range cases {
		if got := KindFromName(c.name); got != c.want {
			t.Errorf("KindFromName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
