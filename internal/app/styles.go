package app

import "github.com/charmbracelet/lipgloss"

var (
	styleTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	styleErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	styleOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleWarn   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	styleMuted  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	styleKey    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231"))
	styleBanner = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220"))
)

func helpLine(pairs ...string) string {
	var b []string
	for i := 0; i+1 < len(pairs); i += 2 {
		b = append(b, styleKey.Render(pairs[i])+" "+styleMuted.Render(pairs[i+1]))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, b...)
}
