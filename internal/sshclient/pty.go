package sshclient

import (
	"context"
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"
)

// PtyConfig describes an interactive PTY session.
type PtyConfig struct {
	Command string
	Term    string
	Cols    int
	Rows    int
}

// Pty is an interactive remote session backed by a PTY. Bytes read from in are
// sent to the remote stdin; remote stdout+stderr (merged by the server-side
// PTY) are written to out.
type Pty struct {
	sess *ssh.Session
}

func (c *Conn) StartPty(ctx context.Context, cfg PtyConfig, in io.Reader, out io.Writer) (*Pty, error) {
	sess, err := c.client.NewSession()
	if err != nil {
		return nil, err
	}
	p := &Pty{sess: sess}

	// NOTE: RequestPty and WindowChange take height (rows) before width (cols).
	if err := sess.RequestPty(cfg.Term, cfg.Rows, cfg.Cols, nil); err != nil {
		sess.Close()
		return nil, fmt.Errorf("request pty: %w", err)
	}

	pw, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	go func() {
		// Copy errors here are normal teardown noise (session closed while
		// typing); Wait's result is authoritative.
		_, _ = io.Copy(pw, in)
		pw.Close()
	}()

	sess.Stdout = out
	sess.Stderr = out

	if err := sess.Start(cfg.Command); err != nil {
		sess.Close()
		return nil, fmt.Errorf("start command: %w", err)
	}

	go func() {
		select {
		case <-ctx.Done():
			_ = sess.Close()
		case <-doneChan(sess):
		}
	}()
	return p, nil
}

// doneChan closes when the session exits so the ctx watcher can stop early.
func doneChan(sess *ssh.Session) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		_ = sess.Wait()
		close(done)
	}()
	return done
}

// WindowChange forwards a terminal resize; rows before cols.
func (p *Pty) WindowChange(rows, cols int) error {
	return p.sess.WindowChange(rows, cols)
}

// Wait blocks until the remote process exits. A *ssh.ExitError carries the
// remote exit code.
func (p *Pty) Wait() error {
	return p.sess.Wait()
}

func (p *Pty) Close() error {
	return p.sess.Close()
}
