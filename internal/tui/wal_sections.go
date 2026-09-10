package tui

import (
	"fmt"

	"pgdu/internal/pg"
)

// walSectionKind names an inert line of the WAL overview. The levelWAL screen
// stacks two tables — resource managers, then the same window per relation —
// so besides the data rows the list carries each table's Σ footer, the
// by-relation title and column header, and a note line for the relation scan's
// loading / failed / empty states. Enter and the cursor skip them (unlike the
// log groups pane's logSection headers, which fold on Enter).
type walSectionKind int

const (
	walRowBlank      walSectionKind = iota // spacer between the two tables
	walRowRmgrTotal                        // Σ over the resource-manager rows
	walRowRelTitle                         // " by relation " title with the window coverage
	walRowRelHeader                        // relation column header (carries the sort marks)
	walRowRelLoading                       // spinner: the block-reference scan is still running
	walRowRelError                         // the scan failed; text is the error
	walRowRelNote                          // muted note: no block refs, or unresolved names
	walRowRelTotal                         // Σ over the relation rows
)

// walSectionRow is the item.data payload of an inert levelWAL line. It has no
// name on purpose: a filter can never match it, and computeVisibleIndexes keeps
// it visible regardless so the two tables stay framed while narrowing.
type walSectionRow struct {
	kind walSectionKind
	text string // walRowRelError / walRowRelNote: what to print
}

func walSectionItem(kind walSectionKind) item {
	return item{data: walSectionRow{kind: kind}}
}

func walSectionText(kind walSectionKind, text string) item {
	return item{data: walSectionRow{kind: kind, text: text}}
}

// buildWALItems lays out the WAL overview from the screen's two breakdowns:
// the rmgr rows sorted by s.sort with their Σ, then the by-relation title,
// column header, the relation rows sorted the same way, and their Σ. While the
// relation scan is still running, failed, or found no block references, the
// second table is a single line under its title saying so.
func buildWALItems(s *screen) []item {
	w := &s.wal
	items := make([]item, 0, len(w.rmgrs)+len(w.rels)+8)
	for _, st := range w.rmgrs {
		items = append(items, walRmgrToItem(st))
	}
	sortItems(items, s.sort, s.sortDesc)
	if len(w.rmgrs) > 0 {
		items = append(items, walSectionItem(walRowRmgrTotal))
	}
	items = append(items, walSectionItem(walRowBlank), walSectionItem(walRowRelTitle))
	switch {
	case w.relsLoading:
		items = append(items, walSectionItem(walRowRelLoading))
	case w.relsErr != nil:
		items = append(items, walSectionText(walRowRelError, w.relsErr.Error()))
	case len(w.rels) == 0:
		items = append(items, walSectionText(walRowRelNote, "no block references in this window"))
	default:
		// Other databases' names are resolved through their own pools, so what
		// is left numeric is either dropped or in a database pgdu could not
		// connect to. Flag it so the numeric rows don't read as a bug.
		if n, db := walUnresolvedRels(w.rels); n > 0 {
			items = append(items, walSectionText(walRowRelNote,
				fmt.Sprintf("%d shown as relfilenode N — dropped since, or pgdu cannot connect to their db (e.g. %s)", n, db)))
		}
		items = append(items, walSectionItem(walRowRelHeader))
		rels := make([]item, 0, len(w.rels))
		for _, st := range w.rels {
			rels = append(rels, walRelStatToItem(st))
		}
		sortItems(rels, s.sort, s.sortDesc)
		items = append(items, rels...)
		items = append(items, walSectionItem(walRowRelTotal))
	}
	return items
}

// walUnresolvedRels counts the relations whose name did not resolve and names
// the database of the first one, to seed the note under the by-relation title.
// The db falls back to "<db>" when even that is unknown (shared catalog /
// dropped, reldatabase 0).
func walUnresolvedRels(rels []pg.WALRelStat) (n int, db string) {
	db = "<db>"
	for _, st := range rels {
		if st.RelName != "" {
			continue
		}
		if db == "<db>" && st.DBName != "" {
			db = st.DBName
		}
		n++
	}
	return n, db
}
