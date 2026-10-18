package deploy

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
	"testing"
	"time"

	"github.com/pkg/sftp"

	"vpstui/internal/store"
)

// pipeSFTP wires an in-process pkg/sftp server to a pkg/sftp client over a
// net.Pipe — no SSH involved; the engine's SFTP calls run against real
// protocol handling.
func pipeSFTP(t *testing.T) *sftp.Client {
	t.Helper()
	c1, c2 := net.Pipe()
	srv, err := sftp.NewServer(c1)
	if err != nil {
		t.Fatalf("sftp server: %v", err)
	}
	done := make(chan struct{})
	go func() {
		srv.Serve()
		close(done)
	}()
	t.Cleanup(func() {
		c2.Close()
		<-done
	})
	cl, err := sftp.NewClientPipe(c2, c2)
	if err != nil {
		t.Fatalf("sftp client: %v", err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl
}

func TestWriteFileAtomic(t *testing.T) {
	cl := pipeSFTP(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")

	if err := WriteFileAtomic(cl, path, []byte("first\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "first\n" {
		t.Fatalf("read back: %q %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, want 0600", info.Mode().Perm())
	}

	// Overwrite: no .tmp left behind, content replaced.
	if err := WriteFileAtomic(cl, path, []byte("second\n"), 0o600); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "second\n" {
		t.Fatalf("after overwrite: %q", got)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
}

func TestUploadDir(t *testing.T) {
	cl := pipeSFTP(t)
	local := t.TempDir()
	if err := os.MkdirAll(filepath.Join(local, "assets"), 0o750); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(local, "index.html"), []byte("<html></html>\n"), 0o644)
	os.WriteFile(filepath.Join(local, "assets", "app.js"), []byte("console.log(1)\n"), 0o640)

	remote := filepath.Join(t.TempDir(), "public")
	if err := UploadDir(cl, os.DirFS(local), remote); err != nil {
		t.Fatalf("upload: %v", err)
	}
	for _, p := range []string{"index.html", filepath.Join("assets", "app.js")} {
		b, err := os.ReadFile(filepath.Join(remote, p))
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if len(b) == 0 {
			t.Fatalf("%s: empty", p)
		}
	}
	info, err := os.Stat(filepath.Join(remote, "assets", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("perm = %v, want 0640", info.Mode().Perm())
	}
}

// fakeRunner answers Exec by substring and records every command. Stream
// emits canned lines.
type fakeRunner struct {
	mu      sync.Mutex
	cmds    []string
	psOut   string // docker compose ps NDJSON
	failCmd string // return error for commands containing this
}

func (f *fakeRunner) record(cmd string) {
	f.mu.Lock()
	f.cmds = append(f.cmds, cmd)
	f.mu.Unlock()
}

func (f *fakeRunner) saw(substr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cmds {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func (f *fakeRunner) Exec(ctx context.Context, timeout time.Duration, cmd string) (string, string, error) {
	f.record(cmd)
	if f.failCmd != "" && strings.Contains(cmd, f.failCmd) {
		return "", "boom", errors.New("exit status 1")
	}
	switch {
	case strings.Contains(cmd, "ps --format json"):
		return f.psOut, "", nil
	case strings.Contains(cmd, "git clone"):
		return "Cloning into 'repo'...\n", "", nil
	case strings.Contains(cmd, "readlink -f"):
		return "", "", errors.New("no current")
	}
	return "", "", nil
}

func (f *fakeRunner) Stream(ctx context.Context, cmd string) (io.ReadCloser, error) {
	f.record(cmd)
	if f.failCmd != "" && strings.Contains(cmd, f.failCmd) {
		return nil, errors.New("up failed")
	}
	return io.NopCloser(strings.NewReader(" Pulling app \x1b[1m\n \x1b[0m Started\n")), nil
}

// removingRunner is a fakeRunner whose rm -rf commands really run.
type removingRunner struct{ fakeRunner }

func (f *removingRunner) Exec(ctx context.Context, timeout time.Duration, cmd string) (string, string, error) {
	f.record(cmd)
	if after, ok := strings.CutPrefix(cmd, "rm -rf "); ok {
		p := strings.Trim(strings.TrimSpace(after), "'")
		return "", "", os.RemoveAll(p)
	}
	return f.fakeRunner.Exec(ctx, timeout, cmd)
}

func testApp(id, sourceType string, cfg map[string]any) store.App {
	return store.App{
		ID: id, EnvironmentID: "env1", ServerID: "srv1", Name: "demo",
		SourceType: sourceType, SourceConfig: cfg,
		EnvVars: map[string]string{"FOO": "bar"}, Domains: []string{"demo.example.com"},
		Port: 80, AutoDeploy: true, Status: "undeployed", UpdatedAt: time.Now().UTC(),
	}
}

func imageApp(id string) store.App {
	return testApp(id, "image", map[string]any{"image": "docker.io/library/nginx:1.27-alpine"})
}

// runFakeRelease drives deployBody end to end with the fake runner against a
// pipe SFTP rooted at tmp, using the given app + release.
func runFakeRelease(t *testing.T, r Runner, app store.App, release string, dataDir ...string) (*lineLogger, string) {
	t.Helper()
	cl := pipeSFTP(t)
	base := t.TempDir()
	dep := store.Deployment{ID: "dep1", AppID: app.ID, Status: "running", Trigger: "manual",
		Release: release, LogFile: filepath.Join(t.TempDir(), "dep1.log"), CreatedAt: time.Now().UTC()}
	dir := t.TempDir()
	if len(dataDir) > 0 {
		dir = dataDir[0]
	}
	eng := &Engine{dataDir: dir}
	lg := &lineLogger{eng: eng, dep: dep, f: openLog(dep.LogFile)}
	t.Cleanup(lg.close)
	server := store.Server{ID: "srv1", BaseDir: base}
	if err := eng.deployBody(context.Background(), dep, app, server, r, cl, lg); err != nil {
		t.Fatalf("deployBody: %v", err)
	}
	return lg, base
}

func TestDeployBodySuccessImage(t *testing.T) {
	r := &fakeRunner{psOut: `{"Name":"vpstui-a1-app","State":"running","Health":""}` + "\n"}
	_, base := runFakeRelease(t, r, imageApp("a1"), "rel1")

	// release tree + compose + .env + current symlink
	rel := filepath.Join(base, "apps", "a1", "releases", "rel1")
	b, err := os.ReadFile(filepath.Join(rel, "compose.yaml"))
	if err != nil {
		t.Fatalf("compose.yaml: %v", err)
	}
	if !bytes.Contains(b, []byte("image: 'docker.io/library/nginx:1.27-alpine'")) ||
		!bytes.Contains(b, []byte("Host(`demo.example.com`)")) ||
		!bytes.Contains(b, []byte("external: true")) {
		t.Fatalf("compose render wrong:\n%s", b)
	}
	envB, err := os.ReadFile(filepath.Join(rel, ".env"))
	if err != nil || string(envB) != "FOO=bar\n" {
		t.Fatalf(".env = %q %v", envB, err)
	}
	if info, _ := os.Stat(filepath.Join(rel, ".env")); info.Mode().Perm() != 0o600 {
		t.Fatalf(".env perm %v", info.Mode().Perm())
	}
	target, err := os.Readlink(filepath.Join(base, "apps", "a1", "current"))
	if err != nil || target != filepath.Join("releases", "rel1") {
		t.Fatalf("current -> %q (%v)", target, err)
	}
	if !r.saw("pull --ignore-pullable") || !r.saw("up -d --remove-orphans") {
		t.Fatalf("missing compose phases; cmds: %v", r.cmds)
	}
}

func TestDeployBodyVerifyFailure(t *testing.T) {
	r := &fakeRunner{psOut: `{"Name":"vpstui-a1-app","State":"exited","Health":""}` + "\n"}
	cl := pipeSFTP(t)
	base := t.TempDir()
	dep := store.Deployment{ID: "dep1", AppID: "a1", Status: "running", Trigger: "manual",
		Release: "rel1", LogFile: filepath.Join(t.TempDir(), "dep1.log"), CreatedAt: time.Now().UTC()}
	eng := &Engine{dataDir: t.TempDir()}
	lg := &lineLogger{eng: eng, dep: dep, f: openLog(dep.LogFile)}
	t.Cleanup(lg.close)
	err := eng.deployBody(context.Background(), dep, imageApp("a1"), store.Server{ID: "srv1", BaseDir: base}, r, cl, lg)
	if err == nil {
		t.Fatal("expected verification failure")
	}
	logB, _ := os.ReadFile(dep.LogFile)
	if !strings.Contains(string(logB), "previous containers (if any) keep running") {
		t.Fatalf("log footer missing:\n%s", logB)
	}
}

func TestDeployBodyDockerfileProbe(t *testing.T) {
	r := &fakeRunner{psOut: `{"State":"running"}` + "\n", failCmd: "command -v git"}
	cl := pipeSFTP(t)
	dep := store.Deployment{ID: "d2", AppID: "a2", Status: "running", Trigger: "manual",
		Release: "rel1", LogFile: filepath.Join(t.TempDir(), "d2.log"), CreatedAt: time.Now().UTC()}
	eng := &Engine{dataDir: t.TempDir()}
	lg := &lineLogger{eng: eng, dep: dep, f: openLog(dep.LogFile)}
	t.Cleanup(lg.close)
	err := eng.deployBody(context.Background(), dep,
		testApp("a2", "dockerfile", map[string]any{"repo": "https://example.com/x.git", "branch": "main"}),
		store.Server{ID: "srv1", BaseDir: t.TempDir()}, r, cl, lg)
	if err == nil || !strings.Contains(err.Error(), "git") {
		t.Fatalf("want friendly git error, got %v", err)
	}
}

func TestDeployBodyComposeVerbatimPlusWarning(t *testing.T) {
	r := &fakeRunner{psOut: `{"State":"running"}` + "\n"}
	app := testApp("a3", "compose", map[string]any{
		"compose_yaml": "services:\n  web:\n    image: nginx\n    labels:\n      traefik.enable: \"true\"\n",
	})
	lg, base := runFakeRelease(t, r, app, "rel1")
	b, err := os.ReadFile(filepath.Join(base, "apps", "a3", "releases", "rel1", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// verbatim, no rendered restart/networks sections replacing it
	if !strings.HasPrefix(string(b), "services:\n  web:\n") {
		t.Fatalf("compose not verbatim:\n%s", b)
	}
	logB, _ := os.ReadFile(lg.dep.LogFile)
	if !strings.Contains(string(logB), "vpstui's labels win") {
		t.Fatalf("traefik label warning missing:\n%s", logB)
	}
}

func TestDeployBodyStatic(t *testing.T) {
	datadir := t.TempDir()
	os.MkdirAll(filepath.Join(datadir, "apps", "a4", "public"), 0o750)
	os.WriteFile(filepath.Join(datadir, "apps", "a4", "public", "index.html"), []byte("hi"), 0o644)

	r := &fakeRunner{psOut: `{"State":"running"}` + "\n"}
	_, base := runFakeRelease(t, r, testApp("a4", "static", nil), "rel1", datadir)
	b, err := os.ReadFile(filepath.Join(base, "apps", "a4", "releases", "rel1", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("image: 'nginx:alpine'")) ||
		!bytes.Contains(b, []byte("./public:/usr/share/nginx/html:ro")) {
		t.Fatalf("static compose wrong:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(base, "apps", "a4", "releases", "rel1", "public", "index.html")); err != nil {
		t.Fatalf("public not uploaded: %v", err)
	}
}

func TestPruneReleases(t *testing.T) {
	cl := pipeSFTP(t)
	base := t.TempDir()
	releases := filepath.Join(base, "releases")
	os.MkdirAll(releases, 0o750)
	for _, n := range []string{"r1", "r2", "r3", "r4", "r5", "r6", "r7"} {
		os.MkdirAll(filepath.Join(releases, n), 0o750)
	}
	r := &removingRunner{fakeRunner: fakeRunner{}}
	if err := pruneReleases(cl, r, releases, 5); err != nil {
		t.Fatalf("prune: %v", err)
	}
	entries, err := os.ReadDir(releases)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if fmt.Sprint(names) != "[r3 r4 r5 r6 r7]" {
		t.Fatalf("remaining = %v", names)
	}
}

func TestSanitizeANSI(t *testing.T) {
	in := "\x1b[1m Pulling \x1b[0m app\r\n\x1b]0;title\x07done"
	if got := sanitizeANSI(in); got != " Pulling  app\ndone" {
		t.Fatalf("sanitize = %q", got)
	}
}
