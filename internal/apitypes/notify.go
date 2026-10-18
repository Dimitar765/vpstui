package apitypes

import "time"

// Notification events. The deploy engine and the monitor dispatch these
// literal strings; channels subscribe by listing them in their events array.
const (
	EventDeploySuccess = "deploy.success"
	EventDeployFailed  = "deploy.failed"
	EventServerOffline = "server.offline"
	EventAlertFired    = "alert.fired"
	EventAlertResolved = "alert.resolved"
	// EventTest is the synthetic event POST /api/notifications/{id}/test sends.
	EventTest = "test"
)

// Notification channel kinds (notification_channels.kind CHECK).
const (
	ChannelDiscord  = "discord"
	ChannelSlack    = "slack"
	ChannelTelegram = "telegram"
	ChannelWebhook  = "webhook"
	ChannelEmail    = "email"
)

// KnownEvents is the closed set of subscribable events; channel CRUD
// validates against it.
func KnownEvents() []string {
	return []string{EventDeploySuccess, EventDeployFailed, EventServerOffline,
		EventAlertFired, EventAlertResolved}
}

// Alert metric literals (alert_rules.metric CHECK).
const (
	MetricLoad1           = "load1"
	MetricMemPct          = "mem_pct"
	MetricContainerCPUPct = "container_cpu_pct"
	MetricContainerMemPct = "container_mem_pct"
	MetricContainerDown   = "container_down"
)

// Alert rule state literals (alert_rules.state).
const (
	AlertStateOK     = "ok"
	AlertStateFiring = "firing"
)

// NotificationChannel mirrors a notification_channels row. Config is the
// free-form per-kind JSON column (webhook_url / bot_token+chat_id / url /
// smtp_host,from,to,…); it is admin-visible by design — the whole API is.
type NotificationChannel struct {
	ID      string         `json:"id"`
	Kind    string         `json:"kind"`
	Config  map[string]any `json:"config"`
	Events  []string       `json:"events"`
	Enabled bool           `json:"enabled"`
}

// AlertRule mirrors an alert_rules row. Empty ServerID means every server;
// empty ContainerPattern means a host-level rule (load1/mem_pct only).
type AlertRule struct {
	ID               string     `json:"id"`
	ServerID         string     `json:"serverId,omitempty"`
	ContainerPattern string     `json:"containerPattern,omitempty"`
	Metric           string     `json:"metric"`
	Threshold        float64    `json:"threshold"`
	DurationSec      int        `json:"durationSec"`
	Enabled          bool       `json:"enabled"`
	State            string     `json:"state"`
	LastFiredAt      *time.Time `json:"lastFiredAt,omitempty"`
}
