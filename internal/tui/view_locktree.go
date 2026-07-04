package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/pg"
)

// lockTreeRow is one flattened node of the blocking forest: the backend plus its
// indent depth and whether it's a root (blocks others but waits on nobody).
type lockTreeRow struct {
	node  pg.LockNode
	depth int
	root  bool
}

// rebuildLockTreeItems assembles the blocking forest from s.lockNodes and
// flattens it into s.items in depth-first order (item.data = lockTreeRow). Roots
// are the backends that block someone but aren't themselves blocked; each node's
// children are the backends it directly blocks. A backend blocked by several
// others appears under each blocker (a lock wait can have multiple holders), so
// a visited set guards against the resulting cycles/duplication runaway while
// still showing every distinct edge once per path.
func (m *Model) rebuildLockTreeItems(s *screen) {
	s.items = s.items[:0]
	s.itemsRev++

	byPID := make(map[int32]pg.LockNode, len(s.lockNodes))
	blockedBy := make(map[int32][]int32) // pid → its blockers
	blocks := make(map[int32][]int32)    // pid → backends it blocks (children)
	for _, n := range s.lockNodes {
		byPID[n.PID] = n
		blockedBy[n.PID] = n.Blockers
		for _, b := range n.Blockers {
			blocks[b] = append(blocks[b], n.PID)
		}
	}

	// Roots: nodes that block someone but wait on nobody. A pure blocker may not
	// itself be a waiter, so it can be a root even with no blockers of its own.
	var roots []int32
	for pid := range byPID {
		if len(blockedBy[pid]) == 0 && len(blocks[pid]) > 0 {
			roots = append(roots, pid)
		}
	}
	// Deterministic order so the tree doesn't reshuffle every refresh.
	slices.Sort(roots)

	var walk func(pid int32, depth int, onPath map[int32]bool)
	walk = func(pid int32, depth int, onPath map[int32]bool) {
		n, ok := byPID[pid]
		if !ok || onPath[pid] {
			return
		}
		onPath[pid] = true
		s.items = append(s.items, item{
			name: lockRowFilterText(n),
			data: lockTreeRow{node: n, depth: depth, root: depth == 0},
		})
		children := append([]int32(nil), blocks[pid]...)
		slices.Sort(children)
		for _, c := range children {
			walk(c, depth+1, onPath)
		}
		delete(onPath, pid)
	}
	for _, r := range roots {
		walk(r, 0, map[int32]bool{})
	}

	// Defensive: if the graph is all cycles (no clean root), fall back to listing
	// every involved backend flat so nothing vanishes from the view.
	if len(s.items) == 0 && len(s.lockNodes) > 0 {
		nodes := append([]pg.LockNode(nil), s.lockNodes...)
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].PID < nodes[j].PID })
		for _, n := range nodes {
			s.items = append(s.items, item{name: lockRowFilterText(n), data: lockTreeRow{node: n}})
		}
	}
	s.clampCursor()
}

// lockRowFilterText is the fuzzy-filter target for a lock node: pid, user,
// database and query so / matches any of them.
func lockRowFilterText(n pg.LockNode) string {
	return fmt.Sprintf("%d %s %s %s", n.PID, n.Username, n.Database, n.Query)
}

// lockTreeSelectedPID returns the PID under the cursor, or 0 when the list is
// empty — the target for the k/x cancel/terminate confirm flow.
func lockTreeSelectedPID(s *screen) int32 {
	vis := s.visibleIndexes()
	if s.cursor < 0 || s.cursor >= len(vis) {
		return 0
	}
	if r, ok := s.items[vis[s.cursor]].data.(lockTreeRow); ok {
		return r.node.PID
	}
	return 0
}

func (m *Model) renderLockTree(s *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder

	if s.lockErr != nil {
		b.WriteString("  " + styleErr.Render("error: "+s.lockErr.Error()) + "\n")
		return padToHeight(&b, height, 1)
	}

	// Header line: count + refresh badge + the k/x action hints.
	waiters := 0
	for _, n := range s.lockNodes {
		if n.Waiting() {
			waiters++
		}
	}
	refresh := "off"
	if m.activityRefresh > 0 {
		refresh = m.activityRefresh.String()
	}
	b.WriteString("  " + styleSelected.Render("blocking chains") + mu(fmt.Sprintf("  ·  %d backends, %d waiting  ·  ⟳ %s  ·  ",
		len(s.lockNodes), waiters, refresh)) +
		styleBadge.Render("k") + mu(" cancel · ") + styleBadge.Render("x") + mu(" terminate · ") +
		styleBadge.Render("t") + mu(" cadence") + "\n")
	used := 1

	if banner := activityPendingBanner(s); banner != "" {
		b.WriteString(banner + "\n")
		used++
	}

	if len(s.items) == 0 {
		b.WriteString("  " + styleBadge.Render("no lock contention") + mu(" — every backend is running unblocked") + "\n")
		return padToHeight(&b, height, used+1)
	}

	vis := s.visibleIndexes()
	rowsH := max(height-used, 0)
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		row, _ := it.data.(lockTreeRow)
		b.WriteString(renderLockRow(row, vi == s.cursor, m.width) + "\n")
		used++
	}
	return padToHeight(&b, height, used)
}

// renderLockRow renders one backend as an indented tree node: pid · user@db ·
// state · xact age · the lock it waits on (for non-roots) · query snippet.
func renderLockRow(r lockTreeRow, selected bool, width int) string {
	mu := styleMuted.Render
	n := r.node

	indent := strings.Repeat("  ", r.depth)
	branch := ""
	if r.depth > 0 {
		branch = mu("└─ ")
	}
	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
	}

	pidStr := fmt.Sprintf("pid %d", n.PID)
	if selected {
		pidStr = styleSelected.Render(pidStr)
	} else if r.root {
		// The root holds the lock everyone waits on — highlight it.
		pidStr = styleBarAlt.Render(pidStr)
	}

	// State: idle-in-transaction roots are the classic culprit — paint red.
	stateStr := n.State
	if st, ok := stateStyle(n.State); ok {
		stateStr = st.Render(n.State)
	}

	var seg []string
	seg = append(seg, pidStr)
	who := n.Username
	if n.Database != "" {
		who += "@" + n.Database
	}
	if who != "" && who != "@" {
		seg = append(seg, mu(who))
	}
	if stateStr != "" {
		seg = append(seg, stateStr)
	}
	if n.XactAgeMs > 0 {
		seg = append(seg, durationStyle(n.XactAgeMs).Render("xact "+fmtAge(n.XactAgeMs)))
	}
	// What this backend is waiting on (only meaningful for blocked, non-root nodes).
	if n.Waiting() && (n.WaitMode != "" || n.WaitRelation != "") {
		wait := "waiting"
		if n.WaitMode != "" {
			wait += " " + n.WaitMode
		}
		if n.WaitRelation != "" {
			wait += " on " + n.WaitRelation
		} else if n.WaitLockType != "" {
			wait += " on " + n.WaitLockType
		}
		seg = append(seg, styleErr.Render(wait))
	}

	line := cursor + indent + branch + strings.Join(seg, mu(" · "))

	// Query snippet on the same line, clipped so the row never wraps.
	if n.Query != "" {
		q := "  " + mu(flattenQuery(n.Query))
		line += q
	}
	if width > 4 && lipgloss.Width(line) > width {
		line = truncateToWidth(line, width)
	}
	return line
}

// padToHeight writes blank lines until `used` reaches height, so the help row
// stays pinned to the bottom. Returns the accumulated string.
func padToHeight(b *strings.Builder, height, used int) string {
	for i := used; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}
