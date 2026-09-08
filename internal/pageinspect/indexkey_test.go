package pageinspect

import (
	"math"
	"testing"

	"pgdu/internal/pg"
)

func col(typname, typalign string, typlen int32, typcat string) pg.IndexKeyColumn {
	return pg.IndexKeyColumn{
		Def: typname, IsKey: true,
		TypLen: typlen, TypAlign: typalign, TypName: typname, TypCategory: typcat,
	}
}

var (
	int2Col = col("int2", "s", 2, "N")
	int4Col = col("int4", "i", 4, "N")
	int8Col = col("int8", "d", 8, "N")
	textCol = col("text", "i", -1, "S")
	oidCol  = col("oid", "i", 4, "N")
	boolCol = col("bool", "c", 1, "B")
)

func TestDecodeIndexKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		hex  string
		cols []pg.IndexKeyColumn
		want string
		ok   bool
	}{
		{
			name: "int8 bigint",
			hex:  "45 4b 86 67 00 00 00 00",
			cols: []pg.IndexKeyColumn{int8Col},
			want: "1736854341", ok: true,
		},
		{
			name: "int4",
			hex:  "17 00 00 00",
			cols: []pg.IndexKeyColumn{int4Col},
			want: "23", ok: true,
		},
		{
			// Regression: this int8's little-endian bytes are all printable
			// ASCII ("T7"), which the old decodeHexKey rendered as garbage text.
			name: "int8 with printable bytes",
			hex:  "54 37 00 00 00 00 00 00",
			cols: []pg.IndexKeyColumn{int8Col},
			want: "14164", ok: true,
		},
		{
			name: "int2 negative is signed",
			hex:  "ff ff",
			cols: []pg.IndexKeyColumn{int2Col},
			want: "-1", ok: true,
		},
		{
			name: "oid is unsigned",
			hex:  "ff ff ff ff",
			cols: []pg.IndexKeyColumn{oidCol},
			want: "4294967295", ok: true,
		},
		{
			name: "bool true",
			hex:  "01",
			cols: []pg.IndexKeyColumn{boolCol},
			want: "t", ok: true,
		},
		{
			// Composite (int4, text): player_id=23, then a short-varlena
			// "manufacturing" packed right after the int with no padding.
			name: "composite int4 + text",
			hex:  "17 00 00 00 1d 6d 61 6e 75 66 61 63 74 75 72 69 6e 67",
			cols: []pg.IndexKeyColumn{int4Col, textCol},
			want: "(23,manufacturing)", ok: true,
		},
		{
			// Suffix-truncated separator: the pivot stores only the leading
			// column, so the rest is dropped — show what's there plus an ellipsis.
			name: "suffix-truncated composite",
			hex:  "17 00 00 00",
			cols: []pg.IndexKeyColumn{int4Col, textCol},
			want: "23…", ok: true,
		},
		{
			// pageinspect cut the hex short after a complete value.
			name: "pageinspect truncation marker",
			hex:  "0d 77 68 65 65 6c …",
			cols: []pg.IndexKeyColumn{textCol},
			want: "wheel…", ok: true,
		},
		{
			// Control characters escape to \n / \t / \xNN instead of hexing
			// the whole value — "ab\ncd" with a 1-byte varlena header.
			name: "text with newline",
			hex:  "0d 61 62 0a 63 64",
			cols: []pg.IndexKeyColumn{textCol},
			want: `ab\ncd`, ok: true,
		},
		{
			name: "empty data (minus infinity)",
			hex:  "",
			cols: []pg.IndexKeyColumn{int8Col},
			want: "", ok: false,
		},
		{
			name: "no column types",
			hex:  "17 00 00 00",
			cols: nil,
			want: "", ok: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DecodeIndexKey(tc.hex, tc.cols)
			if got != tc.want || ok != tc.ok {
				t.Errorf("DecodeIndexKey(%q) = (%q, %v), want (%q, %v)",
					tc.hex, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestDecodeIndexKeyPivot covers suffix-truncated separators: natts (from the
// pivot's ctid offset word) says how many attributes the tuple stores; the
// dropped ones render as −∞, the way nbtree compares them.
func TestDecodeIndexKeyPivot(t *testing.T) {
	cols := []pg.IndexKeyColumn{int4Col, textCol}
	for _, tc := range []struct {
		name  string
		hex   string
		natts int
		want  string
		ok    bool
	}{
		// int4 19224 followed by MAXALIGN zero padding: only the leading
		// column survived truncation.
		{"truncated to leading int", "18 4b 00 00 00 00 00 00", 1, "(19224,−∞)", true},
		// A full separator isn't this function's job — the generic decoder
		// already renders every stored attribute.
		{"natts covers all columns", "17 00 00 00 05 61", 2, "", false},
		// Non-zero leftovers past the stored attributes mean natts wasn't a
		// pivot word (a real heap offset that happened to be small).
		{"leftover data bytes", "17 00 00 00 05 61 62 63", 1, "", false},
		{"minus-infinity downlink", "", 0, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DecodeIndexKeyPivot(tc.hex, cols, tc.natts)
			if got != tc.want || ok != tc.ok {
				t.Errorf("DecodeIndexKeyPivot(%q, natts=%d) = (%q, %v), want (%q, %v)",
					tc.hex, tc.natts, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestFormatPGTemporal(t *testing.T) {
	const day = int64(86_400_000_000) // microseconds in a day
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"ts epoch", formatPGTimestamp(0), "2000-01-01 00:00:00"},
		{"ts one day", formatPGTimestamp(day), "2000-01-02 00:00:00"},
		{"ts fractional", formatPGTimestamp(1), "2000-01-01 00:00:00.000001"},
		{"ts +infinity", formatPGTimestamp(math.MaxInt64), "infinity"},
		{"ts -infinity", formatPGTimestamp(math.MinInt64), "-infinity"},
		{"date epoch", formatPGDate(0), "2000-01-01"},
		{"date one day", formatPGDate(1), "2000-01-02"},
		{"date before epoch", formatPGDate(-1), "1999-12-31"},
		{"date +infinity", formatPGDate(math.MaxInt32), "infinity"},
		{"time midnight", formatPGTime(0), "00:00:00"},
		{"time 01:01:01", formatPGTime(3_661_000_000), "01:01:01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

// TestDecodeIndexKeyTimestamp confirms the temporal path is wired through the
// decoder: 8 zero bytes of a timestamp column render as the PG epoch.
func TestDecodeIndexKeyTimestamp(t *testing.T) {
	tsCol := col("timestamp", "d", 8, "D")
	got, ok := DecodeIndexKey("00 00 00 00 00 00 00 00", []pg.IndexKeyColumn{tsCol})
	if !ok || got != "2000-01-01 00:00:00" {
		t.Errorf("DecodeIndexKey(timestamp 0) = (%q, %v), want (%q, true)",
			got, ok, "2000-01-01 00:00:00")
	}
}

func TestKeyValueLess(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"99", "100", true},      // numeric, not lexicographic
		{"100", "99", false},     // numeric
		{"-5", "3", true},        // signed numeric
		{"apple", "mango", true}, // text
		{"mango", "apple", false},
		{"2026-06-16 01:00:00", "2026-06-16 02:00:00", true}, // ISO timestamps sort lexicographically
		{"3.5", "10.2", true},                                // float fallback
	} {
		if got := KeyValueLess(tc.a, tc.b); got != tc.want {
			t.Errorf("KeyValueLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestLeadingKeyValue(t *testing.T) {
	// Composite (int4, text): leading value is just the int4.
	data := "17 00 00 00 1d 6d 61 6e 75 66 61 63 74 75 72 69 6e 67"
	got, ok := LeadingKeyValue(
		pg.IndexTuple{Data: &data},
		[]pg.IndexKeyColumn{int4Col, textCol},
	)
	if !ok || got != "23" {
		t.Errorf("LeadingKeyValue = (%q, %v), want (\"23\", true)", got, ok)
	}
	// Minus-infinity downlink: no data, no key.
	if _, ok := LeadingKeyValue(pg.IndexTuple{}, []pg.IndexKeyColumn{int4Col}); ok {
		t.Errorf("LeadingKeyValue(no data) ok = true, want false")
	}
}
