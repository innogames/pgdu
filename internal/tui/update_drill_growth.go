package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// pickGrowthWindow applies the highlighted sample as a window endpoint.
//
// It mirrors loadSelectedSnapshot's pairing idiom: with no start applied the
// pick spans pick → now; with one applied, picking another row spans the
// time-ordered range between the two. There is far less to it than the query
// version, because a size window has only one anchor — there is no in-memory
// baseline to stand in for @session and no reset to stand in for @reset.
func (m *Model) pickGrowthWindow(s *screen, cur item) tea.Cmd {
	gt := m.findLevel(levelGrowth)
	if gt == nil || cur.snapPath == "" {
		return nil
	}

	start, end := cur.snapPath, growthNow
	if anchor := gt.growth.basePath; anchor != "" && anchor != cur.snapPath && !s.growth.entry {
		start, end = anchor, cur.snapPath
	}
	if start == growthNow {
		// "now" alone is not a window: it needs a stored sample behind it.
		m.notice = "pick a saved sample as the window's start"
		return nil
	}

	// Both stored ends must describe this server and database, or the diff is
	// meaningless. The live anchor is exempt by definition.
	for _, p := range []string{start, end} {
		if p == growthNow {
			continue
		}
		meta, ok := sizeMetaByPath(s.growth.metas, p)
		if !ok {
			continue
		}
		if compatible, why := m.growthCompatible(s, meta); !compatible {
			m.notice = "sample is from a " + why + " — can't diff this window"
			return nil
		}
	}

	// Order by time, so the older end is always the baseline whichever row was
	// picked first.
	if m.growthTime(s, end).Before(m.growthTime(s, start)) {
		start, end = end, start
	}
	// A frozen range means the pick landed mid-window, so keep the browser open
	// for the user to adjust the other end.
	stay := end != growthNow
	return m.loadGrowthWindowCmd(s.db, start, end, stay)
}

// growthNowTime orders the live anchor after every stored sample, the way
// snapTime does for the query browser's @now.
var growthNowTime = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

func (m *Model) growthTime(s *screen, path string) time.Time {
	if path == growthNow {
		return growthNowTime
	}
	meta, _ := sizeMetaByPath(s.growth.metas, path)
	return meta.CapturedAt
}

// drillGrowth is Enter on the growth table: a roll-up row narrows the leaf list
// in place (structural, since a schema group has no text the / filter could
// match), and a leaf row opens the relation's disk parts.
func (m *Model) drillGrowth(s *screen, cur item) tea.Cmd {
	if s.growth.view.grouped() {
		s.growth.group = &growthFilter{
			view:    s.growth.view,
			key:     cur.growthKey,
			display: growthNarrowDisplay(cur),
		}
		s.growth.view = growthByTable
		m.rebuildGrowthItems(s)
		s.resetCursor()
		return nil
	}
	if !cur.hasChildren {
		return nil // a dropped relation has no parts left to open
	}

	// Resolve by name, never by the sample's stored OID: it is stale on a
	// dropped relation and ambiguous after a dump/restore. This reuses the
	// resolve that already backs the top-queries u jump.
	qualified := growthKeyRelation(cur.growthKey)
	if qualified == "" {
		return nil
	}
	m.stack = append(m.stack, &screen{
		level: levelParts, title: "disk", tool: toolDisk, db: s.db,
		loading: true, sort: sortBySize, sortDesc: sortBySize.defaultDesc(),
	})
	return m.resolveDiskTableCmd(s.db, qualified, false)
}

// growthNarrowDisplay is what the header calls the narrowing. item.name is the
// joined cell text, so its first field is the key column's display.
func growthNarrowDisplay(cur item) string {
	if f := strings.Fields(cur.name); len(f) > 0 {
		return f[0]
	}
	return cur.growthKey
}

// growthKeyRelation turns a leaf row's "database.schema.relname" key into the
// "schema.relname" the disk resolve expects. The three parts come from catalog
// identifiers joined in that order, so taking the last two from the right is
// unambiguous even when a database or schema name contains a dot.
func growthKeyRelation(key string) string {
	dot := strings.LastIndex(key, ".")
	if dot < 0 {
		return ""
	}
	rest := key[:dot]
	if d := strings.LastIndex(rest, "."); d >= 0 {
		return rest[d+1:] + "." + key[dot+1:]
	}
	return rest + "." + key[dot+1:]
}
