package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
)

// row is the shared visual primitive for every list level. It renders a
// fixed-width size bar with an optional bloat overlay, followed by the
// human-readable size, the row's name (highlighted when selected), and a
// trailing detail string.
type row struct {
	size     int64
	bloat    int64 // 0 when unknown or none
	maxSize  int64 // largest sibling, used to scale the bar
	name     string
	detail   string
	selected bool
}

const barWidth = 20

func renderRow(r row) string {
	bar := renderBar(r.size, r.bloat, r.maxSize, barWidth)
	sizeStr := humanize.Bytes(r.size)
	name := r.name
	if r.selected {
		name = styleSelected.Render(name)
	}
	detail := ""
	if r.detail != "" {
		detail = "  " + styleMuted.Render(r.detail)
	}
	cursor := "  "
	if r.selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
	}
	return cursor + bar + "  " + padRight(sizeStr, 10) + "  " + name + detail
}

// renderBar paints a fixed-width bar where the live portion is colorBar and
// the bloated portion is colorBloat at the tail. If size==0 we just emit an
// empty bar so layout stays stable.
func renderBar(size, bloat, max int64, width int) string {
	if max <= 0 {
		max = 1
	}
	filled := int(float64(width) * float64(size) / float64(max))
	if filled > width {
		filled = width
	}
	var bloatChars int
	if bloat > 0 && size > 0 {
		bloatChars = int(float64(filled) * float64(bloat) / float64(size))
		if bloatChars > filled {
			bloatChars = filled
		}
	}
	live := filled - bloatChars
	var b strings.Builder
	b.WriteString("[")
	b.WriteString(styleBar.Render(strings.Repeat("▇", live)))
	b.WriteString(styleBloat.Render(strings.Repeat("▇", bloatChars)))
	b.WriteString(styleMuted.Render(strings.Repeat("░", width-filled)))
	b.WriteString("]")
	return b.String()
}

func padRight(s string, n int) string {
	if lipgloss.Width(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-lipgloss.Width(s))
}
