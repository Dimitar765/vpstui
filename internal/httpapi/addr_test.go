package httpapi

import "testing"

func TestLoopbackOnly(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8712", true},
		{"[::1]:8712", true},
		{"localhost:8712", true},
		{"0.0.0.0:8712", false},
		{":8712", false},
		{"192.168.1.5:8712", false},
		{"garbage", false},
	}
	for _, tc := range cases {
		if got := loopbackOnly(tc.addr); got != tc.want {
			t.Errorf("loopbackOnly(%q) = %v; want %v", tc.addr, got, tc.want)
		}
	}
}

func TestListenAddrDefault(t *testing.T) {
	t.Setenv("VPSTUI_HTTP_ADDR", "")
	addr, err := ListenAddr()
	if err != nil {
		t.Fatal(err)
	}
	if addr != defaultAddr {
		t.Fatalf("addr = %q; want %q", addr, defaultAddr)
	}
}

func TestListenAddrRejectsNonLoopback(t *testing.T) {
	t.Setenv("VPSTUI_HTTP_ADDR", "0.0.0.0:9999")
	if _, err := ListenAddr(); err == nil {
		t.Fatal("ListenAddr accepted non-loopback override")
	}
}

func TestListenAddrAcceptsLoopbackOverride(t *testing.T) {
	t.Setenv("VPSTUI_HTTP_ADDR", "127.0.0.1:9999")
	addr, err := ListenAddr()
	if err != nil || addr != "127.0.0.1:9999" {
		t.Fatalf("addr=%q err=%v", addr, err)
	}
}
