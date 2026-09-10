package tui

import (
	"encoding/binary"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"pgdu/internal/humanize"
	"pgdu/internal/pageinspect"
	"pgdu/internal/pg"
)

// walDetailRow is the payload of one levelWALBlockDetail line. The screen is
// a key/value dump split into sections (record, block, decoded tuple, hex,
// page image); section rows are inert headers, like the WAL overview's.
type walDetailRow struct {
	section bool
	key     string
	value   string
	// styled marks value as carrying its own lipgloss styling.
	styled bool
	// mark flags the page-image line pointer this record touched.
	mark bool
	// tuple, when non-nil, is the page-image line pointer behind this row.
	tuple *pg.HeapTuple
}

func walDetailSection(title string) item {
	return item{name: title, data: walDetailRow{section: true, key: title}}
}

func walDetailKV(key, value string) item {
	return item{name: key, detail: value, data: walDetailRow{key: key, value: value}}
}

func walDetailStyled(key, value string) item {
	return item{name: key, detail: value, data: walDetailRow{key: key, value: value, styled: true}}
}

// buildWALDetailItems turns a block payload into the detail screen's rows.
func buildWALDetailItems(d pg.WALBlockDetail) []item {
	mu := styleMuted.Render
	ref := d.Ref
	var out []item

	// --- record ---
	out = append(out, walDetailSection("record"))
	out = append(out, walDetailKV("lsn", ref.StartLSN+" → "+ref.EndLSN))
	out = append(out, walDetailKV("prev lsn", d.PrevLSN))
	xid := d.Xid
	if xid == "0" {
		xid = "0 (non-transactional)"
	}
	out = append(out, walDetailKV("xid", xid))
	out = append(out, walDetailKV("type", ref.Rmgr+"/"+ref.RecordType))
	if ref.Description != "" {
		out = append(out, walDetailKV("description", ref.Description))
	}
	out = append(out, walDetailKV("record size", fmt.Sprintf("%s total · %s main data",
		humanize.Bytes(int64(d.RecordLength)), humanize.Bytes(int64(d.MainDataLength)))))

	// --- block ---
	out = append(out, walDetailSection("block reference"))
	rel := ref.RelName
	if rel == "" {
		rel = fmt.Sprintf("relfilenode %d", ref.RelFileNode)
	}
	if ref.IsToast {
		rel += " (toast)"
	}
	db := ref.DBName
	if db == "" {
		db = fmt.Sprintf("oid %d", ref.RelDatabase)
	}
	out = append(out, walDetailKV("relation", rel+"  ·  db "+db))
	out = append(out, walDetailKV("page", fmt.Sprintf("%s fork, block %d  (block_id %d)", ref.ForkName(), ref.BlockNumber, ref.BlockID)))
	if d.RelOID != 0 {
		kind := walRelKindLabel(d.RelKind, d.RelAM)
		if len(d.Attrs) > 0 {
			kind += fmt.Sprintf("  ·  %d columns", len(d.Attrs))
		}
		out = append(out, walDetailKV("kind", kind))
	}
	payload := humanize.Bytes(int64(ref.BlockDataLength)) + " change data"
	if ref.FPILength > 0 {
		payload += fmt.Sprintf("  ·  %s full-page image", humanize.Bytes(int64(ref.FPILength)))
		if len(ref.FPIInfo) > 0 {
			payload += "  [" + strings.Join(ref.FPIInfo, ",") + "]"
		}
	} else {
		payload += "  ·  no full-page image"
	}
	out = append(out, walDetailKV("payload", payload))
	if d.DecodeNote != "" {
		note := styleBloat.Render(d.DecodeNote)
		if d.PageInspectMissing != nil && d.PageInspectMissing.Installable {
			note += "  " + mu("— press ") + styleBadge.Render("i") + mu(" to install pageinspect")
		}
		out = append(out, walDetailStyled("note", note))
	}

	// --- change data, decoded ---
	if len(d.BlockData) > 0 {
		tuples, note := decodeWALHeapPayload(d)
		if d.IsBtree() {
			tuples, note = decodeWALBtreePayload(d)
		}
		for i, t := range tuples {
			title := "new tuple (from change data)"
			if t.index {
				title = "new index entry (from change data)"
			}
			if len(tuples) > 1 {
				title = fmt.Sprintf("tuple %d of %d (from change data)", i+1, len(tuples))
			}
			out = append(out, walDetailSection(title))
			if t.index {
				out = append(out, walDetailKV("t_tid", t.tid))
				out = append(out, walDetailKV("t_info", t.info))
			} else {
				out = append(out, walDetailKV("t_infomask", pageinspect.InfomaskText(int32(t.infomask))))
				out = append(out, walDetailKV("t_infomask2", pageinspect.Infomask2Text(int32(t.infomask2))))
				out = append(out, walDetailKV("t_hoff", fmt.Sprintf("%d  ·  %s attribute data", t.hoff, humanize.Bytes(int64(len(t.data))))))
			}
			out = append(out, walDetailColumns(t.cols, t.complete)...)
		}
		if note != "" {
			out = append(out, walDetailStyled("note", mu(note)))
		}
		out = append(out, walDetailSection(fmt.Sprintf("change data · %s (hex)", humanize.Bytes(int64(len(d.BlockData))))))
		out = append(out, hexDumpItems(d.BlockData)...)
	}

	// --- page image ---
	if h := d.PageHeader; h != nil {
		out = append(out, walDetailSection("page image · "+humanize.Bytes(int64(len(d.FPIData)))))
		out = append(out, walDetailKV("page lsn", h.LSN+mu("  (contents include this change; the LSN is stamped after XLogInsert, so it still names the previous record)")))
		out = append(out, walDetailKV("layout", fmt.Sprintf("lower %d · upper %d · special %d · %s free · pagesize %d · version %d",
			h.Lower, h.Upper, h.Special, humanize.Bytes(int64(h.FreeBytes())), h.PageSize, h.Version)))
		out = append(out, walDetailKV("flags", pageFlagsText(h.Flags)))
		out = append(out, walDetailKV("checksum", fmt.Sprintf("0x%04x", uint16(h.Checksum))))
		if h.PruneXid != "" && h.PruneXid != "0" {
			out = append(out, walDetailKV("prune xid", h.PruneXid))
		}
		if prev, next, level, flags, ok := d.BtreeOpaque(); ok {
			out = append(out, walDetailKV("btree", fmt.Sprintf("level %d · prev %s · next %s · %s",
				level, btreeSibling(prev), btreeSibling(next), btreeFlagsText(flags))))
		}
		if len(d.IndexItems) > 0 {
			out = append(out, walDetailSection(fmt.Sprintf("index tuples · %d", len(d.IndexItems))))
			touched, hasTouched := walDescOffset(ref.Description)
			for i := range d.IndexItems {
				t := &d.IndexItems[i]
				out = append(out, walIndexItemRow(t, d.IndexCols, hasTouched && int32(touched) == t.ItemOffset))
			}
		}
		if len(d.PageItems) > 0 {
			live := 0
			for _, t := range d.PageItems {
				if t.LPFlags == pg.LPNormal {
					live++
				}
			}
			out = append(out, walDetailSection(fmt.Sprintf("line pointers · %d (%d normal)", len(d.PageItems), live)))
			touched, hasTouched := walDescOffset(ref.Description)
			for i := range d.PageItems {
				t := &d.PageItems[i]
				out = append(out, walPageItemRow(t, d.Attrs, hasTouched && int32(touched) == t.LP))
			}
		}
	} else if len(d.FPIData) > 0 {
		out = append(out, walDetailSection(fmt.Sprintf("page image · %s (hex, first %d bytes)", humanize.Bytes(int64(len(d.FPIData))), min(len(d.FPIData), walFPIHexCap))))
		out = append(out, hexDumpItems(d.FPIData[:min(len(d.FPIData), walFPIHexCap)])...)
	}
	return out
}

// walFPIHexCap bounds the raw dump of an undecodable page image: the header
// and the first line pointers are what's recognisable; 8 KiB of hex is not.
const walFPIHexCap = 512

func walRelKindLabel(kind, am string) string {
	var label string
	switch kind {
	case "r":
		label = "table"
	case "t":
		label = "toast table"
	case "m":
		label = "materialized view"
	case "p":
		label = "partitioned table"
	case "i":
		label = "index"
	case "I":
		label = "partitioned index"
	case "S":
		label = "sequence"
	default:
		label = "relkind " + kind
	}
	if am != "" {
		label += " (" + am + ")"
	}
	return label
}

// pageFlagsText names PageHeaderData.pd_flags bits (bufpage.h).
func pageFlagsText(f int32) string {
	var names []string
	if f&0x0001 != 0 {
		names = append(names, "HAS_FREE_LINES")
	}
	if f&0x0002 != 0 {
		names = append(names, "PAGE_FULL")
	}
	if f&0x0004 != 0 {
		names = append(names, "ALL_VISIBLE")
	}
	s := fmt.Sprintf("0x%04x", uint16(f))
	if len(names) > 0 {
		s += " · " + strings.Join(names, " ")
	}
	return s
}

// walDetailColumns renders decoded column values as one row per column.
func walDetailColumns(cols [][2]string, complete bool) []item {
	out := make([]item, 0, len(cols)+1)
	for _, c := range cols {
		val := c[1]
		if val == "NULL" {
			out = append(out, walDetailStyled("  "+c[0], styleMuted.Render("NULL")))
			continue
		}
		out = append(out, walDetailKV("  "+c[0], val))
	}
	if !complete {
		out = append(out, walDetailStyled("", styleMuted.Render("… remaining columns not decodable from the raw bytes")))
	}
	return out
}

// walPageItemRow renders one line pointer of the page image: its state, the
// tuple header, and — when the relation's layout is known — the decoded row.
func walPageItemRow(t *pg.HeapTuple, attrs []pg.WALHeapAttr, touched bool) item {
	dot, flag := lpFlagDecoration(t.LPFlags)
	key := fmt.Sprintf("lp %d", t.LP)
	var parts []string
	parts = append(parts, dot+" "+padRight(flag, 8))
	switch t.LPFlags {
	case pg.LPRedirect:
		parts = append(parts, fmt.Sprintf("→ lp %d", t.LPOff))
	case pg.LPNormal:
		parts = append(parts, padRight(humanize.Bytes(int64(t.LPLen)), 8))
		parts = append(parts, styleMuted.Render(fmt.Sprintf("xmin %s xmax %s", pageinspect.XidString(t.Xmin), pageinspect.XidString(t.Xmax))))
		if t.Ctid != nil {
			parts = append(parts, styleMuted.Render("ctid "+*t.Ctid))
		}
		if len(attrs) > 0 && t.Data != nil {
			cols, complete := decodeHeapTupleCols(t.Data, int(t.Infomask2&pg.HeapNattsMask2), bitsFromText(t.Bits, t.Infomask), attrs)
			if len(cols) > 0 {
				parts = append(parts, formatDecodedRow(cols, complete))
			}
		}
	}
	value := strings.Join(parts, "  ")
	if touched {
		value = styleSelected.Render("◀ this record") + "  " + value
	}
	return item{name: key, detail: value, data: walDetailRow{key: key, value: value, styled: true, mark: touched, tuple: t}}
}

// formatDecodedRow joins decoded columns as "(name=val, name=val)".
func formatDecodedRow(cols [][2]string, complete bool) string {
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		parts = append(parts, c[0]+"="+c[1])
	}
	s := "(" + strings.Join(parts, ", ")
	if !complete {
		s += ", …"
	}
	return s + ")"
}

// bitsFromText converts pageinspect's t_bits ("10110000", one char per
// attribute, 1 = not null) into the caller's null-bitmap form. nil means the
// tuple has no nulls (HEAP_HASNULL unset or no bitmap shipped).
func bitsFromText(bits *string, infomask int32) []byte {
	if bits == nil || infomask&pg.HeapHasNull == 0 {
		return nil
	}
	s := *bits
	out := make([]byte, (len(s)+7)/8)
	for i := 0; i < len(s); i++ {
		if s[i] == '1' {
			out[i/8] |= 1 << (i % 8)
		}
	}
	return out
}

// walDescOffset pulls the line-pointer offset out of a heap record description
// ("off: 15, flags: 0x08" in 17+, "off 15 flags 0x08" before). For an UPDATE
// that is the *old* tuple's offset on block 1 and the new tuple's on block 0
// ("new off: N"); the caller only uses it to mark a row, so the primary offset
// is good enough.
func walDescOffset(desc string) (int, bool) {
	m := reWALDescOff.FindStringSubmatch(desc)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

var (
	reWALDescOff   = regexp.MustCompile(`(?:^|[ ,])off:? (\d+)`)
	reWALDescFlags = regexp.MustCompile(`flags:? 0x([0-9A-Fa-f]+)`)
)

// walDecodedTuple is one heap tuple reconstructed from a record's change data.
type walDecodedTuple struct {
	infomask, infomask2 uint16
	hoff                uint8
	// index marks a B-tree IndexTuple instead of a heap tuple: tid/info then
	// describe its header and the infomask fields are unused.
	index    bool
	tid      string
	info     string
	data     []byte // attribute bytes (after t_hoff)
	cols     [][2]string
	complete bool
}

// Heap-rmgr flag bits that matter for reading an UPDATE's change data
// (heapam_xlog.h): with either set, the logged new tuple omits the bytes it
// shares with the old version, so it cannot be decoded standalone.
const (
	xlhUpdatePrefixFromOld = 0x04
	xlhUpdateSuffixFromOld = 0x08
)

// heapHeaderSize is SizeOfHeapHeader: the xl_heap_header (infomask2, infomask,
// hoff) that precedes tuple bytes in INSERT/UPDATE change data.
const heapHeaderSize = 5

// heapTupleHeaderSize is SizeofHeapTupleHeader — offsetof(t_bits); the fixed
// part of the on-page tuple header that the WAL omits.
const heapTupleHeaderSize = 23

// decodeWALHeapPayload reconstructs the tuple(s) a heap record's change data
// carries and decodes their columns against the relation's layout. Only the
// record types whose block data *is* tuple bytes are handled: Heap INSERT and
// UPDATE/HOT_UPDATE (block 0 = the new page) and Heap2 MULTI_INSERT (COPY,
// several tuples back to back). Anything else — DELETE, LOCK, PRUNE, VACUUM,
// index records — returns a note; the hex dump still follows.
func decodeWALHeapPayload(d pg.WALBlockDetail) ([]walDecodedTuple, string) {
	ref := d.Ref
	if !d.IsHeap() {
		return nil, ""
	}
	if len(d.Attrs) == 0 {
		return nil, "column layout unknown — tuple bytes shown raw"
	}
	base := strings.TrimSuffix(ref.RecordType, "+INIT")
	switch {
	case ref.Rmgr == "Heap" && base == "INSERT":
		t, ok := decodeWALHeapTuple(d.BlockData, d.Attrs)
		if !ok {
			return nil, "change data is not a recognisable heap tuple"
		}
		return []walDecodedTuple{t}, ""
	case ref.Rmgr == "Heap" && (base == "UPDATE" || base == "HOT_UPDATE"):
		if ref.BlockID != 0 {
			return nil, "old tuple's page — the change data carries no tuple bytes (the new version is on block 0)"
		}
		flags := walDescFlags(ref.Description)
		if flags&(xlhUpdatePrefixFromOld|xlhUpdateSuffixFromOld) != 0 {
			data := d.BlockData
			var pre, suf uint16
			if flags&xlhUpdatePrefixFromOld != 0 && len(data) >= 2 {
				pre = binary.LittleEndian.Uint16(data)
				data = data[2:]
			}
			if flags&xlhUpdateSuffixFromOld != 0 && len(data) >= 2 {
				suf = binary.LittleEndian.Uint16(data)
			}
			return nil, fmt.Sprintf("new tuple reuses %d prefix + %d suffix bytes of the old version (wal_compression of updates) — columns need the old tuple, bytes shown raw", pre, suf)
		}
		t, ok := decodeWALHeapTuple(d.BlockData, d.Attrs)
		if !ok {
			return nil, "change data is not a recognisable heap tuple"
		}
		return []walDecodedTuple{t}, ""
	case ref.Rmgr == "Heap2" && base == "MULTI_INSERT":
		return decodeWALMultiInsert(d.BlockData, d.Attrs)
	}
	return nil, ""
}

func walDescFlags(desc string) uint64 {
	m := reWALDescFlags.FindStringSubmatch(desc)
	if m == nil {
		return 0
	}
	v, _ := strconv.ParseUint(m[1], 16, 64)
	return v
}

// decodeWALHeapTuple reads an xl_heap_header followed by the tuple bytes from
// t_bits onward (heap_insert logs exactly that: the tuple minus its fixed
// 23-byte header), then decodes the attribute area.
func decodeWALHeapTuple(b []byte, attrs []pg.WALHeapAttr) (walDecodedTuple, bool) {
	if len(b) < heapHeaderSize {
		return walDecodedTuple{}, false
	}
	t := walDecodedTuple{
		infomask2: binary.LittleEndian.Uint16(b[0:2]),
		infomask:  binary.LittleEndian.Uint16(b[2:4]),
		hoff:      b[4],
	}
	bitmapLen := int(t.hoff) - heapTupleHeaderSize
	if bitmapLen < 0 || heapHeaderSize+bitmapLen > len(b) {
		return walDecodedTuple{}, false
	}
	var bits []byte
	if t.infomask&pg.HeapHasNull != 0 {
		bits = b[heapHeaderSize : heapHeaderSize+bitmapLen]
	}
	t.data = b[heapHeaderSize+bitmapLen:]
	t.cols, t.complete = decodeHeapTupleCols(t.data, int(t.infomask2&pg.HeapNattsMask2), bits, attrs)
	return t, true
}

// decodeWALMultiInsert walks Heap2/MULTI_INSERT change data: a sequence of
// xl_multi_insert_tuple {uint16 datalen, uint16 infomask2, uint16 infomask,
// uint8 hoff} headers, each SHORTALIGNed, followed by datalen tuple bytes
// (again from t_bits onward). The tuple count lives in the main data, which
// pg_walinspect doesn't expose per block, so the walk runs until the bytes
// run out.
func decodeWALMultiInsert(b []byte, attrs []pg.WALHeapAttr) ([]walDecodedTuple, string) {
	const hdr = 7
	var out []walDecodedTuple
	off := 0
	for off+hdr <= len(b) {
		off = (off + 1) &^ 1 // SHORTALIGN
		if off+hdr > len(b) {
			break
		}
		datalen := int(binary.LittleEndian.Uint16(b[off:]))
		t := walDecodedTuple{
			infomask2: binary.LittleEndian.Uint16(b[off+2:]),
			infomask:  binary.LittleEndian.Uint16(b[off+4:]),
			hoff:      b[off+6],
		}
		off += hdr
		if off+datalen > len(b) {
			break
		}
		tup := b[off : off+datalen]
		off += datalen
		bitmapLen := int(t.hoff) - heapTupleHeaderSize
		if bitmapLen < 0 || bitmapLen > len(tup) {
			break
		}
		var bits []byte
		if t.infomask&pg.HeapHasNull != 0 {
			bits = tup[:bitmapLen]
		}
		t.data = tup[bitmapLen:]
		t.cols, t.complete = decodeHeapTupleCols(t.data, int(t.infomask2&pg.HeapNattsMask2), bits, attrs)
		out = append(out, t)
		if len(out) >= walMultiInsertCap {
			return out, fmt.Sprintf("showing the first %d tuples of the multi-insert", walMultiInsertCap)
		}
	}
	if len(out) == 0 {
		return nil, "change data is not a recognisable multi-insert"
	}
	return out, ""
}

// walMultiInsertCap bounds how many COPY tuples the detail expands.
const walMultiInsertCap = 64

// decodeHeapTupleCols walks a heap tuple's attribute area (the bytes after
// t_hoff) with the relation's physical layout — the same fixed-width /
// varlena / alignment rules the index-key decoder implements, plus the null
// bitmap (bit i set = attribute i present) and dropped columns, which still
// occupy bytes but aren't shown. natts is the tuple's own attribute count:
// columns added to the table afterwards aren't stored and read as absent.
// complete is false when the walk stopped early (an out-of-line TOAST pointer
// of unknown shape, an unknown type) — later columns are then omitted.
func decodeHeapTupleCols(data []byte, natts int, bits []byte, attrs []pg.WALHeapAttr) ([][2]string, bool) {
	var cols [][2]string
	off := 0
	for i, a := range attrs {
		if i >= natts {
			if !a.Dropped {
				cols = append(cols, [2]string{a.Name, "NULL"})
			}
			continue
		}
		if bits != nil && (i/8 >= len(bits) || bits[i/8]&(1<<(i%8)) == 0) {
			if !a.Dropped {
				cols = append(cols, [2]string{a.Name, "NULL"})
			}
			continue
		}
		val, next, ok := decodeHeapAttr(data, off, a)
		if !ok {
			return cols, false
		}
		off = next
		if !a.Dropped {
			cols = append(cols, [2]string{a.Name, val})
		}
	}
	return cols, true
}

// decodeHeapAttr decodes one stored attribute, delegating to the index-key
// decoder and adding the case it deliberately refuses: an out-of-line TOAST
// pointer, which in a heap tuple is a fixed 18-byte varatt_external the walk
// can step over.
func decodeHeapAttr(b []byte, off int, a pg.WALHeapAttr) (string, int, bool) {
	if a.TypLen == -1 && off < len(b) && b[off] == 0x01 {
		// VARATT_IS_1B_E: 1-byte header + tag + payload sized by the tag.
		if off+1 >= len(b) {
			return "", 0, false
		}
		const vartagOndisk = 18
		if b[off+1] != vartagOndisk {
			return "", 0, false
		}
		end := off + 2 + 16 // sizeof(varatt_external)
		if end > len(b) {
			return "", 0, false
		}
		ext := b[off+2 : end]
		rawSize := binary.LittleEndian.Uint32(ext[0:4]) - 4 // va_rawsize includes the 4-byte header
		valueID := binary.LittleEndian.Uint32(ext[8:12])
		return fmt.Sprintf("<toasted, %s, chunk_id %d>", humanize.Bytes(int64(rawSize)), valueID), end, true
	}
	return pageinspect.DecodeIndexColumn(b, off, pg.IndexKeyColumn{
		TypLen: a.TypLen, TypAlign: a.TypAlign, TypName: a.TypName, TypCategory: a.TypCategory,
	})
}

// hexDumpItems renders bytes as classic 16-per-line hex + ASCII rows, one item
// per line so the list core scrolls them.
func hexDumpItems(b []byte) []item {
	out := make([]item, 0, (len(b)+15)/16)
	for off := 0; off < len(b); off += 16 {
		end := min(off+16, len(b))
		chunk := b[off:end]
		var hx strings.Builder
		for i := range 16 {
			if i == 8 {
				hx.WriteByte(' ')
			}
			if i < len(chunk) {
				fmt.Fprintf(&hx, "%02x ", chunk[i])
			} else {
				hx.WriteString("   ")
			}
		}
		ascii := make([]byte, len(chunk))
		for i, c := range chunk {
			if c >= 0x20 && c <= 0x7e {
				ascii[i] = c
			} else {
				ascii[i] = '.'
			}
		}
		key := fmt.Sprintf("%04x", off)
		value := hx.String() + " " + styleMuted.Render("|"+string(ascii)+"|")
		out = append(out, item{name: key, detail: value, data: walDetailRow{key: key, value: value, styled: true}})
	}
	return out
}

// btreeSibling renders a BTPageOpaque sibling link (P_NONE = 0 means none).
func btreeSibling(blk uint32) string {
	if blk == 0 {
		return "—"
	}
	return strconv.FormatUint(uint64(blk), 10)
}

// btreeFlagsText names BTPageOpaqueData.btpo_flags bits (nbtree.h).
func btreeFlagsText(f uint16) string {
	names := []struct {
		bit  uint16
		name string
	}{
		{0x0001, "LEAF"}, {0x0002, "ROOT"}, {0x0004, "DELETED"}, {0x0008, "META"},
		{0x0010, "HALF_DEAD"}, {0x0020, "SPLIT_END"}, {0x0040, "HAS_GARBAGE"},
		{0x0080, "INCOMPLETE_SPLIT"}, {0x0100, "HAS_FULLXID"},
	}
	var out []string
	for _, n := range names {
		if f&n.bit != 0 {
			out = append(out, n.name)
		}
	}
	if len(out) == 0 {
		return fmt.Sprintf("flags 0x%04x", f)
	}
	return strings.Join(out, " ")
}

// indexAttrs adapts the index's key layout to the heap column walker, so one
// decoder serves both tuple kinds. Non-key (INCLUDE) columns are stored like
// any other attribute.
func indexAttrs(cols []pg.IndexKeyColumn) []pg.WALHeapAttr {
	out := make([]pg.WALHeapAttr, len(cols))
	for i, c := range cols {
		out[i] = pg.WALHeapAttr{Attnum: c.Ordinal, Name: c.Def, TypLen: c.TypLen, TypAlign: c.TypAlign, TypName: c.TypName, TypCategory: c.TypCategory}
	}
	return out
}

// IndexTupleData t_info bits (itup.h).
const (
	indexSizeMask = 0x1fff
	indexVarMask  = 0x4000
	indexNullMask = 0x8000
	indexTupleHdr = 8 // sizeof(IndexTupleData): 6-byte t_tid + 2-byte t_info
)

// decodeWALBtreePayload reads the IndexTuple a B-tree insert logs as change
// data: t_tid (the heap row it points at), t_info (size + flags), an optional
// null bitmap MAXALIGNed, then the key attributes. Only plain inserts qualify;
// splits, deletes and vacuum records carry offsets/arrays, not a tuple.
func decodeWALBtreePayload(d pg.WALBlockDetail) ([]walDecodedTuple, string) {
	if d.Ref.Rmgr != "Btree" || !strings.HasPrefix(d.Ref.RecordType, "INSERT_") {
		return nil, ""
	}
	if len(d.IndexCols) == 0 {
		return nil, "index key layout unknown — tuple bytes shown raw"
	}
	b := d.BlockData
	// INSERT_POST (insert into a posting-list tuple) prefixes the tuple with
	// the uint16 posting offset the new heap tid lands at.
	if d.Ref.RecordType == "INSERT_POST" {
		if len(b) < 2 {
			return nil, "change data is shorter than a posting offset"
		}
		b = b[2:]
	}
	if len(b) < indexTupleHdr {
		return nil, "change data is shorter than an index tuple header"
	}
	blk := uint32(binary.LittleEndian.Uint16(b[0:2]))<<16 | uint32(binary.LittleEndian.Uint16(b[2:4]))
	off := binary.LittleEndian.Uint16(b[4:6])
	info := binary.LittleEndian.Uint16(b[6:8])
	size := int(info & indexSizeMask)
	if size < indexTupleHdr || size > len(b) {
		return nil, fmt.Sprintf("t_info claims a %d-byte tuple in %d bytes of change data — not a plain index tuple, bytes shown raw", size, len(b))
	}
	t := walDecodedTuple{index: true, tid: fmt.Sprintf("(%d,%d)", blk, off)}
	var flags []string
	if info&indexNullMask != 0 {
		flags = append(flags, "has-nulls")
	}
	if info&indexVarMask != 0 {
		flags = append(flags, "has-varwidth")
	}
	t.info = fmt.Sprintf("0x%04x · %d bytes", info, size)
	if len(flags) > 0 {
		t.info += " · " + strings.Join(flags, " ")
	}
	natts := len(d.IndexCols)
	hoff := indexTupleHdr
	var bits []byte
	if info&indexNullMask != 0 {
		hoff = (indexTupleHdr + (natts+7)/8 + 7) &^ 7
		if hoff > size {
			return nil, "index tuple null bitmap overruns the tuple"
		}
		bits = b[indexTupleHdr : indexTupleHdr+(natts+7)/8]
	}
	t.data = b[hoff:size]
	t.cols, t.complete = decodeHeapTupleCols(t.data, natts, bits, indexAttrs(d.IndexCols))
	return []walDecodedTuple{t}, ""
}

// walIndexItemRow renders one bt_page_items entry of a B-tree page image:
// offset, the heap tid it points at, length, dead flag and the decoded key.
func walIndexItemRow(t *pg.IndexTuple, cols []pg.IndexKeyColumn, touched bool) item {
	key := fmt.Sprintf("off %d", t.ItemOffset)
	var parts []string
	if t.Dead {
		parts = append(parts, styleLPDead.Render("● dead  "))
	} else {
		parts = append(parts, styleLPNormal.Render("● live  "))
	}
	parts = append(parts, padRight(humanize.Bytes(int64(t.ItemLen)), 8))
	if t.Ctid != nil {
		parts = append(parts, styleMuted.Render("→ heap "+*t.Ctid))
	}
	if t.Data != nil && len(cols) > 0 {
		if k, ok := pageinspect.DecodeIndexKey(*t.Data, cols); ok {
			parts = append(parts, k)
		}
	}
	value := strings.Join(parts, "  ")
	if touched {
		value = styleSelected.Render("◀ this record") + "  " + value
	}
	return item{name: key, detail: value, data: walDetailRow{key: key, value: value, styled: true, mark: touched}}
}
