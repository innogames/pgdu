package tui

import (
	"strings"
)

// The ? reference for the disk-usage chain. Every other tool has had one; this
// one is the flagship view and was the last without, which showed: the size
// bar's segments, the gap between the breakdown columns and the total, and the
// two ways a bloat cell can be empty are all things the screen cannot say for
// itself.

// renderDiskInfo dispatches to the reference for the disk level in hand. The
// four screens share a tool and a bar, so they share a file and the sections
// that explain the bar.
func (m *Model) renderDiskInfo(l level, height int) string {
	var b strings.Builder
	switch l {
	case levelDatabases:
		m.diskDatabasesInfo(&b)
	case levelSchemas:
		m.diskSchemasInfo(&b)
	case levelTables:
		m.diskTablesInfo(&b)
	case levelParts:
		m.diskPartsInfo(&b)
	case levelColumns:
		m.diskColumnsInfo(&b)
	}
	return padInfo(&b, height)
}

func (m *Model) diskDatabasesInfo(b *strings.Builder) {
	mu := styleMuted.Render
	infoHeader(b, "Databases reference")
	b.WriteString("  " + styleHeader.Render(" what this lists ") + "\n")
	b.WriteString("    " + mu("Every database this role may CONNECT to, sized by pg_database_size — the whole directory,") + "\n")
	b.WriteString("    " + mu("including the tables, indexes, TOAST and free space inside it. Template databases are left out.") + "\n")
	b.WriteString("    " + mu("A database you cannot connect to is not listed at all, so the bars do not add up to the") + "\n")
	b.WriteString("    " + mu("cluster's size on disk. Nor would they anyway: pg_wal, the log directory and any temp-file") + "\n")
	b.WriteString("    " + mu("spill live outside every database.") + "\n")
	b.WriteString("    " + mu("The ") + styleBadge.Render("disk") + mu(" figure on the status row is the other side of that: how full the filesystem under") + "\n")
	b.WriteString("    " + mu("the data directory actually is. It appears only when pgdu runs on the server (see the parts") + "\n")
	b.WriteString("    " + mu("reference), and it covers that one filesystem — not a tablespace elsewhere.") + "\n\n")
	diskBarSection(b)
	b.WriteString("  " + styleHeader.Render(" keys ") + "\n")
	b.WriteString("    " + styleBadge.Render("↵") + mu("  open the database's schemas. A cluster with exactly one connectable database") + "\n")
	b.WriteString("    " + mu("skips this screen on the way in, and esc then leaves the tool rather than coming back to it.") + "\n")
}

func (m *Model) diskSchemasInfo(b *strings.Builder) {
	mu := styleMuted.Render
	infoHeader(b, "Schemas reference")
	b.WriteString("  " + styleHeader.Render(" columns ") + "\n")
	col := func(name, desc string) { b.WriteString("    " + padRight(name, 9) + mu(desc) + "\n") }
	col("size", "every table in the schema with its indexes and TOAST (Σ pg_total_relation_size)")
	col("tables", "ordinary, partitioned and materialized tables (relkind r/p/m)")
	b.WriteString("\n")
	b.WriteString("  " + styleHeader.Render(" what is not counted ") + "\n")
	b.WriteString("    " + mu("Sequences and foreign tables are outside the count and therefore outside the size, and a") + "\n")
	b.WriteString("    " + mu("schema holding nothing but those is not listed at all — every tool below here works on tables,") + "\n")
	b.WriteString("    " + mu("so such a schema is a dead end. Their bytes are small; the omission matters only if you are") + "\n")
	b.WriteString("    " + mu("reconciling against pg_database_size, which counts them.") + "\n\n")
	b.WriteString("  " + styleHeader.Render(" keys ") + "\n")
	b.WriteString("    " + styleBadge.Render("↵") + mu("  open the schema's tables. A database with one non-system schema skips this screen.") + "\n")
	b.WriteString("    " + styleBadge.Render("h") + mu("  size history for this database — what is growing, rather than what is big") + "\n")
}

func (m *Model) diskTablesInfo(b *strings.Builder) {
	mu := styleMuted.Render
	infoHeader(b, "Tables reference")
	b.WriteString("  " + styleHeader.Render(" columns ") + "  " +
		mu("all sortable — ") + styleBadge.Render("←") + mu("/") + styleBadge.Render("→") +
		mu(" switch the column, ") + styleBadge.Render("r") + mu(" reverses") + "\n")
	col := func(name, desc string) { b.WriteString("    " + padRight(name, 9) + mu(desc) + "\n") }
	col("size", "the whole relation: heap, indexes, TOAST and the maps (pg_total_relation_size)")
	col("heap", "the main fork only — the table's own rows, without its maps")
	col("idx", "every index on the table (pg_indexes_size)")
	col("toast", "the TOAST relation with its own index; ‘-’ when it exists but holds nothing")
	col("rows", "reltuples, the planner's estimate — ‘?’ until the table has been analyzed once")
	col("bloat", "estimated wasted bytes, filled in a moment after the list; see below")
	b.WriteString("\n")

	b.WriteString("  " + styleHeader.Render(" why heap + idx + toast ≠ size ") + "\n")
	b.WriteString("    " + mu("heap is the main fork alone. A table also carries a free-space map and a visibility map,") + "\n")
	b.WriteString("    " + mu("which size counts and no breakdown column does. The difference is the muted tail of the bar —") + "\n")
	b.WriteString("    " + mu("usually a rounding error, but a heavily-vacuumed table can carry a visible FSM.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" bloat ") + "  " + mu("an estimate here, measured one level down") + "\n")
	b.WriteString("    " + mu("The figure on this list comes from planner statistics alone — no pages are read — so it is an") + "\n")
	b.WriteString("    " + mu("order of magnitude and wears a ") + styleBadge.Render("~") + mu(". It is as good as the last ANALYZE. A blank cell means") + "\n")
	b.WriteString("    " + mu("the estimate had nothing to work from: a table never analyzed, or a partitioned parent, whose") + "\n")
	b.WriteString("    " + mu("storage is in its partitions. Drill in for the measured figure.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" partitions ") + "\n")
	b.WriteString("    " + mu("A partitioned table and each of its partitions are separate rows here, and the parent reads as") + "\n")
	b.WriteString("    " + mu("almost nothing because it has no storage of its own. The ") + styleBadge.Render("table_show_size") +
		mu(" diagnostic rolls a") + "\n")
	b.WriteString("    " + mu("partition tree up into one row.") + "\n\n")
	diskBarSection(b)
	b.WriteString("  " + styleHeader.Render(" keys ") + "\n")
	b.WriteString("    " + styleBadge.Render("↵") + mu("  the table's parts: its heap, its TOAST and each index separately, with bloat measured") + "\n")
	b.WriteString("    " + styleBadge.Render("d") + mu("  describe the table (\\d with statistics attached)") + "\n")
	b.WriteString("    " + styleBadge.Render("e") + mu("  export this table, filtered and sorted as shown, to CSV") + "\n")
	b.WriteString("    " + styleBadge.Render("h") + mu("  size history for this database: the same relations, but Δ bytes and bytes/day between") + "\n")
	b.WriteString("    " + mu("two captured samples. This screen answers what is big; that one answers what is growing.") + "\n")
}

func (m *Model) diskPartsInfo(b *strings.Builder) {
	mu := styleMuted.Render
	infoHeader(b, "Parts reference")
	b.WriteString("  " + styleHeader.Render(" what a part is ") + "\n")
	b.WriteString("    " + mu("One row per piece of storage the table owns: the heap, the TOAST relation (its own heap plus") + "\n")
	b.WriteString("    " + mu("its index), and every index. The sizes are the ones the tables list already read, so they do") + "\n")
	b.WriteString("    " + mu("not change on refresh — reopen the table to re-measure them.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" bloat ") + "  " + mu("measured on open, and not for every row") + "\n")
	b.WriteString("    " + mu("Where pgstattuple is installed the heap and TOAST are sampled with pgstattuple_approx, a btree") + "\n")
	b.WriteString("    " + mu("index is read with pgstatindex, and GIN, GiST and hash are walked with pgstattuple. Wasted") + "\n")
	b.WriteString("    " + mu("means dead entries plus free space — what a rewrite would hand back, not what VACUUM will.") + "\n")
	b.WriteString("    " + styleBadge.Render("~") + mu("  the figure came from planner statistics, not from reading pages: an order of magnitude.") + "\n")
	b.WriteString("    " + mu("blank  nothing was measured. BRIN and SP-GiST have no reader; TOAST without pgstattuple is") + "\n")
	b.WriteString("    " + mu("       opaque; anything over 50 GB, or slower than the per-part timeout, is skipped so one") + "\n")
	b.WriteString("    " + mu("       relation cannot stall the rest. The status row says how far the scan got.") + "\n")
	b.WriteString("    " + mu("A GIN index also shows its ") + styleBadge.Render("pending") + mu(" list: entries inserted but not yet merged in. Those are") + "\n")
	b.WriteString("    " + mu("live, not waste, which is why they sit in the detail column and not in bloat — but a list that") + "\n")
	b.WriteString("    " + mu("keeps growing means inserts are outrunning the merge and every lookup is paying for it.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" maintenance ") + "\n")
	b.WriteString("    " + mu("The panel below the list is quiet by default. A metric gets a bar row only once it is past") + "\n")
	b.WriteString("    " + mu("its own band; everything healthy folds into the header after a ") + styleBadge.Render("✓") +
		mu(", keeping its figure. A panel of two") + "\n")
	b.WriteString("    " + mu("lines is the good outcome, not a missing one, and the row that is coloured is the one to read.") + "\n")
	b.WriteString("    " + mu("Vacuum and analyze recency never earn a row: a static table nobody has vacuumed in a year is") + "\n")
	b.WriteString("    " + mu("not a fault, and the dead-row count says anything the clock could. A counter earns one at twice") + "\n")
	b.WriteString("    " + mu("this table's own autovacuum threshold — at the threshold a worker is simply due — or, for dead") + "\n")
	b.WriteString("    " + mu("rows, once they are a fifth of the table however the reloptions moved that threshold.") + "\n")
	b.WriteString("    " + mu("A transaction age at ") + styleBadge.Render("autovacuum_freeze_max_age") +
		mu(" is a routine anti-wraparound vacuum, not an emergency;") + "\n")
	b.WriteString("    " + mu("the freeze row appears at twice it, and its bar measures the way to the VACUUM failsafe, which") + "\n")
	b.WriteString("    " + mu("is where the danger actually is. The four statements below are in the footer on this screen.") + "\n")
	b.WriteString("    " + styleBadge.Render("v") + mu("  VACUUM (VERBOSE, ANALYZE, SKIP_LOCKED), with the server's notices streamed into a pane") + "\n")
	b.WriteString("    " + mu("   below. SKIP_LOCKED means a table someone else holds is passed over, not waited for.") + "\n")
	b.WriteString("    " + styleBadge.Render("F") + mu("  the same with FREEZE: freezes every tuple old enough rather than only those past") + "\n")
	b.WriteString("    " + mu("   vacuum_freeze_min_age, which is what actually advances relfrozenxid. Costs a full heap scan.") + "\n")
	b.WriteString("    " + styleBadge.Render("a") + mu("  ANALYZE VERBOSE alone — the vacuum's statistics half without its scan. Every estimate on") + "\n")
	b.WriteString("    " + mu("   these screens, including the tables list's bloat, is only as good as the last one.") + "\n")
	b.WriteString("    " + styleBadge.Render("R") + mu("  REINDEX TABLE CONCURRENTLY — every index in one pass, not gated on a bloat figure") + "\n")
	b.WriteString("    " + styleBadge.Render("↵") + mu("  on a btree index over 5 % measured bloat, arms REINDEX INDEX CONCURRENTLY (") +
		styleBadge.Render("y") + mu(" confirms).") + "\n")
	b.WriteString("    " + mu("   The offer is absent while the scan is still running, on an estimate, and on an index under") + "\n")
	b.WriteString("    " + mu("   the threshold — which is why it can look like it vanished.") + "\n")
	b.WriteString("    " + styleBadge.Render("p") + mu("  open the part in the page inspector") + "\n\n")
	b.WriteString("  " + styleHeader.Render(" reclaim ") + "\n")
	b.WriteString("    " + mu("The panel's reclaim row bars the measured waste against the table's total size and splits it") + "\n")
	b.WriteString("    " + mu("by what would hand it back. Index waste a") + "\n")
	b.WriteString("    " + mu("REINDEX returns to the filesystem. Heap waste VACUUM only makes reusable by the table — the") + "\n")
	b.WriteString("    " + mu("file does not shrink; that needs a rewrite (VACUUM FULL takes an exclusive lock, pg_repack") + "\n")
	b.WriteString("    " + mu("does not). pgdu runs neither, and the diagnostics only ever emit them as comments.") + "\n")
	b.WriteString("    " + styleBadge.Render("↵") + mu("  on the heap row, its columns") + "\n\n")
	b.WriteString("  " + styleHeader.Render(" free space ") + "\n")
	b.WriteString("    " + mu("Postgres has no free-space function, so the ") + styleBadge.Render("disk") +
		mu(" figure on the status row is a host read: it is") + "\n")
	b.WriteString("    " + mu("there only when pgdu runs on the database server itself and may read data_directory. It") + "\n")
	b.WriteString("    " + mu("measures that one filesystem — a tablespace or a relocated pg_wal sits on another.") + "\n")
	b.WriteString("    " + mu("A CONCURRENTLY rebuild builds a second copy of every index it touches and drops the old ones") + "\n")
	b.WriteString("    " + mu("only at the end, so when that copy would not fit in what is free, both ") + styleBadge.Render("R") +
		mu(" and ") + styleBadge.Render("↵") + mu(" refuse and say") + "\n")
	b.WriteString("    " + mu("so. Over half of it and they warn but still run. Where the disk cannot be read nothing is") + "\n")
	b.WriteString("    " + mu("refused: the figure is best-effort, and a key that dies on an unknown is worse than no gate.") + "\n")
}

func (m *Model) diskColumnsInfo(b *strings.Builder) {
	mu := styleMuted.Render
	infoHeader(b, "Columns reference")
	b.WriteString("  " + styleHeader.Render(" what the size means ") + "\n")
	b.WriteString("    " + mu("An estimate of the heap bytes each column occupies: average width × the non-null fraction ×") + "\n")
	b.WriteString("    " + mu("the row estimate. It is a share of the heap, not of the table — indexes and TOAST are not in it,") + "\n")
	b.WriteString("    " + mu("so the column sizes sum to less than the heap row one level up, and per-row overhead is in") + "\n")
	b.WriteString("    " + mu("neither. Accuracy is bounded by the last ANALYZE.") + "\n\n")
	b.WriteString("  " + styleHeader.Render(" widths ") + "\n")
	b.WriteString("    " + mu("pg_stats records only what a value occupies in the row, so an out-of-line value counts as its") + "\n")
	b.WriteString("    " + mu("18-byte pointer. pgdu corrects for that by sampling the table (TABLESAMPLE, up to 50 000 rows)") + "\n")
	b.WriteString("    " + mu("and measuring pg_column_size, so a fat bytea or jsonb column shows its real weight. When the") + "\n")
	b.WriteString("    " + mu("sample fails or comes back empty the pg_stats widths are used as they are.") + "\n\n")
	b.WriteString("  " + styleHeader.Render(" columns ") + "\n")
	col := func(name, desc string) { b.WriteString("    " + padRight(name, 9) + mu(desc) + "\n") }
	col("size", "the estimate above")
	col("avg", "bytes per non-null value")
	col("% null", "the null fraction — a wide, mostly-null column is the cheapest thing to drop")
	col("🍞", "the column is TOAST-able and wide enough that values are likely stored out of line")
}

// diskBarSection explains the segmented bar, which every size-bearing disk
// level draws the same way.
func diskBarSection(b *strings.Builder) {
	mu := styleMuted.Render
	b.WriteString("  " + styleHeader.Render(" the bar ") + "  " + mu("scaled to the largest row on screen, ncdu-style") + "\n")
	b.WriteString("    " + swatch(styleHeapSeg) + " " + mu("heap") + "    " +
		swatch(styleIndexSeg) + " " + mu("indexes") + "    " +
		swatch(styleToastSeg) + " " + mu("TOAST") + "\n")
	b.WriteString("    " + mu("The unfilled tail is the part of the total none of the three accounts for — the free-space and") + "\n")
	b.WriteString("    " + mu("visibility maps. A row's bar is its share of the biggest row, not of the parent, so the bars") + "\n")
	b.WriteString("    " + mu("compare rows with each other rather than summing to anything.") + "\n\n")
}

// diskInfoLevels is the set renderDiskInfo answers for; hasInfoOverlay must
// agree with it or ? opens an empty panel.
var diskInfoLevels = []level{levelDatabases, levelSchemas, levelTables, levelParts, levelColumns}
