package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// relativeAge formats a duration as a short human-readable age suffix such as
// "3h ago" or "12d ago". Negative durations (clock skew) read as "0s ago".
func relativeAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return fmt.Sprintf("%dmo ago", int(d.Hours()/(24*30)))
}

// positionLabel reports the cursor's position within the list, e.g.
// "12/438". Returns "0 items" for empty lists so the status line never
// shows the misleading "0/0". When a filter is active, the visible count
// is shown alongside the total ("12/45 of 438") so the user can tell at a
// glance how many rows were hidden.
func positionLabel(s *screen) string {
	total := len(s.items)
	if total == 0 {
		return "0 items"
	}
	vis := s.visibleLen()
	if vis == 0 {
		return fmt.Sprintf("0/0 of %d", total)
	}
	if s.filter != "" {
		return fmt.Sprintf("%d/%d of %d", s.cursor+1, vis, total)
	}
	return fmt.Sprintf("%d/%d", s.cursor+1, vis)
}

// bloatScanLabel returns a short status indicator for the bloat fetch on
// the parts level. FillBloat is a single Cmd that covers every part, so
// the states are "scanning…" (in flight) or "ready" (done) —
// any partial scanned count comes from individual rows whose bloat could
// not be measured (e.g. unsupported index access methods).
func bloatScanLabel(s *screen) string {
	if s.level != levelParts || len(s.items) == 0 {
		return ""
	}
	if s.bloatScanning {
		return "bloat: scanning…"
	}
	scanned := 0
	for _, it := range s.items {
		if it.hasBloat {
			scanned++
		}
	}
	if scanned == 0 {
		return ""
	}
	if scanned == len(s.items) {
		return "bloat: ready"
	}
	return fmt.Sprintf("bloat: %d/%d scanned", scanned, len(s.items))
}

// levelLabel returns a short human-readable name for the given screen level.
func levelLabel(l level) string {
	switch l {
	case levelTools:
		return "tools"
	case levelDatabases:
		return "databases"
	case levelSchemas:
		return "schemas"
	case levelTables:
		return "tables"
	case levelBufferTables:
		return "buffer-tables"
	case levelBufferDetail:
		return "buffer-detail"
	case levelShmem:
		return "shmem"
	case levelParts:
		return "parts"
	case levelColumns:
		return "columns"
	case levelHeapPages:
		return "heap-pages"
	case levelHeapTuples:
		return "heap-tuples"
	case levelTupleRow:
		return "tuple-row"
	case levelRelations:
		return "relations"
	case levelIndexPages:
		return "index-pages"
	case levelIndexTuples:
		return "index-tuples"
	case levelDescribe:
		return "describe"
	case levelDiagnostics:
		return "diagnostics"
	case levelDiagnosticResult:
		return "diag-result"
	case levelWAL:
		return "wal"
	case levelWALRecords:
		return "wal-records"
	case levelWALBlocks:
		return "wal-blocks"
	case levelWALRelations:
		return "wal-relations"
	case levelWALRelBlocks:
		return "wal-rel-blocks"
	case levelStatements:
		return "queries"
	case levelStatementDetail:
		return "query-detail"
	case levelStatementResult:
		return "query-result"
	case levelSnapshots:
		return "snapshots"
	case levelActivity:
		return "activity"
	case levelLockTree:
		return "lock-tree"
	case levelTableStats:
		return "table overview"
	case levelMaintenance:
		return "system overview"
	case levelSettings:
		return "settings"
	case levelProgress:
		return "progress"
	case levelTriage:
		return "triage"
	case levelWaitProfile:
		return "wait-profile"
	}
	return "?"
}

func formatRows(n int64) string {
	if n < 0 {
		return "?"
	}
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fG", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return strconv.FormatInt(n, 10)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// fmtFloat renders a number with up to 1 decimals, trailing zeros stripped.
func fmtFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	if strings.ContainsRune(s, '.') {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// fmt1 renders a number with exactly one decimal place (60 → "60.0", 98.51 →
// "98.5"). The top-queries numeric columns use it so every value shows a single
// fractional digit rather than a ragged mix of 0/1/2 places.
func fmt1(f float64) string {
	return strconv.FormatFloat(f, 'f', 1, 64)
}

// fmtMs formats a millisecond duration compactly: sub-millisecond and small
// values keep ms; large values switch to seconds so the column stays narrow.
func fmtMs(ms float64) string {
	if ms >= 100000 {
		return fmt1(ms/1000) + "s"
	}
	return fmt1(ms)
}

// fmtAge formats an elapsed time (in ms) as an age with an explicit, scale-
// appropriate unit so values never read ambiguously: "850ms", "31.1s", "11.2m",
// "3.1h", "2.4d". Unlike fmtMs the unit is always present, which is what lets the
// reader tell 105ms from 105s at a glance (paired with durationStyle colouring).
func fmtAge(ms float64) string {
	switch {
	case ms < 1000:
		return fmt.Sprintf("%.0fms", ms)
	case ms < 60*1000:
		return fmt1(ms/1000) + "s"
	case ms < 60*60*1000:
		return fmt1(ms/(60*1000)) + "m"
	case ms < 24*60*60*1000:
		return fmt1(ms/(60*60*1000)) + "h"
	default:
		return fmt1(ms/(24*60*60*1000)) + "d"
	}
}

// fmtDuration renders a window span with explicit units — "45s", "13m 12s",
// "2h 05m", "3d 4h" — so it never reads as a wall-clock time. The old H:MM:SS
// form made "13:12" ambiguous with a start timestamp, which is the whole reason
// it sits next to "since 05:56:46".
func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	default:
		return fmt.Sprintf("%dd %dh", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
	}
}
