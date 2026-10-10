//go:build !unix

package shell

import (
	"errors"

	"vpstui/internal/sshclient"
)

func bridge(conn *sshclient.Conn, cid string) (int, error) {
	return ExitTTY, errors.New("interactive shell is not supported on this platform")
}
