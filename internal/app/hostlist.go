package app

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"vpstui/internal/models"
)

type hostItem struct {
	h    models.Host
	conn bool
}

func (i hostItem) Title() string {
	t := i.h.Name
	if t == "" {
		t = i.h.Address
	}
	if i.conn {
		t = styleOK.Render("●") + " " + t
	}
	return t
}
func (i hostItem) Description() string {
	return fmt.Sprintf("%s@%s:%d · %s", i.h.User, i.h.Address, i.h.Port, i.h.AuthType)
}
func (i hostItem) FilterValue() string { return i.Title() }

type HostList struct {
	l     list.Model
	hosts []models.Host
	conn  map[string]bool
}

func NewHostList(hosts []models.Host) *HostList {
	items := itemsFor(hosts, nil)
	l := list.New(items, list.NewDefaultDelegate(), 40, 12)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	return &HostList{l: l, hosts: hosts}
}

func itemsFor(hosts []models.Host, conn map[string]bool) []list.Item {
	items := make([]list.Item, 0, len(hosts))
	for _, h := range hosts {
		items = append(items, hostItem{h: h, conn: conn[h.ID]})
	}
	return items
}

func (l *HostList) SetItems(hosts []models.Host) {
	l.hosts = hosts
	_ = l.l.SetItems(itemsFor(hosts, l.conn))
}

func (l *HostList) SetConnected(ids []string) {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	l.conn = set
	_ = l.l.SetItems(itemsFor(l.hosts, set))
}

func (l *HostList) SetSize(w, h int) {
	l.l.SetSize(w-2, h-6)
}

func (l *HostList) Update(msg tea.Msg) (*HostList, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && len(l.hosts) > 0 {
		idx := l.l.Index()
		if idx < 0 || idx >= len(l.hosts) {
			idx = 0
		}
		h := l.hosts[idx]
		switch km.String() {
		case "enter":
			return l, func() tea.Msg { return connectHostReq{host: h} }
		case "e":
			return l, func() tea.Msg { return editHostReq{host: h} }
		case "d":
			return l, func() tea.Msg { return deleteHostReq{host: h} }
		}
	}
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "n" {
		return l, func() tea.Msg { return newHostReq{} }
	}
	var cmd tea.Cmd
	l.l, cmd = l.l.Update(msg)
	return l, cmd
}

func (l *HostList) View() string {
	if len(l.hosts) == 0 {
		return styleMuted.Render("no hosts yet — press n to add one")
	}
	return l.l.View()
}
