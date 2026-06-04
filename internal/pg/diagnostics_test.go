package pg

import "testing"

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
