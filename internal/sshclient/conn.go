package sshclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"vpstui/internal/config"
	"vpstui/internal/models"
)

type Conn struct {
	client    *ssh.Client
	keepStop  chan struct{}
	closeOnce sync.Once
}

func knownHostsPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "known_hosts"), nil
}

// Dial connects to the host. secret is the password (auth=password) or the
// private key passphrase (auth=key); unused for agent auth.
func Dial(ctx context.Context, h models.Host, secret string) (*Conn, error) {
	kPath, err := knownHostsPath()
	if err != nil {
		return nil, err
	}

	var toAccept []string
	if _, err := os.Stat(kPath); os.IsNotExist(err) {
		f, cerr := os.OpenFile(kPath, os.O_CREATE|os.O_WRONLY, 0600)
		if cerr != nil {
			return nil, cerr
		}
		f.Close()
	}
	base, err := knownhosts.New(kPath)
	if err != nil {
		return nil, fmt.Errorf("read known_hosts: %w", err)
	}
	hostKeyCallback := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := base(hostname, remote, key)
		if err == nil {
			return nil
		}
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) && len(ke.Want) == 0 {
			toAccept = append(toAccept, knownhosts.Line([]string{hostname}, key))
			return nil
		}
		return fmt.Errorf("host key for %s has CHANGED or is revoked (possible man-in-the-middle). "+
			"Verify the server and fix %s: %w", hostname, kPath, err)
	}

	auths, err := authMethods(h, secret)
	if err != nil {
		return nil, err
	}

	cfg := &ssh.ClientConfig{
		User:            h.User,
		Auth:            auths,
		HostKeyCallback: hostKeyCallback,
		Timeout:         15 * time.Second,
	}

	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", h.Addr())
	if err != nil {
		return nil, fmt.Errorf("tcp connect %s: %w", h.Addr(), err)
	}
	cc, chans, reqs, err := ssh.NewClientConn(nc, h.Addr(), cfg)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}
	client := ssh.NewClient(cc, chans, reqs)

	if len(toAccept) > 0 {
		f, err := os.OpenFile(kPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err == nil {
			for _, line := range toAccept {
				fmt.Fprintln(f, line)
			}
			f.Close()
		}
	}

	c := &Conn{client: client, keepStop: make(chan struct{})}
	go c.keepalive()
	return c, nil
}

func authMethods(h models.Host, secret string) ([]ssh.AuthMethod, error) {
	switch h.AuthType {
	case models.AuthPassword:
		if secret == "" {
			return nil, fmt.Errorf("no password provided")
		}
		return []ssh.AuthMethod{ssh.Password(secret)}, nil
	case models.AuthKey:
		if h.KeyPath == "" {
			return nil, fmt.Errorf("no private key path configured")
		}
		expanded := os.ExpandEnv(h.KeyPath)
		if home, err := os.UserHomeDir(); err == nil {
			if len(expanded) > 1 && expanded[0] == '~' && expanded[1] == '/' {
				expanded = filepath.Join(home, expanded[2:])
			}
		}
		data, err := os.ReadFile(expanded)
		if err != nil {
			return nil, fmt.Errorf("read private key: %w", err)
		}
		var signer ssh.Signer
		if secret != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(secret))
			if err != nil {
				return nil, fmt.Errorf("parse private key (wrong passphrase?): %w", err)
			}
		} else {
			signer, err = ssh.ParsePrivateKey(data)
		}
		var pm *ssh.PassphraseMissingError
		if errors.As(err, &pm) {
			return nil, fmt.Errorf("private key %s is passphrase-protected; provide the passphrase", expanded)
		}
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	case models.AuthAgent:
		sock := os.Getenv("SSH_AUTH_SOCK")
		if sock == "" {
			return nil, fmt.Errorf("SSH_AUTH_SOCK is not set; no ssh-agent available")
		}
		cl, err := net.Dial("unix", sock)
		if err != nil {
			return nil, fmt.Errorf("ssh-agent: %w", err)
		}
		signers, err := agent.NewClient(cl).Signers()
		if err != nil {
			return nil, fmt.Errorf("ssh-agent signers: %w", err)
		}
		if len(signers) == 0 {
			return nil, fmt.Errorf("ssh-agent has no identities")
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signers...)}, nil
	default:
		return nil, fmt.Errorf("unknown auth type %q", h.AuthType)
	}
}

func (c *Conn) keepalive() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if _, _, err := c.client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				c.Close()
				return
			}
		case <-c.keepStop:
			return
		}
	}
}

// Exec runs cmd and returns buffered stdout/stderr. ctx controls the timeout.
func (c *Conn) Exec(ctx context.Context, cmd string) (string, string, error) {
	sess, err := c.client.NewSession()
	if err != nil {
		return "", "", err
	}
	var out, errb bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &errb

	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()

	select {
	case <-ctx.Done():
		_ = sess.Close()
		return out.String(), errb.String(), ctx.Err()
	case err := <-done:
		_ = sess.Close()
		return out.String(), errb.String(), err
	}
}

type mergedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (m *mergedWriter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.w.Write(p)
}

type stream struct {
	rc     io.ReadCloser
	sess   *ssh.Session
	cancel context.CancelFunc
}

func (s *stream) Read(p []byte) (int, error) { return s.rc.Read(p) }
func (s *stream) Close() error               { s.cancel(); return s.sess.Close() }

// StreamExec runs cmd and streams combined stdout/stderr. The reader ends when
// ctx is canceled, the remote process exits, or the connection drops.
func (c *Conn) StreamExec(ctx context.Context, cmd string) (io.ReadCloser, error) {
	sess, err := c.client.NewSession()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	mw := &mergedWriter{w: pw}
	sess.Stdout = mw
	sess.Stderr = mw

	if err := sess.Start(cmd); err != nil {
		cancel()
		pw.Close()
		sess.Close()
		return nil, err
	}

	go func() {
		waitErr := make(chan error, 1)
		go func() { waitErr <- sess.Wait() }()
		select {
		case err := <-waitErr:
			if err != nil {
				pw.CloseWithError(err)
			} else {
				pw.Close()
			}
			cancel()
		case <-ctx.Done():
			_ = sess.Close()
			pw.Close()
		}
	}()

	return &stream{rc: pr, sess: sess, cancel: cancel}, nil
}

func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.keepStop)
		err = c.client.Close()
	})
	return err
}
