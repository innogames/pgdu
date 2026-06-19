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
	// heap and idx render as their own (bar-tinted) columns via hasBreakdown;
	// toast stays visible as the bar's white segment, so the detail line that
	// used to echo the heap/idx/toast figures would now just be redundant.
	return item{
		name: t.Name, size: t.TotalBytes, hasChildren: true,
		data: t,
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
	case pg.GistPageStat:
		return int64(p.Blkno), true
	case pg.BrinPageStat:
		return int64(p.Blkno), true
	case pg.GinPageStat:
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
	case pg.GistPageStat:
		return int64(p.FreeSize), true
	case pg.BrinPageStat:
		return int64(p.FreeSize), true
	case pg.GinPageStat:
		return int64(p.FreeSize), true
	}
	return 0, false
}

// itemLiveLP / itemRedirectLP / itemDeadLP extract a heap page's per-state
// line-pointer counts so each can be sorted on independently. Gated on the
// pg.HeapPageStat payload so rows from other levels sort to the bottom rather
// than tying at zero with a genuinely empty page.
func itemLiveLP(it item) (int64, bool) {
	p, ok := it.data.(pg.HeapPageStat)
	if !ok {
		return 0, false
	}
	return int64(p.LiveLP), true
}

func itemRedirectLP(it item) (int64, bool) {
	p, ok := it.data.(pg.HeapPageStat)
	if !ok {
		return 0, false
	}
	return int64(p.RedirectLP), true
}

func itemDeadLP(it item) (int64, bool) {
	p, ok := it.data.(pg.HeapPageStat)
	if !ok {
		return 0, false
	}
	return int64(p.DeadLP), true
}

// itemLP extracts the line-pointer index for heap-tuple items, or the
// itemoffset for index-tuple items (same concept — a per-page slot index).
func itemLP(it item) (int64, bool) {
	switch t := it.data.(type) {
	case pg.HeapTuple:
		return int64(t.LP), true
	case pg.IndexTuple:
		return int64(t.ItemOffset), true
	case pg.GistItem:
		return int64(t.ItemOffset), true
	case pg.BrinItem:
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

// itemPageType ranks a B-tree page by its type so sortByType groups leaf →
// internal → root → deleted pages together (the name tiebreaker then orders by
// block within a group). Second return is false for non-index-page items.
func itemPageType(it item) (int64, bool) {
	switch p := it.data.(type) {
	case pg.IndexPageStat:
		return int64(indexPageTypeRank(p.Type)), true
	case pg.GistPageStat:
		return int64(gistPageTypeRank(gistPageRole(p.IsLeaf, p.IsDeleted))), true
	case pg.BrinPageStat:
		return int64(brinPageTypeRank(p.PageType)), true
	case pg.GinPageStat:
		return int64(ginPageTypeRank(p.Flags)), true
	case pg.Relation:
		// Relations level: rank by kind so the "type" sort groups heap, toast,
		// then the index access methods together.
		return int64(relationTypeRank(p)), true
	case pg.Part:
		// Parts level: same idea — heap, toast, then index access methods.
		return int64(partTypeRank(p)), true
	}
	return 0, false
}

// partTypeRank orders the parts "type" column: heap first, then toast, then the
// index access methods alphabetically. Unknown access methods sort last among
// indexes; the name tiebreaker then orders within each group.
func partTypeRank(p pg.Part) int {
	switch p.Kind {
	case pg.PartHeap:
		return 0
	case pg.PartToast:
		return 1
	case pg.PartIndex:
		switch p.AccessMethod {
		case "btree":
			return 2
		case "brin":
			return 3
		case "gin":
			return 4
		case "gist":
			return 5
		case "hash":
			return 6
		case "spgist":
			return 7
		}
		return 8
	}
	return 9
}

// relationTypeRank orders the relations "type" column: heap first, then toast,
// then the index access methods alphabetically (btree, brin, gin, gist).
func relationTypeRank(r pg.Relation) int {
	switch r.Kind {
	case pg.RelToast:
		return 1
	case pg.RelBTreeIndex:
		return 2
	case pg.RelBrin:
		return 3
	case pg.RelGin:
		return 4
	case pg.RelGist:
		return 5
	default: // RelTable / heap
		return 0
	}
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

// --- GiST / BRIN / GIN item builders ---

func gistPageToItem(p pg.GistPageStat) item {
	used := max(heapPageBlockSize-int64(p.FreeSize), 0)
	return item{
		name:        fmt.Sprintf("page #%07d", p.Blkno),
		size:        used,
		hasChildren: p.Items > 0 && !p.IsDeleted,
		data:        p,
	}
}

func gistItemToItem(it pg.GistItem) item {
	// name carries the decoded keys so the `/` filter searches by key. The "+"
	// marker is approximate (the builder can't see the page role): a live ctid
	// is a heap pointer on a leaf or a downlink on an internal page — both drill.
	name := fmt.Sprintf("#%04d", it.ItemOffset)
	if it.Keys != nil && *it.Keys != "" {
		name = *it.Keys
	}
	return item{
		name:        name,
		size:        int64(it.ItemLen),
		hasChildren: it.Ctid != nil && !it.Dead,
		data:        it,
	}
}

func brinPageToItem(p pg.BrinPageStat) item {
	used := max(heapPageBlockSize-int64(p.FreeSize), 0)
	return item{
		name: fmt.Sprintf("page #%07d", p.Blkno),
		size: used,
		// Only regular pages carry range summaries to drill into; Enter on one
		// jumps to the heap pages of the summarised block range.
		hasChildren: p.PageType == "regular",
		data:        p,
	}
}

func brinItemToItem(it pg.BrinItem) item {
	val := ""
	if it.Value != nil {
		val = *it.Value
	}
	return item{
		// name carries "blk N attM value" so the `/` filter matches block ranges
		// and summary values alike.
		name:        fmt.Sprintf("blk %d att%d %s", it.BlockNum, it.AttNum, val),
		size:        it.BlockNum, // ranges sort by starting block under sortBySize
		hasChildren: true,        // Enter → heap pages of the range
		data:        it,
	}
}

func ginPageToItem(p pg.GinPageStat) item {
	used := max(heapPageBlockSize-int64(p.FreeSize), 0)
	return item{
		name: fmt.Sprintf("page #%07d", p.Blkno),
		size: used,
		// Only compressed data-leaf pages are itemizable by pageinspect.
		hasChildren: ginPageIsDataLeaf(p.Flags),
		data:        p,
	}
}

func ginItemToItem(it pg.GinItem) item {
	return item{
		// Posting-list segments are terminal (the TIDs are heap pointers, not a
		// single row to open). name carries the first tid for the `/` filter.
		name: fmt.Sprintf("%s ×%d", it.FirstTid, it.TidCount),
		size: int64(it.NBytes),
		data: it,
	}
}

// gistPageRole maps gist_page_opaque_info flags to the short role tag carried in
// screen.indexPageType and shown in the page column: del / leaf / intr.
func gistPageRole(isLeaf, isDeleted bool) string {
	switch {
	case isDeleted:
		return "del"
	case isLeaf:
		return "leaf"
	default:
		return "intr"
	}
}

// gistPageTypeRank / brinPageTypeRank / ginPageTypeRank order the per-AM page
// lists under the "type" sort (sortByType).
func gistPageTypeRank(role string) int {
	switch role {
	case "leaf":
		return 0
	case "intr":
		return 1
	case "del":
		return 2
	}
	return 3
}

func brinPageTypeRank(t string) int {
	switch t {
	case "meta":
		return 0
	case "revmap":
		return 1
	case "regular":
		return 2
	}
	return 3
}

// ginPageIsDataLeaf reports whether a GIN page's opaque flags mark it a
// compressed posting-list data-leaf page — the only kind gin_leafpage_items can
// read.
func ginPageIsDataLeaf(flags string) bool {
	return strings.Contains(flags, "data") && strings.Contains(flags, "leaf")
}

// ginPageTypeRank ranks GIN pages for the "type" sort. Data-leaf pages rank
// first because they're the only itemizable kind — an ascending type sort then
// surfaces the drillable pages at the top of the (often vast) entry-page list.
func ginPageTypeRank(flags string) int {
	switch {
	case strings.Contains(flags, "data") && strings.Contains(flags, "leaf"):
		return 0
	case strings.Contains(flags, "data"):
		return 1
	case strings.Contains(flags, "meta"):
		return 2
	default: // entry-tree pages
		return 3
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

// walRelStatToItem builds one levelWALRelations row. size is the combined byte
// total (record data + FPI) so the bar scales each relation against its
// siblings — the busiest relation tops the list ("what caused the change").
// hasChildren is true when any record touched it, so it drills to that
// relation's block references across the window.
func walRelStatToItem(st pg.WALRelStat) item {
	name := fmt.Sprintf("relfilenode %d", st.RelFileNode)
	if st.RelName != "" {
		name = st.RelName
	}
	if st.IsToast {
		name += " (toast)"
	}
	return item{
		name:        name,
		size:        st.CombinedSize(),
		hasChildren: st.RecCount > 0,
		data:        st,
	}
}

// itemWALCount / itemWALFPI extract the record count and FPI bytes from a
// levelWAL rmgr-stat or levelWALRelations relation-stat item. Second return is
// false for items without that payload so they sort below rows we can rank.
func itemWALCount(it item) (int64, bool) {
	switch v := it.data.(type) {
	case pg.WALRmgrStat:
		return v.Count, true
	case pg.WALRelStat:
		return v.RecCount, true
	}
	return 0, false
}

func itemWALFPI(it item) (int64, bool) {
	switch v := it.data.(type) {
	case pg.WALRmgrStat:
		return v.FPISize, true
	case pg.WALRecord:
		return int64(v.FPILength), true
	case pg.WALRelStat:
		return v.FPIBytes, true
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
	// The kind ("heap"/"toast"/"btree"/…) now lives in its own type column, so
	// detail carries only the remaining, kind-specific metadata: dead/vacuum
	// stats for the heap, primary/unique flags for an index, the underlying
	// pg_toast relname for toast.
	detail := ""
	switch p.Kind {
	case pg.PartHeap:
		detail = heapDetail(p.HeapStats)
	case pg.PartToast:
		detail = p.ToastName
	case pg.PartIndex:
		var tags []string
		if p.IsPrimary {
			tags = append(tags, "primary")
		}
		if p.IsUnique && !p.IsPrimary {
			tags = append(tags, "unique")
		}
		detail = strings.Join(tags, " · ")
	}
	return item{
		name:        p.Name,
		size:        p.SizeBytes,
		bloat:       p.WastedBytes,
		hasBloat:    p.HasBloat,
		hasChildren: p.Kind == pg.PartHeap, // only heap drills into per-column view
		detail:      detail,
		typeTag:     partTypeLabel(p),
		typeStyle:   partTypeStyle(p),
		data:        p,
	}
}

// heapDetail builds the inline status string shown on the heap row at the
// parts level: dead-tuple % and "last vacuum" age. The kind itself is carried
// by the type column, so this returns "" when no stats are available (e.g.
// matviews or stats never collected) rather than echoing the kind.
func heapDetail(h *pg.HeapStats) string {
	if h == nil {
		return ""
	}
	var parts []string
	if frac := h.DeadFrac(); frac >= 0 && h.NDead > 0 {
		parts = append(parts, fmt.Sprintf("%s dead (%.0f%%)", formatRows(h.NDead), frac*100))
	}
	if last := h.LastVacuumed(); last != nil {
		parts = append(parts, "vacuum "+relativeAge(time.Since(*last)))
	} else if h.NLive+h.NDead > 0 {
		parts = append(parts, "never vacuumed")
	}
	if last := h.LastAnalyzed(); last != nil {
		parts = append(parts, "analyze "+relativeAge(time.Since(*last)))
	}
	return strings.Join(parts, " · ")
}
