package tui

import (
	"testing"
	"time"
)

func TestFormatRows(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{-1, "?"},
		{0, "0"},
		{999, "999"},
		{1000, "1.0k"},
		{1500, "1.5k"},
		{1_000_000, "1.0M"},
		{2_500_000, "2.5M"},
		{1_000_000_000, "1.0G"},
		{3_200_000_000, "3.2G"},
	}
	for _, c := range cases {
		if got := formatRows(c.in); got != c.want {
			t.Errorf("formatRows(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShortLSN(t *testing.T) {
	if got := shortLSN("0/16B3748"); got != "16B3748" {
		t.Errorf("shortLSN = %q, want %q", got, "16B3748")
	}
	// No slash: returned unchanged.
	if got := shortLSN("16B3748"); got != "16B3748" {
		t.Errorf("shortLSN (no slash) = %q, want %q", got, "16B3748")
	}
}

func TestFmtDuration(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"seconds", 45 * time.Second, "45s"},
		{"minutes and seconds", 13*time.Minute + 12*time.Second, "13m 12s"},
		{"minutes pad seconds", 13*time.Minute + 2*time.Second, "13m 02s"},
		{"hours and minutes", 2*time.Hour + 5*time.Minute, "2h 05m"},
		{"days and hours", 3*24*time.Hour + 4*time.Hour, "3d 4h"},
		{"rounds to second", 999 * time.Millisecond, "1s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fmtDuration(c.d); got != c.want {
				t.Errorf("fmtDuration(%v) = %q, want %q", c.d, got, c.want)
			}
		})
	}
}

func TestRelativeAge(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"negative clamps to 0s", -5 * time.Second, "0s ago"},
		{"seconds", 30 * time.Second, "30s ago"},
		{"minutes", 5 * time.Minute, "5m ago"},
		{"hours", 3 * time.Hour, "3h ago"},
		{"days", 4 * 24 * time.Hour, "4d ago"},
		{"months", 60 * 24 * time.Hour, "2mo ago"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := relativeAge(c.d); got != c.want {
				t.Errorf("relativeAge(%v) = %q, want %q", c.d, got, c.want)
			}
		})
	}
}

func TestPositionLabel(t *testing.T) {
	items := []item{{name: "users"}, {name: "orders"}, {name: "order_items"}}

	// Empty list never shows the misleading "0/0".
	if got := positionLabel(&screen{}); got != "0 items" {
		t.Errorf("empty = %q, want %q", got, "0 items")
	}
	// No filter: cursor position over the visible (== total) count.
	if got := positionLabel(&screen{items: items, cursor: 0}); got != "1/3" {
		t.Errorf("no filter top = %q, want %q", got, "1/3")
	}
	if got := positionLabel(&screen{items: items, cursor: 2}); got != "3/3" {
		t.Errorf("no filter bottom = %q, want %q", got, "3/3")
	}
	// Active filter shows visible/total so hidden rows are accounted for.
	if got := positionLabel(&screen{items: items, filter: "ord", cursor: 0}); got != "1/2 of 3" {
		t.Errorf("filtered = %q, want %q", got, "1/2 of 3")
	}
	// Filter matching nothing: distinct "0/0 of N", not "0 items".
	if got := positionLabel(&screen{items: items, filter: "zzz"}); got != "0/0 of 3" {
		t.Errorf("filter no match = %q, want %q", got, "0/0 of 3")
	}
}

func TestBloatScanLabel(t *testing.T) {
	withBloat := item{hasBloat: true}
	cases := []struct {
		name string
		s    *screen
		want string
	}{
		{"non-parts level", &screen{level: levelTables, items: []item{withBloat}}, ""},
		{"parts but empty", &screen{level: levelParts}, ""},
		{"scanning in flight", &screen{level: levelParts, bloatScanning: true, items: []item{{}}}, "bloat: scanning…"},
		{"all measured", &screen{level: levelParts, items: []item{withBloat, withBloat}}, "bloat: ready"},
		{"partial", &screen{level: levelParts, items: []item{withBloat, {}}}, "bloat: 1/2 scanned"},
		{"none measured yet", &screen{level: levelParts, items: []item{{}, {}}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bloatScanLabel(c.s); got != c.want {
				t.Errorf("bloatScanLabel = %q, want %q", got, c.want)
			}
		})
	}
}

func TestMaxInt(t *testing.T) {
	if got := maxInt(3, 7); got != 7 {
		t.Errorf("maxInt(3,7) = %d, want 7", got)
	}
	if got := maxInt(7, 3); got != 7 {
		t.Errorf("maxInt(7,3) = %d, want 7", got)
	}
	if got := maxInt(-2, -5); got != -2 {
		t.Errorf("maxInt(-2,-5) = %d, want -2", got)
	}
}
