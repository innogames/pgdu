package humanize

import (
	"strconv"
	"strings"
)

// Float renders f with up to `decimals` fractional digits, stripping trailing
// zeros so "12.00" becomes "12" and "3.10" becomes "3.1".
func Float(f float64, decimals int) string {
	s := strconv.FormatFloat(f, 'f', decimals, 64)
	if strings.ContainsRune(s, '.') {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}
