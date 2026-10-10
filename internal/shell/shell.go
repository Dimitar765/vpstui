// Package shell implements the vpstui _shell child process: it re-dials the
// host and bridges the local terminal to an interactive shell inside a
// container. The parent TUI suspends itself around this via tea.ExecProcess.
package shell

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"vpstui/internal/config"
	"vpstui/internal/models"
	"vpstui/internal/secret"
	"vpstui/internal/sshclient"
)

// Exit codes reserved by the child for setup failures. A remote shell exits
// through the PTY bridge with its own remote exit status, which may collide
// with these values; the parent distinguishes them by stderr content.
const (
	ExitArgs = 2
	ExitDial = 3
	ExitTTY  = 4
	ExitPty  = 5
)

var termPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func sanitizeTerm(s string) string {
	if termPattern.MatchString(s) {
		return s
	}
	return "xterm-256color"
}

func parseShellArgs(args []string) (hostID, cid string, err error) {
	fs := flag.NewFlagSet("_shell", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	host := fs.String("host", "", "host id")
	c := fs.String("cid", "", "container id")
	if err := fs.Parse(args); err != nil {
		return "", "", err
	}
	if *host == "" || *c == "" {
		return "", "", errors.New("--host and --cid are required")
	}
	return *host, *c, nil
}

func FindHost(hosts []models.Host, id string) (models.Host, error) {
	for _, h := range hosts {
		if h.ID == id {
			return h, nil
		}
	}
	return models.Host{}, fmt.Errorf("host %q not found in configuration", id)
}

// StoredSecret reads the persisted secret for h. Empty is valid for
// unencrypted keys and agent auth; a missing password is an error.
func StoredSecret(h models.Host) (string, error) {
	switch h.AuthType {
	case models.AuthPassword:
		s, err := secret.Get(h.PasswordRef())
		if err != nil {
			return "", err
		}
		if s == "" {
			return "", fmt.Errorf("no stored password for host %s", h.Name)
		}
		return s, nil
	case models.AuthKey:
		return secret.Get(h.PassphraseRef())
	default:
		return "", nil
	}
}

// FailureMessage maps a resume error plus captured child stderr to a single
// human-readable line.
func FailureMessage(err error, stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprintf("exited with code %d", ee.ExitCode())
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func Main(args []string) int {
	hostID, cid, err := parseShellArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vpstui _shell:", err)
		return ExitArgs
	}
	hosts, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "vpstui _shell:", err)
		return ExitArgs
	}
	h, err := FindHost(hosts, hostID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vpstui _shell:", err)
		return ExitArgs
	}
	s, err := StoredSecret(h)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vpstui _shell:", err)
		return ExitDial
	}
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancelDial()
	conn, err := sshclient.Dial(dialCtx, h, s)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vpstui _shell:", err)
		return ExitDial
	}
	defer conn.Close()

	code, err := bridge(conn, cid)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vpstui _shell:", err)
	}
	return code
}
