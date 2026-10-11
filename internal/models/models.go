package models

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
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

type DiskObject struct {
	Type        string
	Total       string
	Active      string
	Size        string
	Reclaimable string
}
