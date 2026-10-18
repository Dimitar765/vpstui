package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"vpstui/internal/apitypes"
	"vpstui/internal/models"
	"vpstui/internal/notify"
	"vpstui/internal/store"
)

// registerNotifications: the notification-channel domain.
func (s *Server) registerNotifications(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/notifications", s.handleNotificationsList)
	mux.HandleFunc("POST /api/notifications", s.handleNotificationCreate)
	mux.HandleFunc("DELETE /api/notifications/{id}", s.handleNotificationDelete)
	mux.HandleFunc("POST /api/notifications/{id}/test", s.handleNotificationTest)
}

// dispatcher is the handler-side view of the notification fan-out. The
// dispatcher is stateless over the channels table, so this instance and the
// one main.go wires into the deploy engine and orchestrator behave
// identically; the split exists only because the Server struct (http.go) is
// assembled before the dispatcher in main and must not grow assembly-time
// dependencies for it.
func (s *Server) dispatcher() *notify.Dispatcher {
	return notify.NewDispatcher(s.db, s.log)
}

func toChannelResp(c store.NotificationChannel) apitypes.NotificationChannel {
	if c.Config == nil {
		c.Config = map[string]any{}
	}
	if c.Events == nil {
		c.Events = []string{}
	}
	return apitypes.NotificationChannel{
		ID: c.ID, Kind: c.Kind, Config: c.Config, Events: c.Events, Enabled: c.Enabled,
	}
}

func (s *Server) handleNotificationsList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Channels()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]apitypes.NotificationChannel, 0, len(rows))
	for _, c := range rows {
		out = append(out, toChannelResp(c))
	}
	writeJSON(w, http.StatusOK, out)
}

type channelReq struct {
	Kind    string         `json:"kind"`
	Config  map[string]any `json:"config"`
	Events  []string       `json:"events"`
	Enabled *bool          `json:"enabled"`
}

// validateChannel checks kind, event subscriptions and the per-kind config
// keys. notify owns the config contract; the same checks run on send.
func validateChannel(req *channelReq) error {
	switch req.Kind {
	case apitypes.ChannelDiscord, apitypes.ChannelSlack:
		if s := strCfg(req.Config, "webhook_url"); s == "" {
			return errors.New("config.webhook_url is required")
		}
	case apitypes.ChannelTelegram:
		if strCfg(req.Config, "bot_token") == "" || strCfg(req.Config, "chat_id") == "" {
			return errors.New("config.bot_token and config.chat_id are required")
		}
	case apitypes.ChannelWebhook:
		if strCfg(req.Config, "url") == "" {
			return errors.New("config.url is required")
		}
	case apitypes.ChannelEmail:
		if strCfg(req.Config, "smtp_host") == "" || strCfg(req.Config, "from") == "" || strCfg(req.Config, "to") == "" {
			return errors.New("config.smtp_host, config.from and config.to are required")
		}
	default:
		return errors.New("kind must be discord, slack, telegram, webhook or email")
	}
	known := map[string]bool{}
	for _, e := range apitypes.KnownEvents() {
		known[e] = true
	}
	for _, e := range req.Events {
		if !known[e] {
			return errors.New("unknown event " + e)
		}
	}
	return nil
}

func strCfg(cfg map[string]any, key string) string {
	s, _ := cfg[key].(string)
	return strings.TrimSpace(s)
}

func (s *Server) handleNotificationCreate(w http.ResponseWriter, r *http.Request) {
	var req channelReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if req.Config == nil {
		req.Config = map[string]any{}
	}
	if err := validateChannel(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	row := store.NotificationChannel{
		ID: models.NewID(), Kind: req.Kind, Config: req.Config,
		Events: req.Events, Enabled: enabled,
	}
	if err := s.db.CreateChannel(row); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toChannelResp(row))
}

func (s *Server) channelRow(w http.ResponseWriter, r *http.Request) (store.NotificationChannel, bool) {
	c, err := s.db.ChannelByID(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "channel not found")
		return store.NotificationChannel{}, false
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return store.NotificationChannel{}, false
	}
	return c, true
}

func (s *Server) handleNotificationDelete(w http.ResponseWriter, r *http.Request) {
	c, ok := s.channelRow(w, r)
	if !ok {
		return
	}
	if err := s.db.DeleteChannel(c.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleNotificationTest fires a synthetic test event at exactly this
// channel (synchronously, so the caller learns the outcome).
func (s *Server) handleNotificationTest(w http.ResponseWriter, r *http.Request) {
	c, ok := s.channelRow(w, r)
	if !ok {
		return
	}
	if err := s.dispatcher().TestChannel(r.Context(), c); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
