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
