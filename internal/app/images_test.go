package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"vpstui/internal/docker"
	"vpstui/internal/models"
)

func TestImagesPanel(t *testing.T) {
	d := NewDashboard(host("h"), docker.New(nil), 80, 24, 1)

	// I opens the panel and fetches.
	_, cmd := d.Update(key("I"))
	if !d.showImages || !d.imgLoading || cmd == nil {
		t.Fatalf("I: showImages=%v loading=%v cmd=%v", d.showImages, d.imgLoading, cmd != nil)
	}
	if d.showDisk || d.showPerf {
		t.Fatal("I left another sub-mode open")
	}

	// The load fills the table and the view renders both repositories.
	d.Update(imagesLoadedMsg{msgTag: msgTag{hostID: "h", gen: 1}, images: []models.Image{
		{ID: "8f30766c1db2", Repository: "nginx", Tag: "latest", CreatedSince: "2 weeks ago", Size: "187MB"},
		{ID: "0123456789ab", Repository: "alpine", Tag: "3.19", CreatedSince: "5 days ago", Size: "7MB"},
	}})
	if d.imgLoading || len(d.images) != 2 || d.imgErr != "" {
		t.Fatalf("after load: loading=%v images=%d err=%q", d.imgLoading, len(d.images), d.imgErr)
	}
	v := d.View()
	if !strings.Contains(v, "nginx") || !strings.Contains(v, "alpine") {
		t.Fatalf("view missing image rows:\n%s", v)
	}

	// D on the cursor row arms the removal confirm.
	d.imgTbl.SetCursor(1)
	d.Update(key("D"))
	if d.confirm != "rm image" || d.imgConfirm == nil || d.imgConfirm.ID != "0123456789ab" {
		t.Fatalf("D: confirm=%q imgConfirm=%+v", d.confirm, d.imgConfirm)
	}
	// The confirm label names the image, never the dashboard's selected
	// container behind the panel.
	if v := d.View(); !strings.Contains(v, "rm image alpine:3.19? [y/N]") {
		t.Fatalf("confirm label wrong:\n%s", v)
	}
	// Any other key cancels the confirm without acting.
	d.Update(key("?"))
	if d.confirm != "" {
		t.Fatalf("stray key left confirm=%q", d.confirm)
	}

	// y on an armed confirm returns the action command (not executed: nil conn).
	d.Update(key("D"))
	_, cmd = d.Update(key("y"))
	if cmd == nil {
		t.Fatal("y produced no action cmd")
	}
	if d.imgConfirm != nil {
		t.Fatal("y left imgConfirm armed")
	}

	// n cancels.
	d.Update(key("D"))
	d.Update(key("n"))
	if d.confirm != "" || d.imgConfirm == nil {
		t.Fatalf("n: confirm=%q imgConfirm=%v", d.confirm, d.imgConfirm != nil)
	}

	// D with nothing loaded must not arm anything.
	d2 := NewDashboard(host("h"), docker.New(nil), 80, 24, 1)
	d2.Update(key("I"))
	d2.Update(key("D"))
	if d2.confirm != "" || d2.imgConfirm != nil {
		t.Fatalf("empty list D: confirm=%q imgConfirm=%v", d2.confirm, d2.imgConfirm)
	}

	// esc closes the panel.
	d.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if d.showImages || d.imgConfirm != nil {
		t.Fatalf("esc: showImages=%v imgConfirm=%v", d.showImages, d.imgConfirm != nil)
	}
}

func TestImagesLoadedMsgRoutes(t *testing.T) {
	m := New(nil, nil)
	s := &Session{host: host("a"), gen: 2}
	s.dash = NewDashboard(s.host, docker.New(nil), 80, 24, s.gen)
	s.dash.showImages = true
	m.sessions = []*Session{s}
	m.active = 0
	m.view = viewDashboard

	// Stale tag: dropped before it touches the panel.
	_, cmd := m.Update(imagesLoadedMsg{msgTag: msgTag{hostID: "a", gen: 1}, images: []models.Image{{ID: "deadbeef1234", Repository: "old"}}})
	if cmd != nil {
		t.Fatal("stale imagesLoadedMsg produced a command")
	}
	if len(s.dash.images) != 0 {
		t.Fatalf("stale msg applied: %+v", s.dash.images)
	}

	// Live tag: rows land.
	m.Update(imagesLoadedMsg{msgTag: msgTag{hostID: "a", gen: 2}, images: []models.Image{{ID: "8f30766c1db2", Repository: "nginx"}}})
	if len(s.dash.images) != 1 || s.dash.images[0].Repository != "nginx" {
		t.Fatalf("live msg not applied: %+v", s.dash.images)
	}
}
