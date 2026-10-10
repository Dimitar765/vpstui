//go:build unix

package shell

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"vpstui/internal/docker"
	"vpstui/internal/sshclient"
)

// bridge runs the interactive session and blocks until the remote shell
// exits. It returns (exitCode, err); a normal remote exit yields the remote
// exit status and a nil error.
func bridge(conn *sshclient.Conn, cid string) (int, error) {
	fd := int(os.Stdin.Fd())
	cols, rows, err := term.GetSize(fd)
	if err != nil {
		return ExitTTY, errors.New("stdin is not a terminal")
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return ExitTTY, err
	}
	defer term.Restore(fd, state)

	t := sanitizeTerm(os.Getenv("TERM"))
	p, err := conn.StartPty(context.Background(), sshclient.PtyConfig{
		Command: docker.ShellCommand(cid, t),
		Term:    t,
		Cols:    cols,
		Rows:    rows,
	}, os.Stdin, os.Stdout)
	if err != nil {
		return ExitPty, err
	}

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			c, r, err := term.GetSize(fd)
			if err != nil {
				continue
			}
			// WindowChange takes rows before cols; failures after Wait are
			// harmless closed-channel noise and are ignored.
			_ = p.WindowChange(r, c)
		}
	}()

	err = p.Wait()
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		return ee.ExitStatus(), nil // remote shell exit code; 0 on normal exit
	}
	if err != nil {
		return ExitPty, err // transport dropped mid-session
	}
	return 0, nil
}
