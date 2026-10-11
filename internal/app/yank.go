package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
)

// yankMsg is the outcome of a clipboard write. The shell-out happens in a
// tea.Cmd because Update must never block: wl-copy and xclip are separate
// processes and a hung clipboard helper would freeze the UI.
type yankMsg struct {
	lines int
	bytes int
	err   error
}

// yankCmd returns nil for an empty target, so pressing y on nothing is silent
// rather than an error — no clipboard call, no status line.
func yankCmd(text string) tea.Cmd {
	if text == "" {
		return nil
	}
	return func() tea.Msg {
		return yankMsg{lines: strings.Count(text, "\n") + 1, bytes: len(text), err: clipboard.WriteAll(text)}
	}
}

// Describe reports size only, never the text. A yanked line is remote,
// attacker-controlled data: echoing it would put it on screen a second time and
// let it imitate our own messages.
func (m yankMsg) Describe() string {
	if m.err != nil {
		return "clipboard unavailable — install wl-copy, xclip or pbcopy"
	}
	size := strconv.Itoa(m.bytes) + " bytes"
	if m.bytes >= 1024 {
		size = fmt.Sprintf("%.1f KiB", float64(m.bytes)/1024)
	}
	word := "line"
	if m.lines != 1 {
		word = "lines"
	}
	return "yanked " + strconv.Itoa(m.lines) + " " + word + " (" + size + ")"
}
