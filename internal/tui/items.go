package tui

import (
	"fmt"
	"strings"
	"time"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// --- item builders (db rows → tui rows) ---

func schemaDetail(sc pg.Schema) string {
	return fmt.Sprintf("%d tables", sc.TableCount)
}

func tableToItem(t pg.Table, tl tool) item {
	// In the page-inspector flow only the heap is browsable — indexes and
	// toast aren't reachable through this drill path. Sizing the row by
	// total-relation-size (and showing the heap/idx/toast breakdown) would
	// suggest otherwise, so we show heap-only stats and surface the page
	// count instead — that's the figure the user actually navigates next.
	if tl == toolPageInspect {
		pages := t.HeapBytes / heapPageBlockSize
		if t.HeapBytes%heapPageBlockSize != 0 {
			pages++
		}
		return item{
			name: t.Name, size: t.HeapBytes, hasChildren: true,
			data: t,
			rows: t.EstRows, hasRows: true,
			pages: pages, hasPages: true,
		}
	}
	// Tables with a tiny TOAST relation (empty or a handful of out-of-line
	// values) clutter the detail line with a near-zero figure. Hide TOAST
	// below 1 MiB — the colored bar segment is already 0-width at that scale.
	const toastShowThreshold = 1 << 20
	parts := []string{
		"heap " + humanize.Bytes(t.HeapBytes),
		"idx " + humanize.Bytes(t.IndexesBytes),
	}
	if t.ToastBytes >= toastShowThreshold {
		parts = append(parts, "toast "+humanize.Bytes(t.ToastBytes))
	}
	return item{
		name: t.Name, size: t.TotalBytes, hasChildren: true,
		detail: strings.Join(parts, " · "), data: t,
		heap: t.HeapBytes, idx: t.IndexesBytes, toast: t.ToastBytes,
		rows: t.EstRows, hasRows: true,
	}
}

// heapPageBlockSize is the standard PostgreSQL page size. pgdu doesn't talk
// to clusters with non-default BLCKSZ; if it ever needs to, this becomes a
// per-connection setting read from current_setting('block_size').
const heapPageBlockSize int64 = 8192

func heapPageToItem(p pg.HeapPageStat) item {
	// Used bytes scale the bar against a fixed BLCKSZ so every row in the
	// heap-pages view shares the same horizontal scale — the eye can
	// compare occupancy across pages without re-reading the numbers.
	used := max(heapPageBlockSize-int64(p.FreeBytes), 0)
	return item{
		name: fmt.Sprintf("page #%07d", p.Blkno),
		size: used,
		data: p,
	}
}

func heapTupleToItem(t pg.HeapTuple) item {
	// hasChildren is set only for NORMAL line pointers — DEAD/UNUSED have
	// no row to fetch, and REDIRECT points at a target on (potentially)
	// another page that we'd need to chase, which the row-detail view
	// doesn't currently do.
	return item{
		name:        fmt.Sprintf("#%04d", t.LP),
		size:        int64(t.LPLen),
		hasChildren: t.LPFlags == pg.LPNormal && t.Ctid != nil,
		data:        t,
	}
}

func tupleCellToItem(c pg.TupleCell) item {
	v := "NULL"
	if c.Value != nil {
		v = *c.Value
	}
	return item{
		name:   c.Name,
		detail: v,
		data:   c,
	}
}

// itemBlkno extracts the block number from a heap- or index-page item.
// Returns (0, false) for items lacking page-summary data so they sort
// below pages we can rank.
func itemBlkno(it item) (int64, bool) {
	switch p := it.data.(type) {
	case pg.HeapPageStat:
		return p.Blkno, true
	case pg.IndexPageStat:
		return int64(p.Blkno), true
	}
	return 0, false
}

// itemDeadRatio is dead/(live+dead) for heap- or index-page items; second
// return is false for empty pages so they don't dominate the dead% sort.
func itemDeadRatio(it item) (float64, bool) {
	var r float64
	switch p := it.data.(type) {
	case pg.HeapPageStat:
		r = p.DeadFrac()
	case pg.IndexPageStat:
		r = p.DeadFrac()
	default:
		return 0, false
	}
	if r < 0 {
		return 0, false
	}
	return r, true
}

// itemFreeSpace returns the per-page free bytes; second return is false for
// items lacking page-summary data.
func itemFreeSpace(it item) (int64, bool) {
	switch p := it.data.(type) {
	case pg.HeapPageStat:
		return int64(p.FreeBytes), true
	case pg.IndexPageStat:
		return int64(p.FreeSize), true
	}
	return 0, false
}

// itemLP extracts the line-pointer index for heap-tuple items, or the
// itemoffset for index-tuple items (same concept — a per-page slot index).
func itemLP(it item) (int64, bool) {
	switch t := it.data.(type) {
	case pg.HeapTuple:
		return int64(t.LP), true
	case pg.IndexTuple:
		return int64(t.ItemOffset), true
	}
	return 0, false
}

// itemTreeLevel returns btpo_level for B-tree page items (0 = leaf).
// Second return is false for non-index-page items.
func itemTreeLevel(it item) (int64, bool) {
	p, ok := it.data.(pg.IndexPageStat)
	if !ok {
		return 0, false
	}
	return int64(p.BtpoLevel), true
}

// relationToItem builds the levelRelations row for one mixed relation entry.
// hasChildren is always true: both tables and B-tree indexes drill into a
// page-inspector view. The detail string is left empty — the dedicated
// renderRelationsList paints the parent name in muted text on index rows
// without a separate detail column.
func relationToItem(r pg.Relation) item {
	pages := max(int64(r.Pages), 0)
	return item{
		name:        r.Name,
		size:        r.SizeBytes,
		hasChildren: true,
		data:        r,
		rows:        r.EstRows,
		hasRows:     true,
		pages:       pages,
		hasPages:    true,
	}
}

func indexPageToItem(p pg.IndexPageStat) item {
	// Used bytes mirror the heap-page item: BLCKSZ minus free. The bar
	// reads as "how packed is this page" at a uniform scale.
	used := max(heapPageBlockSize-int64(p.FreeSize), 0)
	return item{
		name: fmt.Sprintf("page #%07d", p.Blkno),
		size: used,
		data: p,
	}
}

func indexTupleToItem(t pg.IndexTuple) item {
	// hasChildren is set only when a live heap row was projected (Decoded
	// non-nil) — that's the same gate the drill handler uses, so the "+"
	// marker tracks what ENTER will actually do. Internal-page downlinks
	// and entries whose heap row is gone don't drill.
	return item{
		name:        fmt.Sprintf("#%04d", t.ItemOffset),
		size:        int64(t.ItemLen),
		hasChildren: t.Decoded != nil && t.Ctid != nil,
		data:        t,
	}
}

// walRmgrToItem builds one levelWAL row from a resource-manager stat. size is
// the combined byte total (record data + FPI) so the shared bar scales the
// rmgr against its siblings; the FPI split and counts render as their own
// columns / bar segment in renderWALList.
func walRmgrToItem(s pg.WALRmgrStat) item {
	return item{
		name:        s.Name,
		size:        s.CombinedSize,
		hasChildren: s.Count > 0,
		data:        s,
	}
}

// walRecordToItem builds one levelWALRecords row. size is the combined byte
// total (record_length + fpi_length). hasChildren is always true: every
// record can be drilled into for its block references (the list may turn out
// empty on PG 15 where pg_get_wal_block_info is absent — surfaced then).
func walRecordToItem(r pg.WALRecord) item {
	return item{
		name:        r.RecordType,
		size:        r.CombinedSize(),
		detail:      r.Description,
		hasChildren: true,
		data:        r,
	}
}

// walBlockToItem builds one levelWALBlocks row. size is the FPI length — the
// bar reads as "how much full-page-image write amplification did this block
// reference cost". Block refs are leaves (no further drill).
func walBlockToItem(b pg.WALBlockRef) item {
	// Prefer the resolved relation name; fall back to the raw relfilenode when
	// the relation lives in another database or has been dropped.
	target := fmt.Sprintf("%d", b.RelFileNode)
	if b.RelName != "" {
		target = b.RelName
	}
	if b.IsToast {
		target += " (toast)"
	}
	return item{
		name: fmt.Sprintf("rel %s/%s blk %d", target, b.ForkName(), b.BlockNumber),
		size: int64(b.FPILength),
		data: b,
	}
}

// itemWALCount / itemWALFPI extract the record count and FPI bytes from a
// levelWAL rmgr-stat item. Second return is false for items without that
// payload so they sort below rows we can rank.
func itemWALCount(it item) (int64, bool) {
	s, ok := it.data.(pg.WALRmgrStat)
	if !ok {
		return 0, false
	}
	return s.Count, true
}

func itemWALFPI(it item) (int64, bool) {
	switch v := it.data.(type) {
	case pg.WALRmgrStat:
		return v.FPISize, true
	case pg.WALRecord:
		return int64(v.FPILength), true
	}
	return 0, false
}

func bufferStatToItem(s pg.TableBufferStat) item {
	// detail is left empty: the per-row figures (table size, cached %, hit %)
	// are rendered as their own columns in renderBufferList.
	return item{
		name: s.Schema + "." + s.Name,
		size: s.BufferedBytes,
		data: s,
	}
}

func columnToItem(col pg.Column) item {
	nullPart := ""
	if col.NullFrac > 0.005 {
		nullPart = fmt.Sprintf(" · %.0f%% null", col.NullFrac*100)
	}
	toastMark := ""
	// 🍞 flags columns whose values are likely actually in TOAST. Capability
	// (extended/external storage on a table with a TOAST relation) isn't enough:
	// PostgreSQL only externalizes values that push the row past
	// TOAST_TUPLE_THRESHOLD (~2 KB). avg_width here is pg_column_size-derived,
	// so a column averaging at/above that threshold is almost certainly being
	// compressed and/or externalized.
	const toastAvgWidthThreshold = 2048
	if col.Toastable && col.AvgWidth >= toastAvgWidthThreshold {
		toastMark = "🍞 "
	}
	detail := fmt.Sprintf("%s%s · avg %s%s", toastMark, col.Type, humanize.Bytes(int64(col.AvgWidth)), nullPart)
	return item{
		name:   col.Name,
		size:   col.EstBytes,
		detail: detail,
		data:   col,
	}
}

func partToItem(p pg.Part) item {
	detail := ""
	switch p.Kind {
	case pg.PartHeap:
		detail = heapDetail(p.HeapStats)
	case pg.PartToast:
		detail = "TOAST storage"
		if p.ToastName != "" {
			detail += " · " + p.ToastName
		}
	case pg.PartIndex:
		var tags []string
		if p.IsPrimary {
			tags = append(tags, "primary")
		}
		if p.IsUnique && !p.IsPrimary {
			tags = append(tags, "unique")
		}
		tags = append(tags, p.AccessMethod)
		detail = "index · " + strings.Join(tags, " · ")
	}
	return item{
		name:        p.Name,
		size:        p.SizeBytes,
		bloat:       p.WastedBytes,
		hasBloat:    p.HasBloat,
		hasChildren: p.Kind == pg.PartHeap, // only heap drills into per-column view
		detail:      detail,
		data:        p,
	}
}

// heapDetail builds the inline status string shown on the heap row at the
// parts level: dead-tuple % and "last vacuum" age. Falls back to "table heap"
// when stats aren't available (e.g. matviews or stats never collected).
func heapDetail(h *pg.HeapStats) string {
	if h == nil {
		return "table heap"
	}
	parts := []string{"heap"}
	if frac := h.DeadFrac(); frac >= 0 && h.NDead > 0 {
		parts = append(parts, fmt.Sprintf("%s dead (%.0f%%)", formatRows(h.NDead), frac*100))
	}
	if last := h.LastVacuumed(); last != nil {
		parts = append(parts, "vac "+relativeAge(time.Since(*last)))
	} else if h.NLive+h.NDead > 0 {
		parts = append(parts, "never vacuumed")
	}
	if last := h.LastAnalyzed(); last != nil {
		parts = append(parts, "ana "+relativeAge(time.Since(*last)))
	}
	return strings.Join(parts, " · ")
}
