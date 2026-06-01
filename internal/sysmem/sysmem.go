// Package sysmem exposes a small snapshot of the local machine's memory by
// parsing /proc/meminfo. It is read by pgdu only to annotate the
// shared_buffers summary; failure (non-Linux host, missing file, very old
// kernels without MemAvailable) is non-fatal and signaled by zero fields.
package sysmem

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// Info is what we report from /proc/meminfo. Zero values mean "unknown" so
// callers can suppress the affected UI rather than rendering bogus stats.
type Info struct {
	Total     int64 // MemTotal
	Available int64 // MemAvailable (free + reclaimable cache)
	Free      int64 // MemFree (strictly unallocated; excludes cache)
}

// Read returns MemTotal, MemAvailable and MemFree from /proc/meminfo in
// bytes, or a zero-filled Info if the file is not readable or the expected
// lines are missing. /proc/meminfo reports values in kB units.
func Read() Info {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return Info{}
	}
	defer func() { _ = f.Close() }()
	var info Info
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			info.Total = parseMeminfoKB(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			info.Available = parseMeminfoKB(line)
		case strings.HasPrefix(line, "MemFree:"):
			info.Free = parseMeminfoKB(line)
		}
		if info.Total > 0 && info.Available > 0 && info.Free > 0 {
			break
		}
	}
	return info
}

// parseMeminfoKB pulls the first whitespace-separated number after the
// "<key>:" prefix and returns it as bytes. /proc/meminfo always reports kB
// (despite the unit label being lowercase), so we multiply by 1024.
func parseMeminfoKB(line string) int64 {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return 0
	}
	fields := strings.Fields(line[idx+1:])
	if len(fields) == 0 {
		return 0
	}
	kb, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	return kb * 1024
}
