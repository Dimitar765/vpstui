package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"vpstui/internal/app"
	"vpstui/internal/config"
	"vpstui/internal/shell"
)

func main() {
	// Child mode invoked by the TUI for interactive container shells.
	if len(os.Args) > 1 && os.Args[1] == "_shell" {
		os.Exit(shell.Main(os.Args[2:]))
	}
	hosts, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	m := app.New(hosts)
	p := tea.NewProgram(m, tea.WithAltScreen())
	m.Attach(p.Send)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
