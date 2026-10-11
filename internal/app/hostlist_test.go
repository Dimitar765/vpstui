package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"vpstui/internal/models"
)

// The host list's keys are pure dispatch: each one must emit exactly the
// request that names the host under the cursor.
func TestHostListDispatch(t *testing.T) {
	hl := NewHostList([]models.Host{host("a"), host("b")})
	hl.SetSize(80, 24)

	run := func(k string) tea.Msg {
		_, cmd := hl.Update(key(k))
		if cmd == nil {
			t.Fatalf("key %q produced no command", k)
		}
		return cmd()
	}

	if msg, ok := run("enter").(connectHostReq); !ok || msg.host.ID != "a" {
		t.Errorf("enter = %+v, want connectHostReq for a", msg)
	}
	if msg, ok := run("e").(editHostReq); !ok || msg.host.ID != "a" {
		t.Errorf("e = %+v, want editHostReq for a", msg)
	}
	if msg, ok := run("d").(deleteHostReq); !ok || msg.host.ID != "a" {
		t.Errorf("d = %+v, want deleteHostReq for a", msg)
	}
	if _, ok := run("n").(newHostReq); !ok {
		t.Error("n did not request the new-host form")
	}

	// After moving the cursor, the requests follow it to the second host.
	hl.Update(key("j"))
	if msg, ok := run("enter").(connectHostReq); !ok || msg.host.ID != "b" {
		t.Errorf("enter after j = %+v, want connectHostReq for b", msg)
	}
	if msg, ok := run("d").(deleteHostReq); !ok || msg.host.ID != "b" {
		t.Errorf("d after j = %+v, want deleteHostReq for b", msg)
	}
}

// An empty list has no host to name, so the host keys must not fire requests.
func TestHostListEmptyIgnoresHostKeys(t *testing.T) {
	hl := NewHostList(nil)
	hl.SetSize(80, 24)
	for _, k := range []string{"enter", "e", "d"} {
		_, cmd := hl.Update(key(k))
		if cmd == nil {
			continue
		}
		if _, bad := cmd().(connectHostReq); bad {
			t.Fatalf("empty list dispatched a connect request for %q", k)
		}
		if _, bad := cmd().(deleteHostReq); bad {
			t.Fatalf("empty list dispatched a delete request for %q", k)
		}
		if _, bad := cmd().(editHostReq); bad {
			t.Fatalf("empty list dispatched an edit request for %q", k)
		}
	}
}
