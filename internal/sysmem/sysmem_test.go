package sysmem

import (
	"strings"
	"testing"
)

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

// TestParse feeds a trimmed real-world /proc/meminfo through parse: the kB rows
// scale to bytes, the HugePages_* rows stay page counts, and rows we don't
// track are ignored. Cached sits well below MemTotal so the field mapping can't
// be confused by coincidental equality.
func TestParse(t *testing.T) {
	const meminfo = `MemTotal:       32768000 kB
MemFree:         1024000 kB
MemAvailable:   20480000 kB
Buffers:          512000 kB
Cached:         16384000 kB
SwapCached:            0 kB
Active:         10000000 kB
SwapTotal:       8388604 kB
SwapFree:        8000000 kB
Dirty:              1234 kB
Shmem:           5500000 kB
HugePages_Total:    2760
HugePages_Free:      100
HugePages_Rsvd:        0
HugePages_Surp:        0
Hugepagesize:       2048 kB
Hugetlb:         5652480 kB
`
	got := parse(strings.NewReader(meminfo))
	want := Info{
		Total:          32768000 * 1024,
		Free:           1024000 * 1024,
		Available:      20480000 * 1024,
		Cached:         16384000 * 1024,
		SwapTotal:      8388604 * 1024,
		SwapFree:       8000000 * 1024,
		HugePagesTotal: 2760,
		HugePagesFree:  100,
		HugePageSize:   2048 * 1024,
	}
	if got != want {
		t.Errorf("parse() = %+v, want %+v", got, want)
	}
	if got.SwapUsed() != (8388604-8000000)*1024 {
		t.Errorf("SwapUsed() = %d", got.SwapUsed())
	}
	if (Info{SwapTotal: 10, SwapFree: 20}).SwapUsed() != 0 {
		t.Error("SwapUsed must clamp at zero")
	}
	if empty := parse(strings.NewReader("")); empty != (Info{}) {
		t.Errorf("parse(empty) = %+v, want zero", empty)
	}
}

func TestParseMeminfoCount(t *testing.T) {
	if got := parseMeminfoCount("HugePages_Total:    2760"); got != 2760 {
		t.Errorf("got %d, want 2760", got)
	}
	if got := parseMeminfoCount("HugePages_Total 2760"); got != 0 {
		t.Errorf("missing colon: got %d, want 0", got)
	}
}
