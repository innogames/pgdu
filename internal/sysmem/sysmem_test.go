package sysmem

import "testing"

func TestParseMeminfoKB(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int64
	}{
		{"well-formed", "MemTotal:       16384000 kB", 16384000 * 1024},
		{"single space", "MemFree: 1024 kB", 1024 * 1024},
		{"no unit label still parses first number", "MemAvailable: 2048", 2048 * 1024},
		{"zero", "MemFree:        0 kB", 0},
		{"missing colon", "MemTotal 1024 kB", 0},
		{"no number after colon", "MemTotal: kB", 0},
		{"non-numeric value", "MemTotal: lots kB", 0},
		{"empty", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseMeminfoKB(c.in); got != c.want {
				t.Errorf("parseMeminfoKB(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}
