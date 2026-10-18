// Command loopback-sshd is a minimal SSH server for vpstui's loopback e2e
// target (127.0.0.1:2222 per localhost.md).
//
// Why it exists: this dev box runs SELinux enforcing with unreadable labels on
// core binaries, so a user-started OpenSSH sshd-session cannot exec /bin/bash
// ("Permission denied") and the historical loopback setup cannot be re-armed
// without root. This server runs as an ordinary user process (same domain as
// any local shell, which CAN exec), needs no root and no PAM, and speaks
// exactly what vpstui and vpstui-server use: `exec` channels (bash -c) and the
// `sftp` subsystem (pkg/sftp server side, also referenced by the deploy
// engine's integration harness).
//
// Scope rules honored per localhost.md: loopback only, pubkey auth only with a
// from=127.0.0.1-equivalent (we only ever bind 127.0.0.1), no PTY sessions
// (vpstui's interactive shell `e` is not part of e2e), no TCP forwarding.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:2222", "loopback listen address")
	hostKey := flag.String("hostkey", "", "path to PEM private host key (required)")
	authKeys := flag.String("authorized-keys", "", "path to authorized_keys file (required)")
	flag.Parse()
	if *hostKey == "" || *authKeys == "" {
		log.Fatal("-hostkey and -authorized-keys are required")
	}

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			ok, err := matchAuthorizedKey(*authKeys, key)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", *authKeys, err)
			}
			if !ok {
				log.Printf("rejected key for %q from %s", meta.User(), meta.RemoteAddr())
				return nil, fmt.Errorf("unknown public key")
			}
			return &ssh.Permissions{}, nil
		},
	}
	hostSigner, err := loadHostKey(*hostKey)
	if err != nil {
		log.Fatalf("host key: %v", err)
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("listen %s: %v", *listen, err)
	}
	log.Printf("loopback-sshd listening on %s", *listen)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go handleConn(conn, cfg)
	}
}

func loadHostKey(path string) (ssh.Signer, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(pem)
}

func matchAuthorizedKey(path string, key ssh.PublicKey) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			continue // options-prefixed or malformed lines are skipped
		}
		if ssh.FingerprintSHA256(pub) == ssh.FingerprintSHA256(key) {
			return true, nil
		}
	}
	return false, nil
}

func handleConn(conn net.Conn, cfg *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		log.Printf("handshake from %s: %v", conn.RemoteAddr(), err)
		return
	}
	defer sconn.Close()
	log.Printf("session from %s user=%q", sconn.RemoteAddr(), sconn.User())
	go ssh.DiscardRequests(reqs) // no tcpip-forward, no keepalive plumbing needed
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "only session channels")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go handleSession(ch, chReqs)
	}
}

// envPath mirrors a normal user login PATH plus ~/.local/bin first, so the
// docker shim (podman socket) resolves exactly as in an interactive shell.
const envPath = "/home/dimitar/.local/bin:/usr/local/bin:/usr/bin:/usr/sbin:/bin"

func handleSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "exec":
			var payload struct{ Command string }
			ssh.Unmarshal(req.Payload, &payload)
			_ = req.Reply(true, nil)
			exit := runCommand(ch, payload.Command)
			sendExit(ch, exit)
			return
		case "shell":
			_ = req.Reply(false, nil) // interactive shell not supported (no PTY in e2e)
		case "subsystem":
			var payload struct{ Name string }
			ssh.Unmarshal(req.Payload, &payload)
			if payload.Name != "sftp" {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			server, err := sftp.NewServer(ch) // serves the real filesystem as dimitar
			if err != nil {
				log.Printf("sftp server: %v", err)
				return
			}
			if err := server.Serve(); err != nil && err != io.EOF {
				log.Printf("sftp serve: %v", err)
			}
			_ = server.Close()
			return
		case "pty-req", "env":
			_ = req.Reply(req.Type == "env", nil) // tolerate env, refuse pty
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

func runCommand(ch ssh.Channel, command string) int {
	cmd := exec.Command("/bin/bash", "-c", command)
	cmd.Stdout = ch
	cmd.Stderr = ch.Stderr()
	cmd.Env = []string{
		"PATH=" + envPath,
		"HOME=/home/dimitar",
		"USER=dimitar",
		"SHELL=/bin/bash",
		"LANG=C.UTF-8",
	}
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintf(ch.Stderr(), "%v\n", err)
		return 127
	}
	return 0
}

func sendExit(ch ssh.Channel, code int) {
	payload := ssh.Marshal(struct{ Status uint32 }{uint32(code)})
	_, _ = ch.SendRequest("exit-status", false, payload)
	ch.CloseWrite() // half-close stdout; session closes when the client drains
}
