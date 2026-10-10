package models

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
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
}

func (c Container) ShortName() string {
	return firstName(c.Names)
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
