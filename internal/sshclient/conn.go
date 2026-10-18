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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"vpstui/internal/config"
	"vpstui/internal/models"
)

// ErrConnLost reports that the SSH connection died or was closed underneath an
// operation. Exec and StreamExec wrap their error with it; the app layer keys
// reconnect decisions on errors.Is instead of matching message text.
var ErrConnLost = errors.New("ssh: connection lost")

type Conn struct {
	client    *ssh.Client
	keepStop  chan struct{}
	closeOnce sync.Once
	closed    atomic.Bool
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
		if err != nil {
			client.Close()
			return nil, fmt.Errorf("first-use host key pin failed: %w", err)
		}
		for _, line := range toAccept {
			if _, err := fmt.Fprintln(f, line); err != nil {
				f.Close()
				client.Close()
				return nil, fmt.Errorf("first-use host key pin failed: %w", err)
			}
		}
		if err := f.Close(); err != nil {
			client.Close()
			return nil, fmt.Errorf("first-use host key pin failed: %w", err)
		}
	}

	c := &Conn{client: client, keepStop: make(chan struct{})}
	// Snapshot the pacing vars here — on the caller's goroutine — so the
	// keepalive goroutine never reads package vars that tests shrink while
	// earlier connections are still winding down.
	go c.keepalive(keepaliveEvery, probeTimeout, probeBudget)
	return c, nil
}

func authMethods(h models.Host, secret string) ([]ssh.AuthMethod, error) {
	switch h.AuthType {
	case models.AuthPassword:
		if secret == "" {
			return nil, fmt.Errorf("no password provided")
		}
		return []ssh.AuthMethod{
			ssh.Password(secret),
			// PAM hosts that reject plain "password" auth still accept the
			// same secret through keyboard-interactive. Every prompt gets the
			// password; a wrong one fails the round as usual.
			ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range questions {
					answers[i] = secret
				}
				return answers, nil
			}),
		}, nil
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
		// The socket stays open on success: the returned agent signers proxy
		// every Sign call through it during the handshake, so closing it here
		// would break agent auth. The error paths below close what they can.
		cl, err := net.DialTimeout("unix", sock, 5*time.Second)
		if err != nil {
			return nil, fmt.Errorf("ssh-agent: %w", err)
		}
		signers, err := agent.NewClient(cl).Signers()
		if err != nil {
			cl.Close()
			return nil, fmt.Errorf("ssh-agent signers: %w", err)
		}
		if len(signers) == 0 {
			cl.Close()
			return nil, fmt.Errorf("ssh-agent has no identities")
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signers...)}, nil
	default:
		return nil, fmt.Errorf("unknown auth type %q", h.AuthType)
	}
}

// Keepalive pacing; vars so tests can shrink them.
var (
	keepaliveEvery = 30 * time.Second
	probeTimeout   = 10 * time.Second
	probeBudget    = 2
)

// keepalive probes the transport every keepaliveEvery. Each probe gets
// probeTimeout to answer; probeBudget consecutive failures — timeout or
// error — close the connection, so a dead link is detected without a budget
// of one flaky probe, and the typed ErrConnLost signal reaches the session's
// next exec.
func (c *Conn) keepalive(every, timeout time.Duration, budget int) {
	t := time.NewTicker(every)
	defer t.Stop()
	failures := 0
	for {
		select {
		case <-t.C:
			res := make(chan error, 1)
			go func() {
				_, _, err := c.client.SendRequest("keepalive@openssh.com", true, nil)
				res <- err
			}()
			var err error
			select {
			case err = <-res:
			case <-time.After(timeout):
				err = errors.New("keepalive probe timed out")
			}
			if err != nil {
				failures++
				if failures >= budget {
					c.Close()
					return
				}
				continue
			}
			failures = 0
		case <-c.keepStop:
			return
		}
	}
}

// maxExecBytes bounds how much output Exec buffers per stream. Sweep output is
// re-capped (and truncation detected) downstream; this only keeps a chatty
// command from exhausting client memory inside its timeout window.
const maxExecBytes = 64 << 20

// limitWriter buffers up to max bytes and silently discards the rest.
type limitWriter struct {
	buf  bytes.Buffer
	max  int
	full bool
}

func newLimitWriter(max int) *limitWriter { return &limitWriter{max: max} }

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.full {
		return len(p), nil
	}
	n := len(p)
	if spare := w.max - w.buf.Len(); n > spare {
		n = spare
		w.full = true
	}
	w.buf.Write(p[:n])
	return len(p), nil
}

func (w *limitWriter) String() string { return w.buf.String() }

// isConnErr recognizes, textually, the transport failures ssh reports for a
// dead or closed connection. It backs ErrConnLost wrapping for drops the
// keepalive has not flagged yet — the closed flag only covers the reverse
// order. The list lives here, inside sshclient; the app layer never
// string-matches transport errors.
func isConnErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, frag := range []string{"connection lost", "eof", "use of closed", "broken pipe", "without exit status"} {
		if strings.Contains(s, frag) {
			return true
		}
	}
	return false
}

// connLost types err as ErrConnLost when the connection is closed or the error
// looks like a transport failure. nil passes through unchanged.
func (c *Conn) connLost(err error) error {
	if err == nil {
		return nil
	}
	if c.closed.Load() || isConnErr(err) {
		return fmt.Errorf("%w: %v", ErrConnLost, err)
	}
	return err
}

// SFTP opens an SFTP subsystem client over the wrapped connection; the
// caller owns closing it. Transport failures type as ErrConnLost like exec.
func (c *Conn) SFTP() (*sftp.Client, error) {
	cl, err := sftp.NewClient(c.client)
	if err != nil {
		return nil, c.connLost(fmt.Errorf("sftp: %w", err))
	}
	return cl, nil
}

// Exec runs cmd and returns buffered stdout/stderr. ctx controls the timeout.
func (c *Conn) Exec(ctx context.Context, cmd string) (string, string, error) {
	sess, err := c.client.NewSession()
	if err != nil {
		return "", "", c.connLost(err)
	}
	out, errb := newLimitWriter(maxExecBytes), newLimitWriter(maxExecBytes)
	sess.Stdout = out
	sess.Stderr = errb

	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()

	select {
	case <-ctx.Done():
		_ = sess.Close()
		// sess.Close makes the Run goroutine return, but it may still be
		// writing into the buffers; drain with a grace period so the reads
		// below never race a live write.
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		return out.String(), errb.String(), c.connLost(ctx.Err())
	case err := <-done:
		_ = sess.Close()
		return out.String(), errb.String(), c.connLost(err)
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
		return nil, c.connLost(err)
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
		return nil, c.connLost(err)
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
		c.closed.Store(true)
		close(c.keepStop)
		err = c.client.Close()
	})
	return err
}
