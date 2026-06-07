package tui

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// EXPLAIN (FORMAT TEXT) renders each plan node as
//
//	->  Node Type …  (cost=START..END rows=R width=W) (actual time=FIRST..LAST rows=R loops=N)
//
// reActual captures a node's per-loop end time and its loop count; the node's
// total (inclusive) time is LAST*loops. reCost captures the total (end) cost,
// used when the plan wasn't ANALYZEd. Both are anchored on the catalog-formatted
// metric blocks, so they never match user data in Output:/Filter: detail lines.
var (
	reActual = regexp.MustCompile(`\(actual time=[0-9.]+\.\.([0-9.]+) rows=[0-9.]+ loops=([0-9]+)\)`)
	reCost   = regexp.MustCompile(`\(cost=[0-9.]+\.\.([0-9.]+) rows=`)
)

// explainNode is one plan-tree node parsed out of the text-format EXPLAIN.
type explainNode struct {
	lineIdx   int
	depth     int     // leading-space count; root=0, deeper nodes strictly larger
	inclusive float64 // total time (LAST*loops) or total cost for this node + subtree
	self      float64 // inclusive minus the sum of direct children's inclusive
}

// parseExplainTree extracts the plan nodes from a text-format EXPLAIN and fills
// in each node's self (exclusive) time/cost by subtracting its direct children.
// A node line is any line carrying a "(cost=" block; Output:/Buffers:/Filter:
// detail lines are skipped. Children are nested by indentation, so CTE/SubPlan
// sub-trees are treated as ordinary descendants — close enough for grading.
func parseExplainTree(plan string, analyze bool) []explainNode {
	lines := strings.Split(plan, "\n")
	var nodes []explainNode
	// idxByLine maps a line index to its position in nodes, so children can fold
	// their inclusive value back into the parent for the self-time computation.
	idxByLine := make(map[int]int)
	var stack []int // indices into nodes, the current ancestor chain

	for i, line := range lines {
		if !strings.Contains(line, "(cost=") {
			continue
		}
		depth := len(line) - len(strings.TrimLeft(line, " "))
		inc := nodeInclusive(line, analyze)

		// Pop ancestors that are siblings or shallower; the remaining top is the
		// parent. Fold our inclusive value out of the parent's self.
		for len(stack) > 0 && nodes[stack[len(stack)-1]].depth >= depth {
			stack = stack[:len(stack)-1]
		}
		n := explainNode{lineIdx: i, depth: depth, inclusive: inc, self: inc}
		nodes = append(nodes, n)
		cur := len(nodes) - 1
		idxByLine[i] = cur
		if len(stack) > 0 {
			parent := stack[len(stack)-1]
			nodes[parent].self -= inc
		}
		stack = append(stack, cur)
	}
	return nodes
}

// nodeInclusive returns a node's total (inclusive) cost or time. "(never
// executed)" branches contribute 0. A line without the expected metric block
// (shouldn't happen for a "(cost=" line) also yields 0.
func nodeInclusive(line string, analyze bool) float64 {
	if analyze {
		m := reActual.FindStringSubmatch(line)
		if m == nil {
			return 0 // e.g. "(never executed)"
		}
		last, _ := strconv.ParseFloat(m[1], 64)
		loops, _ := strconv.ParseFloat(m[2], 64)
		return last * loops
	}
	m := reCost.FindStringSubmatch(line)
	if m == nil {
		return 0
	}
	end, _ := strconv.ParseFloat(m[1], 64)
	return end
}

// explainHeatStyle grades a node's share of the whole-query time/cost into a
// heat colour: hot nodes run red→orange→yellow, cheap nodes get a cool green so
// the eye can tell "fine" from "unpainted". The second return is always true —
// every graded node is coloured.
func explainHeatStyle(frac float64) (lipgloss.Style, bool) {
	switch {
	case frac >= 0.50:
		return lipgloss.NewStyle().Foreground(colorError).Bold(true), true
	case frac >= 0.25:
		return lipgloss.NewStyle().Foreground(colorBloat), true
	case frac >= 0.10:
		return lipgloss.NewStyle().Foreground(colorAccent), true
	default:
		return lipgloss.NewStyle().Foreground(colorOK), true
	}
}

// colorizeExplain renders a text-format EXPLAIN plan into ready-to-print lines:
// each is clipped to the detail width first (keeping clipDetail's rune-based
// truncation ANSI-safe), then the slow nodes get their (actual time=…)/(cost=…)
// metric heat-coloured by self-time share, with the single worst node's name
// bolded. When the plan has no positive root cost/time (trivial or wholly
// "never executed"), every line renders exactly as before.
func (m *Model) colorizeExplain(plan string, analyze bool) []string {
	lines := strings.Split(plan, "\n")
	nodes := parseExplainTree(plan, analyze)

	// nodes[0] is the root (outermost plan node); its inclusive value is the
	// whole-query denominator. worst is the highest-self node — the bottleneck.
	total, worst := 0.0, -1
	if len(nodes) > 0 {
		total = nodes[0].inclusive
	}
	for i, n := range nodes {
		if n.self > 0 && (worst < 0 || n.self > nodes[worst].self) {
			worst = i
		}
	}

	// style decision per source line index.
	type decision struct {
		style    lipgloss.Style
		boldName bool
		selfPct  float64 // node's self-time share of the whole query (analyze only)
	}
	decided := make(map[int]decision)
	if total > 0 {
		for i, n := range nodes {
			style, hot := explainHeatStyle(n.self / total)
			if !hot {
				continue
			}
			decided[n.lineIdx] = decision{style: style, boldName: i == worst, selfPct: n.self / total * 100}
		}
	}

	out := make([]string, 0, len(lines))
	for i, line := range lines {
		clipped := m.clipDetail(line)
		d, ok := decided[i]
		if !ok {
			out = append(out, clipped)
			continue
		}
		out = append(out, paintExplainLine(clipped, d.style, d.boldName, analyze, d.selfPct))
	}
	return out
}

// paintExplainLine wraps the metric block (and, for the bottleneck node, the
// node-type name) of one already-clipped plan line in the given heat style. It
// operates on plain text (no embedded ANSI), so substring math is safe; if the
// metric was clipped away the line is returned unchanged.
func paintExplainLine(line string, style lipgloss.Style, boldName, analyze bool, selfPct float64) string {
	bold := lipgloss.NewStyle().Bold(true)

	// Colour the node-type name first (offsets before the metric are unaffected
	// by appending colour later, since we rebuild left-to-right).
	if boldName {
		line = paintNodeName(line, bold)
	}

	marker := "(actual time="
	if !analyze {
		marker = "(cost="
	}
	start := strings.Index(line, marker)
	if start < 0 {
		return line
	}
	end := strings.IndexByte(line[start:], ')')
	if end < 0 {
		return line
	}
	end += start + 1 // include the ')'
	painted := line[:start] + style.Render(line[start:end])
	// For ANALYZE plans, annotate the node's self-time share right after the
	// metric block so the hotspot reads quantitatively, not just by colour.
	if analyze {
		painted += " " + style.Render(fmt1(selfPct)+"%")
	}
	return painted + line[end:]
}

// paintNodeName styles the node-type label — the text between the optional
// "->  " marker (or line start) and the first "  (cost"/"  (actual" metric.
func paintNodeName(line string, style lipgloss.Style) string {
	nameStart := 0
	if arrow := strings.Index(line, "->  "); arrow >= 0 {
		nameStart = arrow + len("->  ")
	} else {
		nameStart = len(line) - len(strings.TrimLeft(line, " "))
	}
	nameEnd := strings.Index(line, "  (cost=")
	if nameEnd <= nameStart {
		return line
	}
	return line[:nameStart] + style.Render(line[nameStart:nameEnd]) + line[nameEnd:]
}
