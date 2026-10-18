package app

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// keyHelp is one row of a screen's key table. Every screen builds its rows in
// one place (helpRows), and both surfaces that show keys — the one-line help
// bar and the ? overlay — are rendered from it, so the two can never disagree.
type keyHelp struct{ key, desc string }

// helpRowsOf returns the full key table for the screen the model is on. The
// per-screen sources are the views' own helpRows methods; this is the root's
// dispatch, including the host list which lives at model level.
func (m *Model) helpRows() []keyHelp {
	switch m.view {
	case viewHosts:
		return []keyHelp{
			{"↑/↓ j/k", "move"}, {"n", "new"}, {"e", "edit"}, {"d", "delete"},
			{"enter", "connect/resume"}, {"S", "settings"}, {"a", "errors"},
			{"H", "hub"}, {"q", "quit"},
		}
	case viewForm:
		return hostFormRows(false)
	case viewPrompt:
		return hostFormRows(true)
	case viewDashboard:
		if s := m.activeSession(); s != nil && s.dash != nil {
			return s.dash.helpRows()
		}
	case viewContainer:
		if s := m.activeSession(); s != nil && s.cv != nil {
			return s.cv.helpRows()
		}
	case viewErrors:
		if m.errView != nil {
			return m.errView.helpRows()
		}
	case viewHubFleet:
		return hubFleetRows(m.hub != nil && m.hub.showContainers)
	case viewHubApps:
		return hubAppsRows()
	case viewHubDeployments:
		return hubDepsRows()
	case viewHubLogs:
		if m.hubLog != nil {
			return m.hubLog.helpRows()
		}
	case viewHubForm:
		return hubFormRows()
	}
	return nil
}

func hostFormRows(secret bool) []keyHelp {
	if secret {
		return []keyHelp{{"enter", "submit"}, {"esc", "cancel"}}
	}
	return []keyHelp{
		{"up/down", "fields"}, {"←/→", "choice"}, {"enter", "next / save"}, {"esc", "cancel"},
	}
}

// helpBar renders the short form of a key table: the first pairs entries plus
// a "? more" hint that opens the overlay.
func helpBar(rows []keyHelp, pairs int) string {
	if pairs > len(rows) {
		pairs = len(rows)
	}
	shown := make([]string, 0, 2*pairs+2)
	for _, r := range rows[:pairs] {
		shown = append(shown, r.key, r.desc)
	}
	shown = append(shown, "?", "more")
	return helpLine(shown...)
}

// helpOverlay dims the screen behind a centered card listing the full key
// table. The dim is a whole-frame faint: SGR dim is uniform, cheap, and leaves
// the layout byte-identical underneath the card.
func helpOverlay(base string, title string, rows []keyHelp, w, h int) string {
	dim := lipgloss.NewStyle().Faint(true).Render(base)

	var b strings.Builder
	b.WriteString(styleTitle.Render("keys — "+title) + "\n\n")
	for _, r := range rows {
		b.WriteString(styleKey.Render(padRight(r.key, 10)) + styleMuted.Render(r.desc) + "\n")
	}
	b.WriteString("\n" + styleMuted.Render("? / esc — close"))
	card := panel("", b.String(), min(w-4, 52))

	return pasteOver(dim, card, w, h)
}

// pasteOver places card centered over base, line by line. The card's lines
// (padded to the card's width) replace the middle of the frame; the cut is
// ANSI-aware so styled base lines keep their escape sequences intact.
func pasteOver(base, card string, w, h int) string {
	baseLines := strings.Split(base, "\n")
	cardLines := strings.Split(card, "\n")
	cw := 0
	for _, l := range cardLines {
		cw = max(cw, lipgloss.Width(l))
	}
	top := max((h-lipgloss.Height(card))/2, 0)
	left := max((w-cw)/2, 0)

	out := make([]string, len(baseLines))
	copy(out, baseLines)
	for i, cl := range cardLines {
		y := top + i
		if y >= len(out) {
			break
		}
		out[y] = ansi.Truncate(out[y], left, "") + cl
	}
	return strings.Join(out, "\n")
}

func padRight(s string, n int) string {
	if d := n - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}
