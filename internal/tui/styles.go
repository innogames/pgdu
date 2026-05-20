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
)
