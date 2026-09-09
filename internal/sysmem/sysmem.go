// Package sysmem exposes a small snapshot of the local machine's memory by
// parsing /proc/meminfo. pgdu reads it only to annotate Postgres-side figures
// (shared_buffers against host RAM, huge-page allocation, swap); failure
// (non-Linux host, missing file, very old kernels without MemAvailable) is
// non-fatal and signaled by zero fields.
package sysmem

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
)

// Info is what we report from /proc/meminfo. Zero values mean "unknown" so
// callers can suppress the affected UI rather than rendering bogus stats.
// Byte fields are bytes; the HugePages_* counters are page counts, as in the
// file, because that is the unit vm.nr_hugepages is set in.
type Info struct {
	Total     int64 // MemTotal
	Available int64 // MemAvailable (free + reclaimable cache)
	Free      int64 // MemFree (strictly unallocated; excludes cache)
	Cached    int64 // Cached: the page cache, what effective_cache_size should reflect
	SwapTotal int64 // SwapTotal
	SwapFree  int64 // SwapFree

	// Huge pages: the kernel pool huge_pages=try/on draws from. Total = 0 with
	// huge_pages=try is the silent fallback to 4 kB pages that the overview
	// flags. HugePageSize is in bytes (Hugepagesize is reported in kB).
	HugePagesTotal int64
	HugePagesFree  int64
	HugePageSize   int64
}

// SwapUsed is SwapTotal - SwapFree, clamped at zero; 0 when swap is unknown.
func (i Info) SwapUsed() int64 {
	return max(i.SwapTotal-i.SwapFree, 0)
}

// Read parses /proc/meminfo, returning a zero-filled Info if the file is not
// readable.
func Read() Info {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return Info{}
	}
	defer func() { _ = f.Close() }()
	return parse(f)
}

// parse reads meminfo-formatted lines from r. /proc/meminfo reports memory
// sizes in kB (despite the lowercase unit label) and the HugePages_* rows as
// bare page counts.
func parse(r io.Reader) Info {
	var info Info
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		key, _, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch key {
		case "MemTotal":
			info.Total = parseMeminfoKB(line)
		case "MemAvailable":
			info.Available = parseMeminfoKB(line)
		case "MemFree":
			info.Free = parseMeminfoKB(line)
		case "Cached":
			info.Cached = parseMeminfoKB(line)
		case "SwapTotal":
			info.SwapTotal = parseMeminfoKB(line)
		case "SwapFree":
			info.SwapFree = parseMeminfoKB(line)
		case "HugePages_Total":
			info.HugePagesTotal = parseMeminfoCount(line)
		case "HugePages_Free":
			info.HugePagesFree = parseMeminfoCount(line)
		case "Hugepagesize":
			info.HugePageSize = parseMeminfoKB(line)
		}
	}
	// Best-effort by design: a read error mid-file just leaves the remaining
	// fields at their zero ("unknown") value.
	_ = sc.Err()
	return info
}

// parseMeminfoKB pulls the first whitespace-separated number after the
// "<key>:" prefix and returns it as bytes (the file reports kB).
func parseMeminfoKB(line string) int64 {
	return parseMeminfoCount(line) * 1024
}

// parseMeminfoCount pulls the first whitespace-separated number after the
// "<key>:" prefix as-is, for the unitless HugePages_* rows.
func parseMeminfoCount(line string) int64 {
	_, after, ok := strings.Cut(line, ":")
	if !ok {
		return 0
	}
	fields := strings.Fields(after)
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	return n
}
