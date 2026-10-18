package notify

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"vpstui/internal/apitypes"
	"vpstui/internal/store"
)

// newTestDB opens a migrated throwaway store.
func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/n.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return db
}

// hit is one captured request to a recording receiver.
type hit struct {
	path        string
	contentType string
	body        []byte
}

// receiver is an httptest server capturing requests into a channel.
type receiver struct {
	srv *httptest.Server
	ch  chan hit
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{ch: make(chan hit, 16)}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(req.Body)
		r.ch <- hit{path: req.URL.Path, contentType: req.Header.Get("Content-Type"), body: append([]byte{}, b.Bytes()...)}
		w.WriteHeader(http.StatusOK)
	})
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	return r
}

// next waits for one captured request or fails the test.
func (r *receiver) next(t *testing.T) hit {
	t.Helper()
	select {
	case h := <-r.ch:
		return h
	case <-time.After(5 * time.Second):
		t.Fatal("receiver got no request within 5s")
		return hit{}
	}
}

// none asserts no request lands within a short window.
func (r *receiver) none(t *testing.T) {
	t.Helper()
	select {
	case h := <-r.ch:
		t.Fatalf("unexpected request: %s %s", h.path, h.body)
	case <-time.After(300 * time.Millisecond):
	}
}

func channel(id, kind string, cfg map[string]any, events []string) store.NotificationChannel {
	if events == nil {
		events = []string{}
	}
	return store.NotificationChannel{ID: id, Kind: kind, Config: cfg, Events: events, Enabled: true}
}

// TestFanOutPayloadShapes drives all four HTTP kinds through one dispatch and
// asserts each sender's wire shape.
func TestFanOutPayloadShapes(t *testing.T) {
	db := newTestDB(t)
	disc, slack, hook, tg := newReceiver(t), newReceiver(t), newReceiver(t), newReceiver(t)

	d := NewDispatcher(db, log.New(new(bytes.Buffer), "", 0))
	d.telegramAPI = tg.srv.URL

	for _, c := range []store.NotificationChannel{
		channel("disc", "discord", map[string]any{"webhook_url": disc.srv.URL + "/discord"}, []string{apitypes.EventDeploySuccess}),
		channel("slack", "slack", map[string]any{"webhook_url": slack.srv.URL + "/slack"}, []string{apitypes.EventDeploySuccess}),
		channel("hook", "webhook", map[string]any{"url": hook.srv.URL + "/hook"}, []string{apitypes.EventDeploySuccess}),
		channel("tg", "telegram", map[string]any{"bot_token": "T0K", "chat_id": "4242"}, []string{apitypes.EventDeploySuccess}),
	} {
		if err := db.CreateChannel(c); err != nil {
			t.Fatal(err)
		}
	}

	payload := map[string]any{"appId": "a1", "appName": "web", "deploymentId": "dep1"}
	d.Dispatch(apitypes.EventDeploySuccess, payload)

	dh := disc.next(t)
	var dbody map[string]any
	if err := json.Unmarshal(dh.body, &dbody); err != nil {
		t.Fatalf("discord body: %v", err)
	}
	if !strings.Contains(dbody["content"].(string), "web") || !strings.Contains(dbody["content"].(string), "dep1") {
		t.Fatalf("discord content = %v", dbody["content"])
	}

	sh := slack.next(t)
	var sbody map[string]any
	if err := json.Unmarshal(sh.body, &sbody); err != nil {
		t.Fatalf("slack body: %v", err)
	}
	if !strings.Contains(sbody["text"].(string), "deploy succeeded") {
		t.Fatalf("slack text = %v", sbody["text"])
	}

	hh := hook.next(t)
	var hbody map[string]any
	if err := json.Unmarshal(hh.body, &hbody); err != nil {
		t.Fatalf("webhook body: %v", err)
	}
	if hbody["event"] != apitypes.EventDeploySuccess {
		t.Fatalf("webhook event = %v", hbody["event"])
	}
	if hp, ok := hbody["payload"].(map[string]any); !ok || hp["deploymentId"] != "dep1" {
		t.Fatalf("webhook payload = %v", hbody["payload"])
	}
	if _, ok := hbody["time"].(string); !ok {
		t.Fatalf("webhook time missing: %v", hbody)
	}

	th := tg.next(t)
	if th.path != "/botT0K/sendMessage" {
		t.Fatalf("telegram path = %s", th.path)
	}
	var tbody map[string]any
	if err := json.Unmarshal(th.body, &tbody); err != nil {
		t.Fatalf("telegram body: %v", err)
	}
	if tbody["chat_id"] != "4242" || !strings.Contains(tbody["text"].(string), "deploy succeeded") {
		t.Fatalf("telegram body = %v", tbody)
	}
}

// TestEventsFilter: an unsubscribed channel stays silent; a disabled one too.
func TestEventsFilter(t *testing.T) {
	db := newTestDB(t)
	silent, off := newReceiver(t), newReceiver(t)
	for _, c := range []store.NotificationChannel{
		channel("silent", "webhook", map[string]any{"url": silent.srv.URL}, []string{apitypes.EventAlertFired}),
		func() store.NotificationChannel {
			c := channel("off", "webhook", map[string]any{"url": off.srv.URL}, []string{apitypes.EventDeploySuccess})
			c.Enabled = false
			return c
		}(),
	} {
		if err := db.CreateChannel(c); err != nil {
			t.Fatal(err)
		}
	}
	d := NewDispatcher(db, log.New(new(bytes.Buffer), "", 0))
	d.Dispatch(apitypes.EventDeploySuccess, map[string]any{})
	silent.none(t)
	off.none(t)
}

// TestTimeoutNeverBlocks: a hanging receiver must not stall Dispatch, and the
// send must abort at the configured timeout with a logged failure.
func TestTimeoutNeverBlocks(t *testing.T) {
	db := newTestDB(t)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	t.Cleanup(slow.Close)
	if err := db.CreateChannel(channel("slow", "webhook", map[string]any{"url": slow.URL}, []string{apitypes.EventDeployFailed})); err != nil {
		t.Fatal(err)
	}

	var logBuf safeBuffer
	d := NewDispatcher(db, log.New(&logBuf, "", 0))
	d.Timeout = 150 * time.Millisecond

	start := time.Now()
	d.Dispatch(apitypes.EventDeployFailed, map[string]any{"appName": "x"})
	if el := time.Since(start); el > time.Second {
		t.Fatalf("Dispatch blocked for %v", el)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logBuf.String(), "notify: channel") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout failure never logged; log=%q", logBuf.String())
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestTestChannelWebhook drives the notification-test endpoint's path.
func TestTestChannelWebhook(t *testing.T) {
	db := newTestDB(t)
	r := newReceiver(t)
	c := channel("tw", "webhook", map[string]any{"url": r.srv.URL}, nil)
	if err := db.CreateChannel(c); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(db, log.New(new(bytes.Buffer), "", 0))
	if err := d.TestChannel(context.Background(), c); err != nil {
		t.Fatalf("test channel: %v", err)
	}
	h := r.next(t)
	var body map[string]any
	if err := json.Unmarshal(h.body, &body); err != nil {
		t.Fatal(err)
	}
	if body["event"] != apitypes.EventTest {
		t.Fatalf("test event = %v", body["event"])
	}
}

// TestEmailSender speaks just enough SMTP to satisfy net/smtp and captures
// the message.
func TestEmailSender(t *testing.T) {
	var mu sync.Mutex
	var data string
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		say := func(s string) { w.WriteString(s + "\r\n"); w.Flush() }
		say("220 test ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				w.WriteString("250-test\r\n250-AUTH PLAIN\r\n250 OK\r\n")
				w.Flush()
			case strings.HasPrefix(cmd, "AUTH"):
				say("235 ok")
			case strings.HasPrefix(cmd, "MAIL FROM"):
				say("250 ok")
			case strings.HasPrefix(cmd, "RCPT TO"):
				say("250 ok")
			case strings.HasPrefix(cmd, "DATA"):
				say("354 go")
				var b strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					b.WriteString(l)
					if l == ".\r\n" || l == ".\n" {
						break
					}
				}
				mu.Lock()
				data = b.String()
				mu.Unlock()
				say("250 ok")
			case strings.HasPrefix(cmd, "QUIT"):
				say("221 bye")
				return
			default:
				say("500 ?")
			}
		}
	}()

	cfg := map[string]any{
		"smtp_host": ln.Addr().String(), "from": "vpstui@test", "to": "admin@test, ops@test",
		"username": "u", "password": "p",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sendEmail(ctx, cfg, apitypes.EventDeployFailed, "deploy FAILED — app web"); err != nil {
		t.Fatalf("sendEmail: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, want := range []string{"Subject: [vpstui] ", "To: admin@test, ops@test", "deploy FAILED"} {
		if !strings.Contains(data, want) {
			t.Fatalf("message missing %q: %q", want, data)
		}
	}
}

// TestEmailTimeout: a silent SMTP server must honor the context deadline.
func TestEmailTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c
		}
	}() // accept and say nothing

	cfg := map[string]any{"smtp_host": ln.Addr().String(), "from": "a@t", "to": "b@t"}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err = sendEmail(ctx, cfg, apitypes.EventTest, "x")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
}

// TestHeadlineFallback covers the unknown-event rendering.
func TestHeadlineFallback(t *testing.T) {
	got := Headline("custom.event", map[string]any{"b": 2, "a": "one"})
	if got != "vpstui: custom.event — a=one, b=2" {
		t.Fatalf("headline = %q", got)
	}
	if got := Headline("custom.event", nil); got != "vpstui: custom.event" {
		t.Fatalf("empty headline = %q", got)
	}
	if !strings.Contains(Headline(apitypes.EventServerOffline, map[string]any{"serverName": "loop", "error": "refused"}), "loop") {
		t.Fatal("offline headline missing server name")
	}
}
