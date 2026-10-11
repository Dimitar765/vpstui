package models

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	AuthPassword = "password"
	AuthKey      = "key"
	AuthAgent    = "agent"
)

type Host struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	AuthType string `json:"authType"`
	KeyPath  string `json:"keyPath,omitempty"`
}

func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (h Host) Target() string {
	return fmt.Sprintf("%s@%s:%d", h.User, h.Address, h.Port)
}

func (h Host) Addr() string {
	return fmt.Sprintf("%s:%d", h.Address, h.Port)
}

func (h Host) PasswordRef() string   { return h.ID + ":password" }
func (h Host) PassphraseRef() string { return h.ID + ":passphrase" }

// Tag is a label the user attaches to one container on one host. It lives only
// in vpstui's own config: the container's real docker name is never changed,
// and nothing about a tag ever reaches the daemon.
type Tag struct {
	HostID string    `json:"hostId"`
	Name   string    `json:"name"` // ShortName the label was written for
	Tag    string    `json:"tag"`
	Color  string    `json:"color"` // palette name, see app.TagColors
	ID     string    `json:"id"`    // container id at write time, informational
	SetAt  time.Time `json:"setAt"`
}

type Container struct {
	ID        string
	Names     string
	Image     string
	State     string
	Status    string
	CreatedAt string
	Labels    string
	Networks  []string // every attached network name, sorted, defaults included
}

func (c Container) ShortName() string {
	return firstName(c.Names)
}

// ComposeProject returns the value of the com.docker.compose.project label,
// or "" for standalone containers.
func (c Container) ComposeProject() string {
	const key = "com.docker.compose.project="
	for _, pair := range strings.Split(c.Labels, ",") {
		if v, ok := strings.CutPrefix(pair, key); ok {
			return v
		}
	}
	return ""
}

type ContainerDetail struct {
	Name         string
	Image        string
	Status       string
	Created      string
	RestartCount int
	Networks     []NetworkAddr // sorted by name
	Ports        []string      // "80/tcp -> 0.0.0.0:8080"
	Mounts       []string      // "bind: /host -> /ctr (rw)"
}

type NetworkAddr struct {
	Name      string
	IPAddress string
}

func firstName(names string) string {
	for i := 0; i < len(names); i++ {
		if names[i] == ',' {
			return names[:i]
		}
	}
	return names
}

type ContainerStats struct {
	ID       string
	Name     string
	CPUPerc  string
	MemUsage string
	MemPerc  string
}

type HostStats struct {
	Load1      float64
	Load5      float64
	Load15     float64
	MemTotalMB int
	MemUsedMB  int
	NumCPU     int
	UptimeSec  float64
}

// ContainerPeak is one container's all-time record, as the kernel's cgroup
// counters kept it — the peak that happened while nothing was watching.
//
// Every numeric field is Unknown when the host could not report it. Zero is a
// measurement ("this container has never been throttled"); absence is not, and
// rendering it as 0 would invent a clean bill of health.
type ContainerPeak struct {
	ID           string
	MemPeakBytes int64
	CPUUsec      int64  // cumulative CPU in microseconds
	Throttled    int64  // nr_throttled
	Periods      int64  // nr_periods
	StartedAt    string // docker's State.StartedAt, "" when unknown
}

// Unknown is the "no reading" marker for ContainerPeak's counters.
const Unknown int64 = -1

type DiskObject struct {
	Type        string
	Total       string
	Active      string
	Size        string
	Reclaimable string
}
