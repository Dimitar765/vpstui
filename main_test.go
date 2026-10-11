package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"vpstui/internal/httpapi"
)

func TestStartAPIBindsAndServes(t *testing.T) {
	t.Setenv("VPSTUI_HTTP_ADDR", "127.0.0.1:0")
	store := httpapi.NewStore()
	srv, ln := startAPI(store)
	if srv == nil || ln == nil {
		t.Fatal("startAPI returned nil server/listener")
	}
	defer srv.Close()

	url := "http://" + ln.Addr().String() + "/healthz"
	deadline := time.Now().Add(2 * time.Second)
	for {
		res, err := http.Get(url)
		if err == nil {
			defer res.Body.Close()
			var body map[string]bool
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if !body["ok"] {
				t.Fatalf("healthz body = %v", body)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never answered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStartAPIWarnsOnNonLoopback(t *testing.T) {
	t.Setenv("VPSTUI_HTTP_ADDR", "0.0.0.0:0")
	srv, ln := startAPI(httpapi.NewStore())
	if srv != nil || ln != nil {
		t.Fatal("startAPI bound a non-loopback address")
	}
}
