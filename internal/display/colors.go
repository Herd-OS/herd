package display

import "github.com/charmbracelet/lipgloss"

var (
	Green  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	Red    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	Yellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	Blue   = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	Muted  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	Bold   = lipgloss.NewStyle().Bold(true)
)
