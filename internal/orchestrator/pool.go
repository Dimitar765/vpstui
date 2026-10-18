// Package orchestrator owns the daemon's live relationship with managed
// servers: a per-server SSH connection pool and the fleet monitor that keeps
// status, host stats, container snapshots and error samples fresh.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"vpstui/internal/docker"
	"vpstui/internal/models"
	"vpstui/internal/secret"
	"vpstui/internal/sshclient"
	"vpstui/internal/store"
)

// Client is the per-server surface the monitor and container actions need.
// *docker.Executor satisfies it; tests supply fakes.
type Client interface {
	Snapshot(ctx context.Context) ([]models.Container, []models.ContainerStats, models.HostStats, error)
	StreamEvents(ctx context.Context) (io.ReadCloser, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Restart(ctx context.Context, id string) error
	Rm(ctx context.Context, id string) error
	SweepErrors(ctx context.Context, since string, tail int) (string, error)
}

// Pool caches one SSH connection per managed server, redialing after a lost
// connection. Secrets are resolved through internal/secret at dial time —
// they never touch SQLite.
type Pool struct {
	db *store.DB

	mu     sync.Mutex
	conns  map[string]*sshclient.Conn
	dialFn func(ctx context.Context, h models.Host, secret string) (*sshclient.Conn, error)
}

// NewPool builds a pool over the store's server rows.
func NewPool(db *store.DB) *Pool {
	return &Pool{db: db, conns: map[string]*sshclient.Conn{}, dialFn: sshclient.Dial}
}

func (p *Pool) host(s store.Server) models.Host { return HostRow(s) }

// secretFor resolves the dial secret for a server row: the password for
// password auth, the key passphrase for key auth, nothing for agent auth.
func secretFor(s store.Server) (string, error) {
	switch s.AuthType {
	case models.AuthPassword:
		return secret.Get(s.ID + ":password")
	case models.AuthKey:
		return secret.Get(s.ID + ":passphrase")
	default: // agent
		return "", nil
	}
}

// SecretFor resolves the dial secret for a server row; exported for callers
// outside the package (the HTTP test probe dials without the pool).
func SecretFor(s store.Server) (string, error) { return secretFor(s) }

// HostRow maps a store server row onto the models.Host the SSH transport
// dials with. Exported for the same reason as SecretFor.
func HostRow(s store.Server) models.Host {
	return models.Host{
		ID: s.ID, Name: s.Name, Address: s.Address, Port: s.Port,
		User: s.User, AuthType: s.AuthType, KeyPath: s.KeyPath,
	}
}

// Conn returns a live connection for the server, dialing one if the cache is
// empty. Callers that observe sshclient.ErrConnLost must Pool.Remove the
// server so the next Conn redials.
func (p *Pool) Conn(ctx context.Context, s store.Server) (*sshclient.Conn, error) {
	p.mu.Lock()
	c := p.conns[s.ID]
	p.mu.Unlock()
	if c != nil {
		return c, nil
	}
	sec, err := secretFor(s)
	if err != nil {
		return nil, fmt.Errorf("resolve credentials for %s: %w", s.Name, err)
	}
	c, err = p.dialFn(ctx, p.host(s), sec)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", s.Name, err)
	}
	p.mu.Lock()
	// A concurrent dial may have won; keep one and close the loser.
	if old := p.conns[s.ID]; old != nil {
		c.Close()
		c = old
	} else {
		p.conns[s.ID] = c
	}
	p.mu.Unlock()
	return c, nil
}

// Executor returns a docker executor over the server's pooled connection.
func (p *Pool) Executor(ctx context.Context, s store.Server) (*docker.Executor, error) {
	c, err := p.Conn(ctx, s)
	if err != nil {
		return nil, err
	}
	return docker.New(c), nil
}

// Client returns the monitor's view of the server. The concrete type is the
// docker executor; the interface exists so tests can inject fakes.
func (p *Pool) Client(ctx context.Context, s store.Server) (Client, error) {
	return p.Executor(ctx, s)
}

// Remove drops (and closes) the cached connection for a server.
func (p *Pool) Remove(serverID string) {
	p.mu.Lock()
	c := p.conns[serverID]
	delete(p.conns, serverID)
	p.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// Close removes every cached connection.
func (p *Pool) Close() {
	p.mu.Lock()
	conns := p.conns
	p.conns = map[string]*sshclient.Conn{}
	p.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

// isConnLost reports whether err carries the typed connection-loss signal.
func isConnLost(err error) bool { return errors.Is(err, sshclient.ErrConnLost) }
