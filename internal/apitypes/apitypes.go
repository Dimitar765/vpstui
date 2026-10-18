// Package apitypes is the shared JSON contract between the vpstui-server
// daemon and its two clients: the TUI hub mode and the web GUI. Field names
// mirror internal/httpapi's published shapes (cpuPct, memPct, memUsage, …) so
// client tables written for one feed read the other without translation.
package apitypes

import "time"

// WS frame type literals. Both sides switch on these exact strings.
const (
	FrameFleetUpdate  = "fleet.update"
	FrameDeployLog    = "deploy.log"
	FrameDeployStatus = "deploy.status"
	FrameAlert        = "alert"
)

// Server status values stored in servers.status and published on every fleet
// frame.
const (
	StatusUnknown = "unknown"
	StatusOnline  = "online"
	StatusOffline = "offline"
)

// HostStats mirrors httpapi's hostStatsJSON exactly.
type HostStats struct {
	Load1      float64 `json:"load1"`
	Load5      float64 `json:"load5"`
	Load15     float64 `json:"load15"`
	MemTotalMB int     `json:"memTotalMB"`
	MemUsedMB  int     `json:"memUsedMB"`
	NumCPU     int     `json:"numCPU"`
	UptimeSec  float64 `json:"uptimeSec"`
}

// Container mirrors httpapi's containerJSON exactly. Tag/TagColor are always
// empty from the server: container tags live only in the TUI's own config and
// never reach the daemon, but the fields stay so one table renders both feeds.
type Container struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Tag      string   `json:"tag"`
	TagColor string   `json:"tagColor"`
	Image    string   `json:"image"`
	State    string   `json:"state"`
	Status   string   `json:"status"`
	CPUPerc  string   `json:"cpu"`
	MemPerc  string   `json:"memPerc"`
	MemUsage string   `json:"memUsage"`
	CPUPct   *float64 `json:"cpuPct"`
	MemPct   *float64 `json:"memPct"`
}

// FleetServer is one row of the fleet view: the server row plus its latest
// monitoring snapshot. FetchedAt is nil while no snapshot has ever landed.
type FleetServer struct {
	ID                 string      `json:"id"`
	Name               string      `json:"name"`
	Address            string      `json:"address"`
	Port               int         `json:"port"`
	User               string      `json:"user"`
	AuthType           string      `json:"authType"`
	KeyPath            string      `json:"keyPath,omitempty"`
	BaseDir            string      `json:"baseDir"`
	ProxyHTTPPort      int         `json:"proxyHttpPort"`
	ProxyHTTPSPort     int         `json:"proxyHttpsPort"`
	MonitorIntervalSec int         `json:"monitorIntervalSec"`
	Status             string      `json:"status"`
	LastSeenAt         *time.Time  `json:"lastSeenAt,omitempty"`
	HasHostStats       bool        `json:"hasHostStats"`
	HostStats          *HostStats  `json:"hostStats,omitempty"`
	Containers         []Container `json:"containers"`
	ContainerCount     int         `json:"containerCount"`
	FetchedAt          *time.Time  `json:"fetchedAt,omitempty"`
	// Perf is the per-container perf ranking (fold-in); nil until the
	// monitor's first snapshot lands. Source "snapshot" — latest-sample
	// ranking, see ServerPerf.
	Perf *ServerPerf `json:"perf,omitempty"`
}

// FleetResponse is the GET /api/fleet payload. Servers is never null.
type FleetResponse struct {
	Servers []FleetServer `json:"servers"`
}

// FleetUpdateFrame is pushed on the WS after every snapshot that changes
// anything: {"type":"fleet.update","servers":[…]}.
type FleetUpdateFrame struct {
	Type    string        `json:"type"`
	Servers []FleetServer `json:"servers"`
}

// DeployLogFrame streams one deployment log line.
type DeployLogFrame struct {
	Type         string `json:"type"`
	DeploymentID string `json:"deploymentId"`
	Line         string `json:"line"`
	Done         bool   `json:"done"`
}

// DeployStatusFrame announces a deployment state transition.
type DeployStatusFrame struct {
	Type         string `json:"type"`
	DeploymentID string `json:"deploymentId"`
	Status       string `json:"status"`
}

// AlertFrame announces an alert rule transition ("firing" / "resolved").
type AlertFrame struct {
	Type   string `json:"type"`
	RuleID string `json:"ruleId"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}

// Error sample/group DTOs mirror httpapi's errors payload so the GUI's error
// page is the same component the loopback page is.

type ErrorSample struct {
	Host      string `json:"host"`
	Container string `json:"container"`
	Sev       string `json:"sev"`
	Stamp     string `json:"stamp,omitempty"`
	Text      string `json:"text"`
}

type ErrorGroup struct {
	Sig        string        `json:"sig"`
	Sev        string        `json:"sev"`
	Count      int           `json:"count"`
	Hosts      []string      `json:"hosts"`
	Containers []string      `json:"containers"`
	FirstSeen  time.Time     `json:"firstSeen"`
	LastSeen   time.Time     `json:"lastSeen"`
	Sample     *ErrorSample  `json:"sample,omitempty"`
	Examples   []ErrorSample `json:"examples"`
}

type ErrorHost struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Swept     bool      `json:"swept"`
	SweptAt   time.Time `json:"sweptAt"`
	LineCount int       `json:"lineCount"`
}

type ErrorsResponse struct {
	Swept     int          `json:"swept"`
	Connected int          `json:"connected"`
	Truncated bool         `json:"truncated"`
	FetchedAt time.Time    `json:"fetchedAt"`
	LineCount int          `json:"lineCount"`
	Hosts     []ErrorHost  `json:"hosts"`
	Groups    []ErrorGroup `json:"groups"`
}

// App/Deployment status literals shared by the daemon, TUI hub mode and GUI.
const (
	AppUndeployed = "undeployed"
	AppRunning    = "running"

	DepQueued   = "queued"
	DepRunning  = "running"
	DepSuccess  = "success"
	DepFailed   = "failed"
	DepCanceled = "canceled"

	TriggerManual   = "manual"
	TriggerWebhook  = "webhook"
	TriggerRollback = "rollback"
)

// App is one deployable application. SourceConfig/EnvVars/Domains mirror the
// store row's JSON columns verbatim.
type App struct {
	ID                 string            `json:"id"`
	EnvironmentID      string            `json:"environmentId"`
	ServerID           string            `json:"serverId"`
	Name               string            `json:"name"`
	SourceType         string            `json:"sourceType"`
	SourceConfig       map[string]any    `json:"sourceConfig"`
	EnvVars            map[string]string `json:"envVars"`
	Domains            []string          `json:"domains"`
	Port               int               `json:"port"`
	AutoDeploy         bool              `json:"autoDeploy"`
	WebhookToken       string            `json:"webhookToken"`
	LastGoodDeployment string            `json:"lastGoodDeployment,omitempty"`
	Status             string            `json:"status"`
	UpdatedAt          time.Time         `json:"updatedAt"`
}

// Project / Environment mirror the store rows (shared by the GUI tree view).
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

type Environment struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
}

// Deployment is one run of the deploy engine for an app.
type Deployment struct {
	ID         string     `json:"id"`
	AppID      string     `json:"appId"`
	Status     string     `json:"status"`
	Trigger    string     `json:"trigger"`
	Release    string     `json:"release"`
	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// Database is a managed database instance. Config carries the create-time
// knobs (publish, port); credentials are never here — connection details go
// through GET /api/databases/{id}/connection.
type Database struct {
	ID            string          `json:"id"`
	EnvironmentID string          `json:"environmentId"`
	ServerID      string          `json:"serverId"`
	DbType        string          `json:"dbType"`
	Name          string          `json:"name"`
	Config        map[string]any  `json:"config"`
	Status        string          `json:"status"`
	CreatedAt     time.Time       `json:"createdAt"`
	Backup        *BackupSchedule `json:"backup,omitempty"`
}

// BackupSchedule mirrors backup_schedules (admin CRUD + scheduler state).
type BackupSchedule struct {
	DatabaseID string     `json:"databaseId"`
	CronSpec   string     `json:"cronSpec"`
	Retention  int        `json:"retention"`
	LastRunAt  *time.Time `json:"lastRunAt,omitempty"`
}
