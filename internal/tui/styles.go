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

	// Server-memory bar: shared_buffers free pages, the kernel/app "other
	// used" portion, and the reclaimable kernel page cache. Chosen to read
	// distinctly from the per-table palette and from each other.
	styleSBFree    = lipgloss.NewStyle().Foreground(lipgloss.Color("67"))  // steel blue
	styleOtherUsed = lipgloss.NewStyle().Foreground(lipgloss.Color("173")) // warm orange
	styleCache     = lipgloss.NewStyle().Foreground(lipgloss.Color("108")) // sage green — "kinda used"

	// Segment colors for the table-row bar. Heap reuses the default bar tint
	// so the colour palette doesn't bloom; index and toast get distinct hues.
	styleHeapSeg  = styleBar
	styleIndexSeg = lipgloss.NewStyle().Foreground(lipgloss.Color("114")) // soft green
	styleToastSeg = lipgloss.NewStyle().Foreground(lipgloss.Color("231")) // white

	// Page-inspector colours. Distinct from the bar segment colours above so
	// the H/T flag glyphs read as overlays, not part of the page-fill bar.
	styleHeapHot      = lipgloss.NewStyle().Foreground(lipgloss.Color("213")) // magenta
	styleHeapToastTag = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // toast yellow

	// LP-flag dot colours for the per-tuple view. NORMAL/REDIRECT/DEAD/UNUSED
	// pair with the four lp_flags values from itemid.h.
	styleLPNormal   = lipgloss.NewStyle().Foreground(colorOK)
	styleLPRedirect = lipgloss.NewStyle().Foreground(colorAccent)
	styleLPDead     = lipgloss.NewStyle().Foreground(colorBloat)
	styleLPUnused   = lipgloss.NewStyle().Foreground(colorMuted)

	// bufferSlicePalette is the set of distinct foreground colours used to
	// paint per-table slices in the shared_buffers occupancy bar and the
	// matching row bars in the list. Capped at 10 — tables ranked beyond
	// that fall back to the default bar colour rather than cycling palette
	// hues, which would otherwise re-use a colour for an unrelated table.
	bufferSlicePalette = []lipgloss.Style{
		lipgloss.NewStyle().Foreground(lipgloss.Color("33")),  // blue
		lipgloss.NewStyle().Foreground(lipgloss.Color("165")), // magenta
		lipgloss.NewStyle().Foreground(lipgloss.Color("208")), // orange
		lipgloss.NewStyle().Foreground(lipgloss.Color("99")),  // violet
		lipgloss.NewStyle().Foreground(lipgloss.Color("142")), // olive
		lipgloss.NewStyle().Foreground(lipgloss.Color("169")), // pink
		lipgloss.NewStyle().Foreground(lipgloss.Color("184")), // chartreuse
		lipgloss.NewStyle().Foreground(lipgloss.Color("105")), // lavender
		lipgloss.NewStyle().Foreground(lipgloss.Color("49")),  // spring green
		lipgloss.NewStyle().Foreground(lipgloss.Color("215")), // peach
	}
)

// bufferSliceStyle returns the palette colour for slice index i, cycling on
// overflow. Callers should still cap N to a sensible number so legends stay
// readable.
func bufferSliceStyle(i int) lipgloss.Style {
	return bufferSlicePalette[i%len(bufferSlicePalette)]
}

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
