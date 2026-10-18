// Package notify fans deployment/server/alert events out to the enabled
// notification channels. Dispatch never blocks its caller: each send runs in
// its own goroutine bounded by a 10 s timeout, and any failure is logged and
// dropped — a dead webhook can never stall a deployment or a monitor pass.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"vpstui/internal/apitypes"
	"vpstui/internal/store"
)

// sendTimeout bounds one channel send.
const sendTimeout = 10 * time.Second

// Dispatcher fans events out to the notification_channels table. It is
// stateless over the store, so the daemon's instances (engine, orchestrator,
// HTTP test endpoint) are interchangeable.
type Dispatcher struct {
	db  *store.DB
	log *log.Logger

	// Timeout bounds one send; defaults to sendTimeout. Test seam.
	Timeout time.Duration

	// telegramAPI is the Bot API base; overridable in tests.
	telegramAPI string
}

// NewDispatcher assembles the dispatcher. A nil logger discards output.
func NewDispatcher(db *store.DB, logger *log.Logger) *Dispatcher {
	if logger == nil {
		logger = log.Default()
	}
	return &Dispatcher{db: db, log: logger, Timeout: sendTimeout,
		telegramAPI: "https://api.telegram.org"}
}

// Dispatch fans the event out to every enabled channel subscribed to it.
// Returns immediately; sends happen in the background and only log failures.
func (d *Dispatcher) Dispatch(event string, payload any) {
	channels, err := d.db.Channels()
	if err != nil {
		d.log.Printf("notify: list channels: %v", err)
		return
	}
	text := Headline(event, payload)
	for _, ch := range channels {
		if !ch.Enabled || !subscribes(ch.Events, event) {
			continue
		}
		go d.deliver(ch, event, text, payload)
	}
}

// TestChannel sends a synthetic test event to one channel synchronously —
// the POST /api/notifications/{id}/test endpoint. Explicit user action, so
// it ignores the channel's events filter and enabled flag.
func (d *Dispatcher) TestChannel(ctx context.Context, ch store.NotificationChannel) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	now := time.Now().UTC().Format(time.RFC3339)
	return send(ctx, d, ch, apitypes.EventTest, "vpstui test notification — if you read this, the channel works.",
		map[string]any{"event": apitypes.EventTest, "time": now})
}

// deliver runs one channel send with the timeout applied; failures are
// logged, never propagated — the goroutine's caller is long gone.
func (d *Dispatcher) deliver(ch store.NotificationChannel, event, text string, payload any) {
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout())
	defer cancel()
	if err := send(ctx, d, ch, event, text, payload); err != nil {
		d.log.Printf("notify: channel %s (%s): %v", ch.ID, ch.Kind, err)
	}
}

func (d *Dispatcher) timeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return sendTimeout
}

// subscribes reports whether the channel's events array contains event.
func subscribes(events []string, event string) bool {
	for _, e := range events {
		if e == event {
			return true
		}
	}
	return false
}

// send routes one message to the channel's kind-specific sender.
func send(ctx context.Context, d *Dispatcher, ch store.NotificationChannel, event, text string, payload any) error {
	switch ch.Kind {
	case apitypes.ChannelDiscord:
		return sendDiscord(ctx, ch.Config, text)
	case apitypes.ChannelSlack:
		return sendSlack(ctx, ch.Config, text)
	case apitypes.ChannelTelegram:
		return d.sendTelegram(ctx, ch.Config, text)
	case apitypes.ChannelWebhook:
		return sendWebhook(ctx, ch.Config, event, text, payload)
	case apitypes.ChannelEmail:
		return sendEmail(ctx, ch.Config, event, text)
	default:
		return fmt.Errorf("unknown channel kind %q", ch.Kind)
	}
}

// ---------------------------------------------------------------- senders

func postJSON(ctx context.Context, url string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	return nil
}

// cfgStr fetches a string config key defensively.
func cfgStr(cfg map[string]any, key string) string {
	s, _ := cfg[key].(string)
	return strings.TrimSpace(s)
}

func sendDiscord(ctx context.Context, cfg map[string]any, text string) error {
	u := cfgStr(cfg, "webhook_url")
	if u == "" {
		return errors.New("discord: config.webhook_url is required")
	}
	return postJSON(ctx, u, map[string]any{"content": text})
}

func sendSlack(ctx context.Context, cfg map[string]any, text string) error {
	u := cfgStr(cfg, "webhook_url")
	if u == "" {
		return errors.New("slack: config.webhook_url is required")
	}
	return postJSON(ctx, u, map[string]any{"text": text})
}

func (d *Dispatcher) sendTelegram(ctx context.Context, cfg map[string]any, text string) error {
	tok := cfgStr(cfg, "bot_token")
	chat := cfgStr(cfg, "chat_id")
	if tok == "" || chat == "" {
		return errors.New("telegram: config.bot_token and config.chat_id are required")
	}
	if _, err := strconv.ParseInt(chat, 10, 64); err != nil {
		return fmt.Errorf("telegram: chat_id %q is not numeric", chat)
	}
	u := d.telegramAPI + "/bot" + url.PathEscape(tok) + "/sendMessage"
	return postJSON(ctx, u, map[string]any{"chat_id": chat, "text": text})
}

// sendWebhook posts the full event as JSON: {"event","text","payload","time"}.
func sendWebhook(ctx context.Context, cfg map[string]any, event, text string, payload any) error {
	u := cfgStr(cfg, "url")
	if u == "" {
		return errors.New("webhook: config.url is required")
	}
	return postJSON(ctx, u, map[string]any{
		"event":   event,
		"text":    text,
		"payload": payload,
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}

// sendEmail sends via stdlib net/smtp (STARTTLS when the server offers it).
// Config: smtp_host ("host[:port]", default port 25), username/password
// (optional PLAIN auth), from, to (comma-separated).
func sendEmail(ctx context.Context, cfg map[string]any, event, text string) error {
	host := cfgStr(cfg, "smtp_host")
	if host == "" {
		return errors.New("email: config.smtp_host is required")
	}
	from := cfgStr(cfg, "from")
	to := cfgStr(cfg, "to")
	if from == "" || to == "" {
		return errors.New("email: config.from and config.to are required")
	}
	if _, err := mail.ParseAddress(from); err != nil {
		return fmt.Errorf("email: bad from %q: %w", from, err)
	}
	var tos []string
	for _, t := range strings.Split(to, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tos = append(tos, t)
		}
	}
	if len(tos) == 0 {
		return errors.New("email: config.to is empty")
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host += ":25"
	}
	var auth smtp.Auth
	if user := cfgStr(cfg, "username"); user != "" {
		auth = smtp.PlainAuth("", user, cfgStr(cfg, "password"), strings.Split(host, ":")[0])
	}
	msg := buildMessage(from, tos, event, text)
	// smtp.SendMail has no context; run it aside and honor the caller's
	// deadline. On timeout the goroutine dies on its own — net/smtp dials
	// with a 10 s timeout, so it never lingers forever.
	errc := make(chan error, 1)
	go func() { errc <- smtp.SendMail(host, auth, from, tos, msg) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		return fmt.Errorf("email: %w", ctx.Err())
	}
}

func buildMessage(from string, tos []string, event, text string) []byte {
	var b bytes.Buffer
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(tos, ", ") + "\r\n")
	b.WriteString("Subject: [vpstui] " + sanitizeSubject(event) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(strings.ReplaceAll(text, "\n", "\r\n"))
	b.WriteString("\r\n")
	return b.Bytes()
}

func sanitizeSubject(s string) string {
	return strings.ReplaceAll(s, "\r", " ")
}

// ---------------------------------------------------------------- rendering

// Headline renders the human-readable one-liner the chat and email senders
// carry. Unknown payloads fall back to a sorted key=value dump so a future
// event kind still reads sensibly.
func Headline(event string, payload any) string {
	m, _ := payload.(map[string]any)
	s := func(key string) string { v, _ := m[key].(string); return v }
	switch event {
	case apitypes.EventDeploySuccess:
		return fmt.Sprintf("vpstui: deploy succeeded — app %s (deployment %s)", s("appName"), s("deploymentId"))
	case apitypes.EventDeployFailed:
		return fmt.Sprintf("vpstui: deploy FAILED — app %s (deployment %s)", s("appName"), s("deploymentId"))
	case apitypes.EventServerOffline:
		return fmt.Sprintf("vpstui: server offline — %s (%s)", s("serverName"), s("error"))
	case apitypes.EventAlertFired:
		return fmt.Sprintf("vpstui: alert FIRING [%s] — %s", s("metric"), s("detail"))
	case apitypes.EventAlertResolved:
		return fmt.Sprintf("vpstui: alert resolved [%s] — %s", s("metric"), s("detail"))
	case apitypes.EventTest:
		return "vpstui test notification — if you read this, the channel works."
	}
	var parts []string
	for _, k := range sortedKeys(m) {
		parts = append(parts, k+"="+fmt.Sprintf("%v", m[k]))
	}
	detail := strings.Join(parts, ", ")
	if detail == "" {
		return "vpstui: " + event
	}
	return "vpstui: " + event + " — " + detail
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
