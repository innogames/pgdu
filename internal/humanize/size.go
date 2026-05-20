package humanize

import "fmt"

var units = []string{"B", "KB", "MB", "GB", "TB", "PB"}

// Bytes formats a byte count using 1024-based units with two digits of
// precision once we're past kilobytes.
func Bytes(n int64) string {
	if n < 0 {
		return "-" + Bytes(-n)
	}
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.2f %s", f, units[i])
}
