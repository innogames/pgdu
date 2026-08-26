package humanize

import (
	"strconv"
	"strings"
)

// sizePrettyUnits maps the unit suffixes emitted by PostgreSQL's
// pg_size_pretty() to their byte multipliers. pg_size_pretty is 1024-based, so
// these mirror the server's own thresholds. The spellings ("bytes", "kB", …)
// intentionally differ from Bytes' output table ("B", "KB", …): this parses
// pg_size_pretty output, not strings produced by Bytes.
var sizePrettyUnits = map[string]float64{
	"bytes": 1,
	"kB":    1 << 10,
	"MB":    1 << 20,
	"GB":    1 << 30,
	"TB":    1 << 40,
	"PB":    1 << 50,
}

// ParseSizePretty parses a string in the exact "<number> <unit>" form produced
// by pg_size_pretty() (e.g. "9832 kB", "97 MB", "0 bytes") into a byte count.
// The match is deliberately strict — number, single space, known unit — so a
// genuine text column is never mistaken for a size and given a bogus sort key.
func ParseSizePretty(s string) (float64, bool) {
	num, unit, ok := strings.Cut(s, " ")
	if !ok {
		return 0, false
	}
	mult, ok := sizePrettyUnits[unit]
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, false
	}
	return f * mult, true
}
