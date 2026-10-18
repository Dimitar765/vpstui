package sshclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"vpstui/internal/models"
)

// testServer is a minimal SSH server on a loopback listener, built only from
// x/crypto/ssh, exercising the real Dial/Exec/StreamExec/keepalive paths.
type testServer struct {
	t          *testing.T
	ln         net.Listener
	addr       string
	port       int
	failProbes int // never answer the first N keepalive global requests
	// stallProbes delays the first N probes' replies past the client's
	// probeTimeout, so exactly one keepalive failure registers and later
	// probes recover. A never-answered probe cannot be used for this: it
	// holds x/crypto's global-request mutex forever and wedges all later
	// probes, which the budget test relies on for the dead-network case.
	stallProbes int
	probesSeen  atomic.Int64

	// hold, when non-nil, makes every exec handler keep its channel open
	// after writing its output — no exit-status, no close — until the test
	// closes it. Stream tests use this to read from a live channel.
	hold chan struct{}

	// clientKeyPath is the private key the server accepts, set for auth=key.
	clientKeyPath string

	mu    sync.Mutex
	conns []net.Conn

	closeOnce sync.Once
	closeAll  func() // drops every connection abruptly
}

func newHostKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}
	return signer
}

// writeClientKey generates an unencrypted ed25519 key, writes it to a temp
// file in OpenSSH PEM form and returns the path plus the matching public key.
func writeClientKey(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "test")
	if err != nil {
		t.Fatalf("marshal client key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatalf("write client key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("client signer: %v", err)
	}
	return path, signer.PublicKey()
}

// startTestServer builds the server on a fresh loopback port. auth is
// "password", "key" or "interactive" and is the ONLY auth method offered.
// exec handles an exec request and returns stdout, stderr, exit code.
func startTestServer(t *testing.T, auth string, exec func(cmd string) (string, string, uint32)) *testServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return startTestServerOn(t, ln, auth, exec)
}

func startTestServerOn(t *testing.T, ln net.Listener, auth string, exec func(cmd string) (string, string, uint32)) *testServer {
	t.Helper()
	s := &testServer{t: t, ln: ln}
	s.addr = ln.Addr().String()
	if ap, ok := ln.Addr().(*net.TCPAddr); ok {
		s.port = ap.Port
	}

	cfg := &ssh.ServerConfig{}
	cfg.AddHostKey(newHostKey(t)) // regenerated per server: TOFU tests need distinct keys
	switch auth {
	case "password":
		cfg.PasswordCallback = func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) == "pw" {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("wrong password")
		}
	case "key":
		path, wantKey := writeClientKey(t)
		s.clientKeyPath = path
		cfg.PublicKeyCallback = func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if key.Type() == wantKey.Type() && bytes.Equal(key.Marshal(), wantKey.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("unknown public key")
		}
	case "interactive":
		cfg.KeyboardInteractiveCallback = func(conn ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			answers, err := client("auth", "", []string{"Password:"}, []bool{false})
			if err != nil {
				return nil, err
			}
			if len(answers) == 1 && answers[0] == "pw" {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("wrong answer")
		}
	default:
		t.Fatalf("unknown auth mode %q", auth)
	}

	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, nc)
			s.mu.Unlock()
			go s.serve(nc, cfg, exec)
		}
	}()

	s.closeAll = func() {
		s.closeOnce.Do(func() {
			ln.Close()
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, nc := range s.conns {
				nc.Close()
			}
			s.conns = nil
		})
	}
	t.Cleanup(s.closeAll)
	return s
}

func (s *testServer) host() models.Host {
	return models.Host{ID: "srv", Name: "srv", Address: "127.0.0.1", Port: s.port, User: "test", AuthType: models.AuthPassword}
}

func (s *testServer) serve(nc net.Conn, cfg *ssh.ServerConfig, exec func(cmd string) (string, string, uint32)) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go s.serveGlobals(reqs)
	for newCh := range chans {
		go s.handleChannel(newCh, exec)
	}
}

func (s *testServer) serveGlobals(reqs <-chan *ssh.Request) {
	for req := range reqs {
		if req.Type == "keepalive@openssh.com" {
			seen := int(s.probesSeen.Add(1)) - 1
			if seen < s.failProbes {
				// Refused = never answered: SendRequest can only time out.
				// An explicit Reply(false) would look healthy to the client.
				continue
			}
			if seen < s.stallProbes {
				time.Sleep(probeTimeout + 10*time.Millisecond)
			}
			req.Reply(true, nil)
			continue
		}
		req.Reply(true, nil)
	}
}

func (s *testServer) handleChannel(newCh ssh.NewChannel, exec func(string) (string, string, uint32)) {
	ch, reqs, err := newCh.Accept()
	if err != nil {
		return
	}
	defer ch.Close()
	for req := range reqs {
		// pkg/sftp's client opens a session channel and requests the sftp
		// subsystem on it; serve sftp over that same channel.
		if req.Type == "subsystem" {
			if len(req.Payload) >= 4 && string(req.Payload[4:]) == "sftp" {
				req.Reply(true, nil)
				srv, err := sftp.NewServer(ch)
				if err == nil {
					srv.Serve()
				}
				return
			}
			req.Reply(false, nil)
			continue
		}
		if req.Type != "exec" {
			if req.WantReply {
				req.Reply(true, nil)
			}
			continue
		}
		// x/crypto exports no exec-payload struct: 4-byte big-endian length,
		// then the command string.
		if len(req.Payload) < 4 {
			req.Reply(false, nil)
			continue
		}
		n := binary.BigEndian.Uint32(req.Payload[:4])
		if int(n) > len(req.Payload[4:]) {
			req.Reply(false, nil)
			continue
		}
		cmd := string(req.Payload[4 : 4+n])
		req.Reply(true, nil)
		stdout, stderr, code := exec(cmd)
		if stdout != "" {
			ch.Write([]byte(stdout))
		}
		if stderr != "" {
			ch.Stderr().Write([]byte(stderr))
		}
		if s.hold != nil {
			<-s.hold
		}
		ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{code}))
		ch.Close()
		return
	}
}

func dialCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func TestDialPassword(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := startTestServer(t, "password", func(string) (string, string, uint32) { return "", "", 0 })

	ctx, cancel := dialCtx()
	defer cancel()
	conn, err := Dial(ctx, s.host(), "pw")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	conn.Close()
}

func TestDialWrongPassword(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := startTestServer(t, "password", func(string) (string, string, uint32) { return "", "", 0 })

	ctx, cancel := dialCtx()
	defer cancel()
	_, err := Dial(ctx, s.host(), "nope")
	if err == nil || !strings.Contains(err.Error(), "ssh handshake") {
		t.Fatalf("err = %v, want ssh handshake failure", err)
	}
}

func TestDialKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := startTestServer(t, "key", func(string) (string, string, uint32) { return "", "", 0 })

	h := s.host()
	h.AuthType = models.AuthKey
	h.KeyPath = s.clientKeyPath
	ctx, cancel := dialCtx()
	defer cancel()
	conn, err := Dial(ctx, h, "")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	conn.Close()
}

func TestDialKeyboardInteractiveOnly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := startTestServer(t, "interactive", func(string) (string, string, uint32) { return "", "", 0 })

	// The app's password method list offers Password then KeyboardInteractive;
	// the server accepts only the latter with the same secret.
	ctx, cancel := dialCtx()
	defer cancel()
	conn, err := Dial(ctx, s.host(), "pw")
	if err != nil {
		t.Fatalf("Dial via keyboard-interactive fallback: %v", err)
	}
	conn.Close()
}

func TestExecOutputExitStatus(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := startTestServer(t, "password", func(string) (string, string, uint32) {
		return "out\n", "err2\n", 0
	})

	ctx, cancel := dialCtx()
	defer cancel()
	conn, err := Dial(ctx, s.host(), "pw")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	out, errOut, err := conn.Exec(ctx, "anything")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if out != "out\n" || errOut != "err2\n" {
		t.Fatalf("Exec = %q, %q", out, errOut)
	}

	// A second server exiting non-zero reports the failure but still hands
	// back whatever output arrived.
	s2 := startTestServer(t, "password", func(string) (string, string, uint32) {
		return "partial\n", "", 1
	})
	conn2, err := Dial(ctx, s2.host(), "pw")
	if err != nil {
		t.Fatalf("Dial 2: %v", err)
	}
	defer conn2.Close()
	out, _, err = conn2.Exec(ctx, "anything")
	if err == nil {
		t.Fatal("exit 1 did not produce an error")
	}
	if out != "partial\n" {
		t.Fatalf("output after failure = %q, want partial\n", out)
	}
}

func TestExecTimeoutPrompt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := startTestServer(t, "password", func(string) (string, string, uint32) {
		time.Sleep(5 * time.Second)
		return "", "", 0
	})

	ctx, cancel := dialCtx()
	defer cancel()
	conn, err := Dial(ctx, s.host(), "pw")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	start := time.Now()
	ectx, ecancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer ecancel()
	_, _, err = conn.Exec(ectx, "slow")
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed >= 3*time.Second {
		t.Fatalf("Exec returned after %v, want well under the remote 5s", elapsed)
	}
}

func TestExecConnLost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	s := startTestServer(t, "password", func(string) (string, string, uint32) {
		<-block
		return "", "", 0
	})

	ctx, cancel := dialCtx()
	defer cancel()
	conn, err := Dial(ctx, s.host(), "pw")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	done := make(chan error, 1)
	go func() {
		_, _, err := conn.Exec(ctx, "blocks")
		done <- err
	}()
	// Give the exec a moment to reach the server, then drop the transport.
	time.Sleep(100 * time.Millisecond)
	s.closeAll()

	select {
	case err := <-done:
		if !errors.Is(err, ErrConnLost) {
			t.Fatalf("err = %v, want ErrConnLost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Exec did not return after the connection dropped")
	}
}

func TestStreamExecCloses(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	// The handler writes "a\nb\n" and then holds the channel open (no
	// exit-status, no close), so the client's reader stays live until Close.
	s := startTestServer(t, "password", func(string) (string, string, uint32) {
		return "a\nb\n", "", 0
	})
	s.hold = hold

	ctx, cancel := dialCtx()
	defer cancel()
	conn, err := Dial(ctx, s.host(), "pw")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	r, err := conn.StreamExec(ctx, "stream")
	if err != nil {
		t.Fatalf("StreamExec: %v", err)
	}
	var got []string
	buf := make([]byte, 1)
	for len(got) < 2 {
		var line []byte
		for {
			n, err := r.Read(buf)
			if n > 0 {
				if buf[0] == '\n' {
					break
				}
				line = append(line, buf[0])
			}
			if err != nil {
				t.Fatalf("read line %d: %v", len(got), err)
			}
		}
		got = append(got, string(line))
	}
	if got[0] != "a" || got[1] != "b" {
		t.Fatalf("streamed %q, want [a b]", got)
	}

	// A double close during teardown may surface EOF; the contract that
	// matters is that the reader is dead afterwards.
	_ = r.Close()
	if _, err := r.Read(buf); err == nil {
		t.Fatal("Read after Close returned no error")
	}
}

func TestKeepaliveBudget(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	oldEvery, oldTimeout, oldBudget := keepaliveEvery, probeTimeout, probeBudget
	// probeTimeout below one keepalive interval: each probe resolves before
	// the next tick, so refused/answered results never interleave.
	keepaliveEvery, probeTimeout, probeBudget = 60*time.Millisecond, 25*time.Millisecond, 2
	t.Cleanup(func() { keepaliveEvery, probeTimeout, probeBudget = oldEvery, oldTimeout, oldBudget })

	ctx, cancel := dialCtx()
	defer cancel()

	// Every probe refused: two consecutive failures must close the connection.
	s := startTestServer(t, "password", func(string) (string, string, uint32) { return "", "", 0 })
	s.failProbes = 1000
	conn, err := Dial(ctx, s.host(), "pw")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !conn.closed.Load() {
		time.Sleep(10 * time.Millisecond)
	}
	if !conn.closed.Load() {
		t.Fatal("connection stayed open after two refused probes")
	}
	conn.Close()

	// A single failed probe must not kill the connection: the first reply is
	// delayed past probeTimeout (one registered failure), later probes answer.
	s2 := startTestServer(t, "password", func(string) (string, string, uint32) { return "", "", 0 })
	s2.stallProbes = 1
	conn2, err := Dial(ctx, s2.host(), "pw")
	if err != nil {
		t.Fatalf("Dial 2: %v", err)
	}
	defer conn2.Close()
	// ≥ 7 intervals: probe 1 timed out (failure 1), probe 2 answered (reset),
	// so the budget never reached 2.
	time.Sleep(7 * 60 * time.Millisecond)
	if conn2.closed.Load() {
		t.Fatal("single failed probe killed the connection")
	}
	if _, _, err := conn2.Exec(ctx, "still-alive"); err != nil {
		t.Fatalf("Exec after one failed probe: %v", err)
	}
}

func TestTOFUPinsThenChangedKeyBlocks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	simple := func(string) (string, string, uint32) { return "", "", 0 }
	a := startTestServer(t, "password", simple)
	ctx, cancel := dialCtx()
	defer cancel()

	conn, err := Dial(ctx, a.host(), "pw")
	if err != nil {
		t.Fatalf("first Dial: %v", err)
	}
	conn.Close()

	kPath, err := knownHostsPath()
	if err != nil {
		t.Fatalf("knownHostsPath: %v", err)
	}
	pins, err := os.ReadFile(kPath)
	if err != nil || len(strings.TrimSpace(string(pins))) == 0 {
		t.Fatalf("known_hosts after first dial: %q (err %v)", pins, err)
	}
	if got := len(strings.Split(strings.TrimSpace(string(pins)), "\n")); got != 1 {
		t.Fatalf("known_hosts has %d lines, want 1", got)
	}

	// Same port, fresh host key: the pinned entry must block the dial.
	a.closeAll()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", a.port))
	if err != nil {
		t.Fatalf("re-listen on %d: %v", a.port, err)
	}
	b := startTestServerOn(t, ln, "password", simple)

	_, err = Dial(ctx, b.host(), "pw")
	if err == nil || !strings.Contains(err.Error(), "CHANGED") {
		t.Fatalf("err = %v, want changed-host-key error", err)
	}
}

func TestTOFUPinAppendFailure(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	// known_hosts as an unwritable file: knownhosts.New still parses it (it
	// is empty but readable), while the first-use append fails. A directory
	// named known_hosts would fail earlier, inside knownhosts.New itself.
	vpsDir := filepath.Join(cfgDir, "vpstui")
	if err := os.MkdirAll(vpsDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	kh := filepath.Join(vpsDir, "known_hosts")
	if err := os.WriteFile(kh, nil, 0400); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	s := startTestServer(t, "password", func(string) (string, string, uint32) { return "", "", 0 })
	ctx, cancel := dialCtx()
	defer cancel()
	_, err := Dial(ctx, s.host(), "pw")
	if err == nil || !strings.Contains(err.Error(), "first-use host key pin failed") {
		t.Fatalf("err = %v, want first-use host key pin failed", err)
	}
}
