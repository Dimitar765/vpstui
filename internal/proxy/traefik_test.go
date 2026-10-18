package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"vpstui/internal/store"
)

// pipeSFTP wires an in-process pkg/sftp server to a client over net.Pipe,
// rooted at a temp dir via the server's working directory.
func pipeSFTP(t *testing.T) *sftp.Client {
	t.Helper()
	c1, c2 := net.Pipe()
	srv, err := sftp.NewServer(c1)
	if err != nil {
		t.Fatalf("sftp server: %v", err)
	}
	done := make(chan struct{})
	go func() { srv.Serve(); close(done) }()
	t.Cleanup(func() { c2.Close(); <-done })
	cl, err := sftp.NewClientPipe(c2, c2)
	if err != nil {
		t.Fatalf("sftp client: %v", err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl
}

// cmdRunner records Exec commands and answers the rev probe with revOut.
type cmdRunner struct {
	cmds   []string
	revOut string
}

func (f *cmdRunner) Exec(ctx context.Context, timeout time.Duration, cmd string) (string, string, error) {
	f.cmds = append(f.cmds, cmd)
	if strings.Contains(cmd, `index .Config.Labels "vpstui.proxy.rev"`) {
		return f.revOut, "", nil
	}
	return "", "", nil
}

func (f *cmdRunner) Stream(ctx context.Context, cmd string) (io.ReadCloser, error) {
	f.cmds = append(f.cmds, cmd)
	return nil, errors.New("not implemented")
}

func (f *cmdRunner) saw(substr string) bool {
	for _, c := range f.cmds {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func testServer() store.Server {
	return store.Server{ID: "srv1", Name: "loop", Address: "127.0.0.1", Port: 2222,
		User: "dimitar", AuthType: "key", BaseDir: "/tmp/vpstui-proxy-test",
		ProxyHTTPPort: 8080, ProxyHTTPSPort: 8443}
}

func uploadedFiles(t *testing.T, cl *sftp.Client) (string, []byte) {
	t.Helper()
	read := func(p string) []byte {
		f, err := cl.Open(p)
		if err != nil {
			t.Fatalf("open %s: %v", p, err)
		}
		defer f.Close()
		buf := make([]byte, 16384)
		n, _ := f.Read(buf)
		return buf[:n]
	}
	return string(read("/tmp/vpstui-proxy-test/proxy/docker-compose.yml")),
		read("/tmp/vpstui-proxy-test/proxy/.env")
}

func TestEnsureUploadsStack(t *testing.T) {
	t.Setenv("VPSTUI_ACME_EMAIL", "ops@example.com")
	t.Setenv("VPSTUI_PROXY_SOCKET_PATH", "/run/user/1000/podman/podman.sock")
	r := &cmdRunner{} // rev probe returns "" → no stack → full path
	cl := pipeSFTP(t)

	if err := Ensure(context.Background(), r, cl, testServer()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	comp, env := uploadedFiles(t, cl)

	for _, frag := range []string{
		"name: vpstui-proxy",
		"image: traefik:v3.3",
		"--providers.docker.endpoint=tcp://socket-proxy:2375",
		"--providers.docker.exposedbydefault=false",
		"--providers.docker.network=vpstui",
		"--entrypoints.web.address=:${HTTP_PORT}",
		"--entrypoints.websecure.address=:${HTTPS_PORT}",
		"--certificatesresolvers.le.acme.tlschallenge=true",
		"--certificatesresolvers.le.acme.httpchallenge=true",
		"--certificatesresolvers.le.acme.httpchallenge.entrypoint=web",
		"--certificatesresolvers.le.acme.email=${ACME_EMAIL}",
		"--certificatesresolvers.le.acme.storage=/acme/acme.json",
		"image: tecnativa/docker-socket-proxy",
		"CONTAINERS: '1'", "NETWORKS: '1'", "SERVICES: '1'", "EVENTS: '1'", "POST: '0'",
		"${SOCKET_PATH}:/var/run/docker.sock:ro,Z",
		"vpstui.proxy.rev=3",
		"vpstui:\n    external: true",
	} {
		if !strings.Contains(comp, frag) {
			t.Errorf("compose missing %q:\n%s", frag, comp)
		}
	}
	for _, want := range []string{"HTTP_PORT=8080\n", "HTTPS_PORT=8443\n",
		"SOCKET_PATH=/run/user/1000/podman/podman.sock\n", "ACME_EMAIL=ops@example.com\n"} {
		if !strings.Contains(string(env), want) {
			t.Errorf(".env missing %q: %q", want, env)
		}
	}

	// acme.json pre-created 0600 inside the named volume, network created,
	// compose up run.
	if !r.saw("umask 177; touch /acme/acme.json") {
		t.Error("acme.json pre-create command missing")
	}
	if !r.saw("docker network create vpstui") {
		t.Error("network create missing")
	}
	if !r.saw("docker compose -f '/tmp/vpstui-proxy-test/proxy/docker-compose.yml' up -d") {
		t.Error("compose up missing")
	}
}

func TestEnsureNoAcme(t *testing.T) {
	t.Setenv("VPSTUI_ACME_EMAIL", "")
	r := &cmdRunner{}
	cl := pipeSFTP(t)
	if err := Ensure(context.Background(), r, cl, testServer()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	comp, env := uploadedFiles(t, cl)
	if strings.Contains(comp, "certificatesresolvers") || strings.Contains(comp, "ACME_EMAIL") {
		t.Errorf("compose must omit ACME without email:\n%s", comp)
	}
	if strings.Contains(string(env), "ACME_EMAIL") {
		t.Errorf(".env must omit ACME_EMAIL: %q", env)
	}
	if r.saw("umask 177") {
		t.Error("acme.json pre-create must be skipped without email")
	}
	if !strings.Contains(string(env), "SOCKET_PATH=/var/run/docker.sock\n") {
		t.Errorf("default socket path missing: %q", env)
	}
}

func TestEnsureIdempotent(t *testing.T) {
	t.Setenv("VPSTUI_ACME_EMAIL", "")
	// Probe reports the current rev → Ensure must be a pure no-op.
	r := &cmdRunner{revOut: "3\n"}
	cl := pipeSFTP(t)
	if err := Ensure(context.Background(), r, cl, testServer()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(r.cmds) != 1 {
		t.Fatalf("second Ensure issued %d commands, want 1: %v", len(r.cmds), r.cmds)
	}
}

func TestEnsureRevBumpReuploads(t *testing.T) {
	t.Setenv("VPSTUI_ACME_EMAIL", "")
	// Stale rev on the running stack → full re-upload path.
	r := &cmdRunner{revOut: "0\n"}
	cl := pipeSFTP(t)
	if err := Ensure(context.Background(), r, cl, testServer()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !r.saw("up -d") {
		t.Error("stale rev must re-run compose up")
	}
}

func TestEnsureEnvFilePerms(t *testing.T) {
	t.Setenv("VPSTUI_ACME_EMAIL", "ops@example.com")
	cl := pipeSFTP(t)
	r := &cmdRunner{}
	if err := Ensure(context.Background(), r, cl, testServer()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// The sftp server is rooted at the process cwd; .env lands under /tmp
	// which resolves to the host tmp. Verify mode through the client.
	info, err := cl.Stat("/tmp/vpstui-proxy-test/proxy/.env")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf(".env perm = %v, want 0600", info.Mode().Perm())
	}
}
