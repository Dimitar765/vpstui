package app

import (
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"vpstui/internal/docker"
	"vpstui/internal/sshclient"
)

// One typed connection loss arms exactly one automatic redial; a second loss
// leaves the manual banner up. Plain docker errors arm nothing.
func TestConnLossArmsSingleRetry(t *testing.T) {
	old := retryAfter
	retryAfter = 5 * time.Millisecond
	t.Cleanup(func() { retryAfter = old })

	loss := fmt.Errorf("%w: %v", sshclient.ErrConnLost, io.EOF)

	m := New(nil, nil)
	// 41 can never collide with nextGen(), which starts counting at 1 — a
	// hand-set gen of 1 would mask the redial's generation bump.
	s := &Session{host: host("a"), gen: 41}
	s.dash = NewDashboard(s.host, docker.New(nil), 80, 24, s.gen)
	m.sessions = []*Session{s}
	m.active = 0
	m.view = viewDashboard

	_, cmd := m.Update(statsLoadedMsg{msgTag: msgTag{hostID: "a", gen: 41}, err: loss})
	if cmd == nil {
		t.Fatal("first connection loss must arm a retry")
	}
	if !s.retried {
		t.Error("retried flag not set when arming")
	}
	if !s.dash.disconnected {
		t.Error("disconnected banner flag not set after the loss")
	}

	msg := cmd()
	rm, ok := msg.(retryMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want retryMsg", msg)
	}
	if rm.hostID != "a" || rm.gen != 41 {
		t.Errorf("retry tag = %+v, want a/41", rm.msgTag)
	}

	// The retry redials the same session: new generation, dashboard dropped,
	// the connecting screen up. The dial command is never executed here.
	m2, dial := m.Update(rm)
	if s.gen == 41 {
		t.Error("retry did not mint a new generation")
	}
	if s.conn != nil || s.dash != nil {
		t.Error("retry did not tear down the dead generation")
	}
	if m2.(*Model).view != viewConnecting {
		t.Errorf("view = %v, want viewConnecting during redial", m2.(*Model).view)
	}
	if dial == nil {
		t.Error("retry produced no dial command")
	}

	// A second loss on the same generation must not re-arm; the banner stays.
	m3 := New(nil, nil)
	s3 := &Session{host: host("b"), gen: 1, retried: true}
	s3.dash = NewDashboard(s3.host, docker.New(nil), 80, 24, s3.gen)
	m3.sessions = []*Session{s3}
	_, cmd = m3.Update(statsLoadedMsg{msgTag: msgTag{hostID: "b", gen: 1}, err: loss})
	if cmd != nil {
		t.Fatal("second loss must not re-arm the retry")
	}
	if !s3.dash.disconnected {
		t.Error("disconnected banner flag not set on second loss")
	}

	// A plain docker error is not a connection loss: no retry, no banner.
	m4 := New(nil, nil)
	s4 := &Session{host: host("c"), gen: 1}
	s4.dash = NewDashboard(s4.host, docker.New(nil), 80, 24, s4.gen)
	m4.sessions = []*Session{s4}
	_, cmd = m4.Update(statsLoadedMsg{msgTag: msgTag{hostID: "c", gen: 1}, err: errors.New("docker daemon is not running on this host")})
	if cmd != nil {
		t.Fatal("docker error must not arm a retry")
	}
	if s4.dash.disconnected {
		t.Error("docker error must not raise the disconnected banner")
	}
}
