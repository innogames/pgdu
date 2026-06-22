package tui

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// exportDoneMsg reports the outcome of writing the current screen to CSV.
type exportDoneMsg struct {
	path string
	rows int
	err  error
}

// exportCSVCmd serializes the table the screen currently shows to
// pgdu-<tool>-<datetime>.csv in the working directory. The header/row slices are
// built synchronously (while we're on the Bubble Tea goroutine and s.items is
// stable); only the file write happens in the returned command. Returns nil when
// the screen has nothing tabular to export, so the caller can show a hint instead.
func (m *Model) exportCSVCmd(s *screen) tea.Cmd {
	header, rows, ok := m.screenCSV(s)
	if !ok {
		return nil
	}
	// Write to the temp dir, not the CWD: under `sudo -iu postgres` the CWD is
	// the postgres home dir (often unwritable / a surprising place to land a
	// file), whereas the temp dir is world-writable and predictable. The notice
	// reports this absolute path so the user can find the file.
	name := filepath.Join(os.TempDir(), fmt.Sprintf("pgdu-%s-%s.csv", s.tool.Name(), time.Now().Format("20060102-150405")))
	return func() tea.Msg {
		f, err := os.Create(name)
		if err != nil {
			return exportDoneMsg{path: name, err: err}
		}
		w := csv.NewWriter(f)
		err = w.Write(header)
		if err == nil {
			err = w.WriteAll(rows) // WriteAll flushes the buffered writer
		}
		if err == nil {
			err = w.Error()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return exportDoneMsg{path: name, err: err}
		}
		return exportDoneMsg{path: name, rows: len(rows)}
	}
}

// screenCSV builds the header and rows for the table the current screen shows,
// in visible (filtered, sorted) order. ok is false for screens that aren't a
// browsable table — the tool picker, the diagnostic-query list, the statement
// detail panel, describe — where there's nothing tabular to write.
//
// Two paths: the generic diagnostic-table levels (levelDiagnosticResult,
// levelStatements) already carry a column schema in s.diagCols and per-row
// []pg.DiagCell, so they serialize uniformly; every other level maps its typed
// item.data via csvSchema.
func (m *Model) screenCSV(s *screen) ([]string, [][]string, bool) {
	vis := s.visibleIndexes()

	if s.diagCols != nil {
		header := make([]string, len(s.diagCols))
		for i, c := range s.diagCols {
			header[i] = c.Name
		}
		var rows [][]string
		for _, idx := range vis {
			cells, ok := s.items[idx].data.([]pg.DiagCell)
			if !ok {
				continue
			}
			row := make([]string, len(header))
			for i := range header {
				if i >= len(cells) {
					continue
				}
				// Numeric cells export their raw Num (machine-friendly), text
				// cells their Display string.
				if c := cells[i]; c.HasNum {
					row[i] = numStr(c.Num)
				} else {
					row[i] = c.Display
				}
			}
			rows = append(rows, row)
		}
		return header, rows, true
	}

	header, rowFn, ok := csvSchema(s.level)
	if !ok {
		return nil, nil, false
	}
	var rows [][]string
	for _, idx := range vis {
		if r := rowFn(s.items[idx]); r != nil {
			rows = append(rows, r)
		}
	}
	return header, rows, true
}

// csvSchema returns the column header and a row builder for one typed level.
// The row builder type-asserts item.data and returns nil when it doesn't match
// (so a stray row never breaks the export). Header and builder are declared
// together per case so the column order can't drift from the values.
func csvSchema(l level) (header []string, row func(it item) []string, ok bool) {
	switch l {
	case levelDatabases:
		return []string{"name", "size_bytes"},
			func(it item) []string {
				d, ok := it.data.(pg.Database)
				if !ok {
					return nil
				}
				return []string{d.Name, csvInt(d.SizeBytes)}
			}, true

	case levelSchemas:
		return []string{"database", "name", "size_bytes", "table_count"},
			func(it item) []string {
				sc, ok := it.data.(pg.Schema)
				if !ok {
					return nil
				}
				return []string{sc.DB, sc.Name, csvInt(sc.SizeBytes), csvInt(sc.TableCount)}
			}, true

	case levelTables:
		return []string{"schema", "name", "oid", "heap_bytes", "indexes_bytes", "toast_bytes", "total_bytes", "est_rows", "toast_name"},
			func(it item) []string {
				t, ok := it.data.(pg.Table)
				if !ok {
					return nil
				}
				return []string{t.Schema, t.Name, csvUint(t.OID), csvInt(t.HeapBytes), csvInt(t.IndexesBytes), csvInt(t.ToastBytes), csvInt(t.TotalBytes), csvInt(t.EstRows), t.ToastName}
			}, true

	case levelParts:
		return []string{"kind", "name", "oid", "size_bytes", "wasted_bytes", "has_bloat", "is_primary", "is_unique", "access_method", "n_live", "n_dead"},
			func(it item) []string {
				p, ok := it.data.(pg.Part)
				if !ok {
					return nil
				}
				nLive, nDead := "", ""
				if p.HeapStats != nil {
					nLive, nDead = csvInt(p.HeapStats.NLive), csvInt(p.HeapStats.NDead)
				}
				return []string{p.Kind.String(), p.Name, csvUint(p.OID), csvInt(p.SizeBytes), csvInt(p.WastedBytes), strconv.FormatBool(p.HasBloat), strconv.FormatBool(p.IsPrimary), strconv.FormatBool(p.IsUnique), p.AccessMethod, nLive, nDead}
			}, true

	case levelColumns:
		return []string{"name", "type", "avg_width", "null_frac", "est_bytes", "toastable"},
			func(it item) []string {
				c, ok := it.data.(pg.Column)
				if !ok {
					return nil
				}
				return []string{c.Name, c.Type, csvInt(c.AvgWidth), numStr(c.NullFrac), csvInt(c.EstBytes), strconv.FormatBool(c.Toastable)}
			}, true

	case levelBufferTables:
		return []string{"schema", "name", "oid", "buffered_bytes", "total_bytes", "hits", "reads", "hit_ratio"},
			func(it item) []string {
				st, ok := it.data.(pg.TableBufferStat)
				if !ok {
					return nil
				}
				return []string{st.Schema, st.Name, csvUint(st.OID), csvInt(st.BufferedBytes), csvInt(st.TotalBytes), csvInt(st.Hits), csvInt(st.Reads), numStr(st.HitRatio())}
			}, true

	case levelShmem:
		return []string{"name", "category", "off", "size_bytes", "allocated_bytes"},
			func(it item) []string {
				a, ok := it.data.(pg.ShmemAllocation)
				if !ok {
					return nil
				}
				off := ""
				if a.Off >= 0 {
					off = csvInt(a.Off)
				}
				return []string{shmemDisplayName(a), shmemCatOf(a).label(), off, csvInt(a.Size), csvInt(a.AllocatedSize)}
			}, true

	case levelRelations:
		return []string{"kind", "schema", "name", "oid", "size_bytes", "est_rows", "pages", "access_method", "parent_name"},
			func(it item) []string {
				r, ok := it.data.(pg.Relation)
				if !ok {
					return nil
				}
				return []string{relKindName(r.Kind), r.Schema, r.Name, csvUint(r.OID), csvInt(r.SizeBytes), csvInt(r.EstRows), csvInt(r.Pages), r.AccessMethod, r.ParentName}
			}, true

	case levelHeapPages:
		return []string{"blkno", "lsn", "lower", "upper", "special", "page_size", "flags", "free_bytes", "live_lp", "redirect_lp", "dead_lp", "unused_lp", "live_bytes", "dead_bytes", "hot_updated", "has_external"},
			func(it item) []string {
				p, ok := it.data.(pg.HeapPageStat)
				if !ok {
					return nil
				}
				return []string{csvInt(p.Blkno), p.LSN, csvInt(p.Lower), csvInt(p.Upper), csvInt(p.Special), csvInt(p.PageSize), csvInt(p.Flags), csvInt(p.FreeBytes), csvInt(p.LiveLP), csvInt(p.RedirectLP), csvInt(p.DeadLP), csvInt(p.UnusedLP), csvInt(p.LiveBytes), csvInt(p.DeadBytes), csvInt(p.HotUpdated), csvInt(p.HasExternal)}
			}, true

	case levelHeapTuples:
		return []string{"lp", "lp_off", "lp_flags", "lp_len", "xmin", "xmax", "ctid", "infomask", "infomask2", "hoff", "oid", "chunk_id", "chunk_seq"},
			func(it item) []string {
				t, ok := it.data.(pg.HeapTuple)
				if !ok {
					return nil
				}
				return []string{csvInt(t.LP), csvInt(t.LPOff), csvInt(t.LPFlags), csvInt(t.LPLen), csvUintP(t.Xmin), csvUintP(t.Xmax), csvStrP(t.Ctid), csvInt(t.Infomask), csvInt(t.Infomask2), csvIntP(t.Hoff), csvUintP(t.Oid), csvUintP(t.ChunkID), csvIntP(t.ChunkSeq)}
			}, true

	case levelTupleRow:
		return []string{"column", "value"},
			func(it item) []string {
				c, ok := it.data.(pg.TupleCell)
				if !ok {
					return nil
				}
				return []string{c.Name, csvStrP(c.Value)}
			}, true

	case levelIndexPages:
		return []string{"blkno", "type", "live_items", "dead_items", "avg_item_size", "page_size", "free_size", "btpo_prev", "btpo_next", "btpo_level", "btpo_flags"},
			func(it item) []string {
				p, ok := it.data.(pg.IndexPageStat)
				if !ok {
					return nil
				}
				return []string{csvInt(p.Blkno), p.Type, csvInt(p.LiveItems), csvInt(p.DeadItems), csvInt(p.AvgItemSize), csvInt(p.PageSize), csvInt(p.FreeSize), csvInt(p.BtpoPrev), csvInt(p.BtpoNext), csvInt(p.BtpoLevel), csvInt(p.BtpoFlags)}
			}, true

	case levelIndexTuples:
		return []string{"item_offset", "ctid", "item_len", "nulls", "vars", "data", "decoded"},
			func(it item) []string {
				t, ok := it.data.(pg.IndexTuple)
				if !ok {
					return nil
				}
				return []string{csvInt(t.ItemOffset), csvStrP(t.Ctid), csvInt(t.ItemLen), csvBoolP(t.Nulls), csvBoolP(t.Vars), csvStrP(t.Data), csvStrP(t.Decoded)}
			}, true

	case levelWAL:
		return []string{"rmgr", "count", "record_bytes", "fpi_bytes", "combined_bytes"},
			func(it item) []string {
				st, ok := it.data.(pg.WALRmgrStat)
				if !ok {
					return nil
				}
				return []string{st.Name, csvInt(st.Count), csvInt(st.RecordSize), csvInt(st.FPISize), csvInt(st.CombinedSize)}
			}, true

	case levelWALRecords:
		return []string{"start_lsn", "end_lsn", "prev_lsn", "xid", "rmgr", "record_type", "record_length", "main_data_length", "fpi_length", "description"},
			func(it item) []string {
				r, ok := it.data.(pg.WALRecord)
				if !ok {
					return nil
				}
				return []string{r.StartLSN, r.EndLSN, r.PrevLSN, r.Xid, r.Rmgr, r.RecordType, csvInt(r.RecordLength), csvInt(r.MainDataLength), csvInt(r.FPILength), r.Description}
			}, true

	case levelWALBlocks:
		return []string{"block_id", "rel_database", "rel_filenode", "rel_name", "fork", "block_number", "rmgr", "record_type", "block_data_length", "fpi_length", "is_toast", "db_name", "description"},
			func(it item) []string {
				b, ok := it.data.(pg.WALBlockRef)
				if !ok {
					return nil
				}
				return []string{csvInt(b.BlockID), csvUint(b.RelDatabase), csvUint(b.RelFileNode), b.RelName, b.ForkName(), csvInt(b.BlockNumber), b.Rmgr, b.RecordType, csvInt(b.BlockDataLength), csvInt(b.FPILength), strconv.FormatBool(b.IsToast), b.DBName, b.Description}
			}, true

	case levelStatementSamples:
		return []string{"relation", "column", "operator", "value", "position", "occurrences"},
			func(it item) []string {
				sm, ok := it.data.(pg.QualSample)
				if !ok {
					return nil
				}
				return []string{sm.Relation, sm.Column, sm.Operator, sm.ConstValue, strconv.Itoa(sm.Position), csvInt(sm.Occurrences)}
			}, true
	}
	return nil, nil, false
}

// relKindName maps a page-inspector relation kind to a stable CSV label.
func relKindName(k pg.RelationKind) string {
	switch k {
	case pg.RelTable:
		return "table"
	case pg.RelBTreeIndex:
		return "btree"
	case pg.RelGist:
		return "gist"
	case pg.RelBrin:
		return "brin"
	case pg.RelGin:
		return "gin"
	case pg.RelToast:
		return "toast"
	}
	return "?"
}

// numStr renders a raw float in its minimal decimal form: whole numbers come out
// without a fractional part ("12345678"), fractions keep their significant
// digits ("42.5"). Used so numeric CSV cells carry machine-friendly values
// rather than the humanized strings shown in the TUI.
func numStr(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func csvInt[T int | int32 | int64](n T) string { return strconv.FormatInt(int64(n), 10) }
func csvUint(n uint32) string                  { return strconv.FormatUint(uint64(n), 10) }

// Pointer-field helpers: empty string when the source value is SQL NULL.
func csvIntP(p *int32) string {
	if p == nil {
		return ""
	}
	return csvInt(*p)
}

func csvUintP(p *uint32) string {
	if p == nil {
		return ""
	}
	return csvUint(*p)
}

func csvStrP(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func csvBoolP(p *bool) string {
	if p == nil {
		return ""
	}
	return strconv.FormatBool(*p)
}
