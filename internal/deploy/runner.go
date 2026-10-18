// Package deploy is the vpstui-server's deploy engine: it renders compose
// files per source type, uploads release directories over SFTP and drives
// `docker compose` on the managed server through the Runner seam.
package deploy

import (
	"context"
	"io"
	"time"

	"vpstui/internal/docker"
)

// Runner is the command-execution seam the engine and proxy code run
// through. *docker.Executor satisfies it via Adapter; tests supply fakes.
type Runner interface {
	Exec(ctx context.Context, timeout time.Duration, cmd string) (stdout, stderr string, err error)
	Stream(ctx context.Context, cmd string) (io.ReadCloser, error)
}

// Adapter wraps a *docker.Executor as a Runner: the exported Run/RunStream
// passthroughs bound to the interface.
func Adapter(e *docker.Executor) Runner { return adapter{e} }

type adapter struct{ e *docker.Executor }

func (a adapter) Exec(ctx context.Context, timeout time.Duration, cmd string) (string, string, error) {
	return a.e.Run(ctx, timeout, cmd)
}

func (a adapter) Stream(ctx context.Context, cmd string) (io.ReadCloser, error) {
	return a.e.RunStream(ctx, cmd)
}
