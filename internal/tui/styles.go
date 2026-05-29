package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorBar    = lipgloss.Color("39")  // cyan-blue
	colorBloat  = lipgloss.Color("203") // red-orange
	colorMuted  = lipgloss.Color("244")
	colorAccent = lipgloss.Color("220") // yellow
	colorError  = lipgloss.Color("196")
	colorOK     = lipgloss.Color("114")

	styleHeader = lipgloss.NewStyle().
			Foreground(lipgloss.Color("231")).
			Background(lipgloss.Color("237")).
			Padding(0, 1).
			Bold(true)

	styleBreadcrumb  = lipgloss.NewStyle().Foreground(colorMuted)
	styleCrumbActive = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)

	styleHelp     = lipgloss.NewStyle().Foreground(colorMuted)
	styleErr      = lipgloss.NewStyle().Foreground(colorError).Bold(true)
	styleSelected = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleMuted    = lipgloss.NewStyle().Foreground(colorMuted)
	styleBar      = lipgloss.NewStyle().Foreground(colorBar)
	styleBloat    = lipgloss.NewStyle().Foreground(colorBloat)
	styleBadge    = lipgloss.NewStyle().Foreground(colorOK)
	styleBarAlt   = lipgloss.NewStyle().Foreground(colorAccent)

	// Segment colors for the table-row bar. Heap reuses the default bar tint
	// so the colour palette doesn't bloom; index and toast get distinct hues.
	styleHeapSeg  = styleBar
	styleIndexSeg = lipgloss.NewStyle().Foreground(lipgloss.Color("114")) // soft green
	styleToastSeg = lipgloss.NewStyle().Foreground(lipgloss.Color("231")) // white
)

// percentStyle picks a colour for a "higher is better" percentage value:
// green near 100, cyan in the healthy band, yellow as a warning, red below.
// Used for hit ratio, cached %, and shared_buffers occupancy so the eye
// can grade values without reading the digits.
func percentStyle(pct float64) lipgloss.Style {
	switch {
	case pct >= 99:
		return lipgloss.NewStyle().Foreground(colorOK)
	case pct >= 90:
		return lipgloss.NewStyle().Foreground(colorBar)
	case pct >= 70:
		return lipgloss.NewStyle().Foreground(colorAccent)
	default:
		return lipgloss.NewStyle().Foreground(colorBloat)
	}
}
