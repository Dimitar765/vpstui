package app

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	styleTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	styleErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	styleOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleWarn   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	styleMuted  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	styleKey    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231"))
	styleBanner = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220"))

	styleTableHeader   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("241"))
	styleTableSelected = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("236")).Foreground(lipgloss.Color("231"))
	styleComposeHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("147"))
)

// stateCell renders a container state with a colored status dot. The result
// stays within ctable's State column width for all docker states.
func stateCell(state string) string {
	switch state {
	case "running":
		return styleOK.Render("● " + state)
	case "exited":
		return styleMuted.Render("○ " + state)
	default:
		return styleWarn.Render("● " + state)
	}
}

// pctCell colorizes percentage strings by threshold: ≥90 err, ≥75 warn.
func pctCell(s string) string {
	f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "%"), 64)
	if err != nil {
		return s
	}
	switch {
	case f >= 90:
		return styleErr.Render(s)
	case f >= 75:
		return styleWarn.Render(s)
	}
	return s
}

func lipglossActiveTab() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Reverse(true).Padding(0, 1)
}

func helpLine(pairs ...string) string {
	var b []string
	for i := 0; i+1 < len(pairs); i += 2 {
		b = append(b, styleKey.Render(pairs[i])+" "+styleMuted.Render(pairs[i+1]))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, b...)
}

// panel renders content inside a rounded frame with an optional title line.
func panel(title, content string, width int) string {
	block := content
	if title != "" {
		block = title + "\n" + strings.TrimRight(content, "\n")
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		// lipgloss Width excludes border and padding; shrink so the outer
		// frame lands exactly at the requested width.
		Width(max(width-4, 10)).
		Render(block)
}

// pinFooter places body at the top of a height-row frame and footer on its
// last row. If the body is taller than the frame the footer simply follows.
func pinFooter(body, footer string, height int) string {
	body = strings.TrimRight(body, "\n")
	footer = strings.TrimRight(footer, "\n")
	if footer == "" {
		return body
	}
	gap := height - lipgloss.Height(body) - lipgloss.Height(footer)
	if gap < 0 {
		gap = 0
	}
	return body + strings.Repeat("\n", gap+1) + footer
}
