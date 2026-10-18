// Package proxy owns the per-server Traefik reverse proxy stack.
package proxy

import (
	"context"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"

	"vpstui/internal/deploy"
	"vpstui/internal/store"
)

// proxyRev is the revision of the rendered proxy compose stack. Bump it to
// force a re-upload + `compose up` on every managed server on their next
// deploy (Ensure compares it against the `vpstui.proxy.rev` compose label of
// the running traefik container).
// 3: pre-create acme.json with security_opt label=disable (SELinux).
// 2: socket-proxy gets security_opt label=disable and a :ro,Z socket mount
// (rootless podman/SELinux: HAProxy 503 SC without it).
const proxyRev = 3

// networkName is the shared docker network apps and Traefik join. The deploy
// engine also creates it before every deploy; ours is guarded and idempotent.
const networkName = "vpstui"

// acmeEmailEnv carries the daemon's --acme-email into Ensure: the seam
// signature (ctx, Runner, *sftp.Client, store.Server) has no email slot and is
// final, so cmd/vpstui-server/main.go exports the configured value here.
const acmeEmailEnv = "VPSTUI_ACME_EMAIL"

// defaultSocketPath is the docker socket the socket-proxy bind-mounts. Dev
// accommodation for rootless podman: set VPSTUI_PROXY_SOCKET_PATH (e.g.
// /run/user/1000/podman/podman.sock) on the daemon and it lands in the
// target's proxy .env, which the compose file interpolates.
const defaultSocketPath = "/var/run/docker.sock"

// Ensure idempotently brings up the vpstui-proxy Traefik + docker-socket-proxy
// stack on the server: it short-circuits when the running stack is already at
// proxyRev, otherwise it renders and atomically uploads docker-compose.yml +
// .env into <base_dir>/proxy, makes sure the shared network exists,
// pre-creates acme.json 0600 in the acme volume, and runs `compose up -d`.
// It runs inside every deploy before `compose up` of the app.
func Ensure(ctx context.Context, r deploy.Runner, cl *sftp.Client, s store.Server) error {
	baseDir, err := remoteBaseDir(ctx, r, s.BaseDir)
	if err != nil {
		return err
	}
	dir := path.Join(baseDir, "proxy")

	// Cheap path: a stack already running at our revision needs nothing.
	out, _, err := r.Exec(ctx, 15*time.Second, revProbeCmd)
	if err == nil && strings.TrimSpace(out) == strconv.Itoa(proxyRev) {
		return nil
	}

	if err := cl.MkdirAll(dir); err != nil {
		return fmt.Errorf("sftp mkdir %s: %w", dir, err)
	}
	if err := deploy.WriteFileAtomic(cl, path.Join(dir, "docker-compose.yml"), composeFile(s), 0o644); err != nil {
		return fmt.Errorf("upload proxy compose: %w", err)
	}
	if err := deploy.WriteFileAtomic(cl, path.Join(dir, ".env"), envFile(s), 0o600); err != nil {
		return fmt.Errorf("upload proxy env: %w", err)
	}

	// Shared network (engine repeats this per deploy; both are no-ops when it
	// already exists).
	if _, _, err := r.Exec(ctx, 15*time.Second, "docker network create "+networkName+" || true"); err != nil {
		return fmt.Errorf("create network: %w", err)
	}

	// Traefik refuses to start with a missing/world-accessible acme.json, so
	// pre-create it 0600 inside the named volume. No-op when it exists.
	// label=disable: on SELinux targets a fresh named volume is only writable
	// by a container with the matching MCS label (else `touch` fails — and
	// podman's docker-shim may still report rc 0).
	if e := acmeEmail(); e != "" {
		mk := "docker run --rm --security-opt label=disable -v vpstui-proxy_acme:/acme" +
			" --entrypoint sh " + shq("traefik:v3.3") + " -c 'umask 177; touch /acme/acme.json'"
		if _, _, err := r.Exec(ctx, 2*time.Minute, mk); err != nil {
			return fmt.Errorf("pre-create acme.json: %w", err)
		}
	}

	up := "docker compose -f " + shq(path.Join(dir, "docker-compose.yml")) + " up -d --remove-orphans"
	if _, _, err := r.Exec(ctx, 5*time.Minute, up); err != nil {
		return fmt.Errorf("proxy compose up: %w", err)
	}
	return nil
}

// revProbeCmd prints the vpstui.proxy.rev label of the traefik container of
// the vpstui-proxy compose project, or nothing when the stack is absent.
const revProbeCmd = `out=$(docker ps -a --filter label=vpstui.proxy.rev -q | head -n 1); ` +
	`if [ -n "$out" ]; then docker inspect --format '{{index .Config.Labels "vpstui.proxy.rev"}}' "$out"; fi`

// acmeEmail returns the daemon-configured ACME email ("" = TLS disabled).
func acmeEmail() string { return strings.TrimSpace(os.Getenv(acmeEmailEnv)) }

// socketPath honors VPSTUI_PROXY_SOCKET_PATH (rootless podman accommodation).
func socketPath() string {
	if p := os.Getenv("VPSTUI_PROXY_SOCKET_PATH"); p != "" {
		return p
	}
	return defaultSocketPath
}

// composeFile renders the proxy stack. Both ACME resolvers (tls + http
// challenge) are emitted when an email is configured; without one the
// resolver flags are omitted entirely — domains then serve plain HTTP on the
// web entrypoint (no silent cert failures).
func composeFile(s store.Server) []byte {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("# Rendered by vpstui-server (proxy rev %d) — edits are overwritten on the next deploy.\n", proxyRev)
	w("name: vpstui-proxy\n\nservices:\n")
	w("  traefik:\n")
	w("    image: traefik:v3.3\n")
	w("    restart: unless-stopped\n")
	w("    depends_on:\n      - socket-proxy\n")
	w("    command:\n")
	w("      - --providers.docker.endpoint=tcp://socket-proxy:2375\n")
	w("      - --providers.docker.exposedbydefault=false\n")
	w("      - --providers.docker.network=%s\n", networkName)
	w("      - --entrypoints.web.address=:${HTTP_PORT}\n")
	w("      - --entrypoints.websecure.address=:${HTTPS_PORT}\n")
	if e := acmeEmail(); e != "" {
		w("      - --certificatesresolvers.le.acme.tlschallenge=true\n")
		w("      - --certificatesresolvers.le.acme.httpchallenge=true\n")
		w("      - --certificatesresolvers.le.acme.httpchallenge.entrypoint=web\n")
		w("      - --certificatesresolvers.le.acme.email=${ACME_EMAIL}\n")
		w("      - --certificatesresolvers.le.acme.storage=/acme/acme.json\n")
	}
	w("    ports:\n")
	w("      - '${HTTP_PORT}:${HTTP_PORT}'\n")
	w("      - '${HTTPS_PORT}:${HTTPS_PORT}'\n")
	w("    volumes:\n      - acme:/acme\n")
	w("    networks:\n      - %s\n", networkName)
	w("    labels:\n      - 'vpstui.proxy.rev=%d'\n", proxyRev)
	w("  socket-proxy:\n")
	w("    image: tecnativa/docker-socket-proxy\n")
	w("    restart: unless-stopped\n")
	// Rootless-podman/SELinux targets (Fedora): the podman API socket never
	// carries the container's MCS label, so the proxy is denied read access
	// (HAProxy 503 SC). label=disable drops the label check; harmless on
	// non-SELinux docker hosts.
	w("    security_opt:\n      - label=disable\n")
	w("    environment:\n")
	w("      CONTAINERS: '1'\n")
	w("      NETWORKS: '1'\n")
	w("      SERVICES: '1'\n")
	w("      EVENTS: '1'\n")
	w("      POST: '0'\n")
	w("    volumes:\n")
	w("      # Docker socket for the control API. Rootless/podman targets: point\n")
	w("      # VPSTUI_PROXY_SOCKET_PATH at the user socket (daemon env). :Z relabels\n")
	w("      # the socket for SELinux (docker hosts without SELinux ignore it).\n")
	w("      - '${SOCKET_PATH}:/var/run/docker.sock:ro,Z'\n")
	w("    networks:\n      - %s\n", networkName)
	w("\nvolumes:\n  acme: {}\n")
	w("\nnetworks:\n  %s:\n    external: true\n", networkName)
	return []byte(b.String())
}

// envFile renders the proxy .env (written 0600): entrypoint ports from the
// server row, the docker socket path, and ACME_EMAIL only when TLS is on.
func envFile(s store.Server) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP_PORT=%d\n", s.ProxyHTTPPort)
	fmt.Fprintf(&b, "HTTPS_PORT=%d\n", s.ProxyHTTPSPort)
	fmt.Fprintf(&b, "SOCKET_PATH=%s\n", socketPath())
	if e := acmeEmail(); e != "" {
		fmt.Fprintf(&b, "ACME_EMAIL=%s\n", e)
	}
	return []byte(b.String())
}

// remoteBaseDir anchors a relative base_dir (~-prefixed) at the remote home,
// mirroring the engine's expandBaseDir (it lives there unexported).
func remoteBaseDir(ctx context.Context, r deploy.Runner, baseDir string) (string, error) {
	if strings.HasPrefix(baseDir, "/") {
		return strings.TrimSuffix(baseDir, "/"), nil
	}
	out, _, err := r.Exec(ctx, 10*time.Second, `printf %s "$HOME"`)
	if err != nil {
		return "", fmt.Errorf("resolve $HOME: %w", err)
	}
	return path.Join(strings.TrimSpace(out), strings.TrimPrefix(baseDir, "~/")), nil
}

// shq single-quotes a path for safe interpolation into a remote shell command.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
