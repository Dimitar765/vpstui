package app

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"vpstui/internal/perf"
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
	styleAuth          = lipgloss.NewStyle().Foreground(lipgloss.Color("110"))
)

// stateCell renders a container state with a colored status dot. The result
// stays within ctable's State column width for all docker states.
func stateCell(state string) string {
	switch state {
	case "":
		return ""
	case "running":
		return styleOK.Render("● " + state)
	case "exited":
		return styleMuted.Render("○ " + state)
	default:
		return styleWarn.Render("● " + state)
	}
}

// sevCell colors an aggregated severity exactly as the log view colors a
// matching line, so red means the same thing on both screens.
func sevCell(s string) string {
	switch s {
	case "error":
		return styleErr.Render(s)
	case "warn":
		return styleWarn.Render(s)
	}
	return s
}

// pctCell colorizes percentage strings by threshold: ≥90 err, ≥75 warn. The
// number comes from perf.Percent, the one parser the ranking and the published
// JSON use, so a cell cannot be red on one surface and unmeasured on another.
func pctCell(s string) string {
	f, ok := perf.Percent(s)
	if !ok {
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

// flagCell colors a perf badge pair. Both flags share one column, so the words
// are colored individually — red means the container holds the machine, amber
// that it jumped, and a row carrying both says both.
func flagCell(s string) string {
	if s == "" {
		return s
	}
	parts := strings.Split(s, " ")
	for i, p := range parts {
		switch p {
		case strings.ToUpper(perf.FlagHog):
			parts[i] = styleErr.Render(p)
		case strings.ToUpper(perf.FlagSpike):
			parts[i] = styleWarn.Render(p)
		}
	}
	return strings.Join(parts, " ")
}

func lipglossActiveTab() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("236")).Foreground(lipgloss.Color("231")).Padding(0, 1)
}

func helpLine(pairs ...string) string {
	var b []string
	for i := 0; i+1 < len(pairs); i += 2 {
		b = append(b, styleKey.Render(pairs[i])+" "+styleMuted.Render(pairs[i+1])+"  ")
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
func pinFooter(body, footer string, width, height int) string {
	body = strings.TrimRight(body, "\n")
	footer = strings.TrimRight(footer, "\n")
	if footer == "" {
		return body
	}
	if width > 0 {
		var wrapped []string
		for _, line := range strings.Split(footer, "\n") {
			wrapped = append(wrapped, wrapSegments(line, width)...)
		}
		footer = strings.Join(wrapped, "\n")
	}
	gap := height - lipgloss.Height(body) - lipgloss.Height(footer)
	if gap < 0 {
		gap = 0
	}
	return body + strings.Repeat("\n", gap+1) + footer
}

// wrapSegments folds a line made of "  "-separated styled segments onto
// itself. runewidth is not ANSI-aware, so grouping happens per segment
// using lipgloss.Width, keeping escape sequences intact.
func wrapSegments(line string, width int) []string {
	segs := strings.Split(line, "  ")
	var lines []string
	cur := ""
	for _, s := range segs {
		if s == "" {
			continue
		}
		if cur != "" && lipgloss.Width(cur)+2+lipgloss.Width(s) > width {
			lines = append(lines, cur)
			cur = s
			continue
		}
		if cur == "" {
			cur = s
			continue
		}
		cur += "  " + s
	}
	if cur != "" || lines == nil {
		lines = append(lines, cur)
	}
	return lines
}
