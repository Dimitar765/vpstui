package deploy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"

	"vpstui/internal/apitypes"
	"vpstui/internal/docker"
	"vpstui/internal/models"
	"vpstui/internal/orchestrator"
	"vpstui/internal/queue"
	"vpstui/internal/store"
)

// Broadcaster is the WS fan-out seam; *server.Hub satisfies it. (deploy must
// not import internal/server, hence the interface.)
type Broadcaster interface {
	Broadcast(frame any)
}

// Notifier is the notification seam; S7 swaps a no-op default for the real
// dispatcher by wiring only main.go.
type Notifier interface {
	Dispatch(event string, payload any)
}

type nopNotifier struct{}

func (nopNotifier) Dispatch(string, any) {}

// deployTimeout bounds the compose pull+up phase.
const deployTimeout = 15 * time.Minute

// keepReleases is how many release directories survive a prune.
const keepReleases = 5

// Engine runs deployments: one durable row per run, a 2-worker queue,
// per-app serialization, and the SFTP+compose pipeline.
type Engine struct {
	db      *store.DB
	pool    *orchestrator.Pool
	hub     Broadcaster
	nf      Notifier
	log     *log.Logger
	dataDir string

	q       *queue.Queue
	locksMu sync.Mutex
	locks   map[string]*sync.Mutex

	// AcmeEmail is the daemon's --acme-email value. Non-empty → app routers
	// get websecure + certresolver labels (see TraefikLabels).
	AcmeEmail string

	// EnsureProxy is the reverse-proxy seam (Phase S4). Signature is final:
	// func(ctx, Runner, *sftp.Client, store.Server) error — proxy.Ensure
	// satisfies it; main.go wires it because proxy imports deploy.Runner and
	// the cycle must stay broken here. Default (S3): no-op stub.
	EnsureProxy func(ctx context.Context, r Runner, cl *sftp.Client, s store.Server) error
}

// NewEngine assembles the engine. dataDir is the daemon's data directory
// (deploy logs under logs/, static sources under apps/<id>/public).
func NewEngine(db *store.DB, pool *orchestrator.Pool, hub Broadcaster, dataDir string, logger *log.Logger) *Engine {
	if hub == nil {
		hub = nopBroadcaster{}
	}
	if logger == nil {
		logger = log.New(os.Stderr, "", log.LstdFlags|log.Lmsgprefix)
	}
	return &Engine{db: db, pool: pool, hub: hub, nf: nopNotifier{}, log: logger, dataDir: dataDir,
		locks: map[string]*sync.Mutex{}, EnsureProxy: func(context.Context, Runner, *sftp.Client, store.Server) error {
			return nil
		}}
}

type nopBroadcaster struct{}

func (nopBroadcaster) Broadcast(any) {}

// SetNotifier installs the real dispatcher (S7). Safe to call once at boot.
func (e *Engine) SetNotifier(n Notifier) { e.nf = n }

// Start launches the 2-worker queue. Call once, after crash recovery.
func (e *Engine) Start() { e.q = queue.New(2, e.process) }

// Stop drains the queue (in-flight deployments finish).
func (e *Engine) Stop() {
	if e.q != nil {
		e.q.Close()
	}
}

func (e *Engine) appLock(id string) *sync.Mutex {
	e.locksMu.Lock()
	defer e.locksMu.Unlock()
	m, ok := e.locks[id]
	if !ok {
		m = &sync.Mutex{}
		e.locks[id] = m
	}
	return m
}

// Enqueue inserts a queued deployment row (durable BEFORE the signal) and
// wakes a worker. The release is a fresh timestamp token.
func (e *Engine) Enqueue(appID, trigger string) (string, error) {
	return e.enqueueRelease(appID, trigger, newRelease())
}

// EnqueuePinned re-deploys an existing release directory (rollback).
func (e *Engine) EnqueuePinned(appID, trigger, release string) (string, error) {
	return e.enqueueRelease(appID, trigger, release)
}

func newRelease() string {
	return time.Now().UTC().Format("20060102150405") + "-" + models.NewID()[:6]
}

func (e *Engine) enqueueRelease(appID, trigger, release string) (string, error) {
	if e.q == nil {
		return "", fmt.Errorf("deploy engine not started")
	}
	if _, err := e.db.AppByID(appID); err != nil {
		return "", fmt.Errorf("app %s: %w", appID, err)
	}
	depID := models.NewID()
	dep := store.Deployment{
		ID:        depID,
		AppID:     appID,
		Status:    apitypes.DepQueued,
		Trigger:   trigger,
		Release:   release,
		LogFile:   filepath.Join(e.dataDir, "logs", depID+".log"),
		CreatedAt: time.Now().UTC(),
	}
	if err := e.db.CreateDeployment(dep); err != nil {
		return "", err
	}
	e.q.Enqueue(depID)
	return depID, nil
}

// RecoverInterrupted fails every deployment left queued/running by a daemon
// crash, annotating each log file. Call once at boot, before Start.
func (e *Engine) RecoverInterrupted() error {
	deps, err := e.db.FailInterrupted()
	if err != nil {
		return err
	}
	for _, dep := range deps {
		appendLog(dep.LogFile, "interrupted by restart\n")
	}
	if len(deps) > 0 {
		e.log.Printf("crash recovery: marked %d in-flight deployment(s) failed", len(deps))
	}
	return nil
}

// ---------------------------------------------------------------- worker

// process runs one deployment end to end. Serialized per app.
func (e *Engine) process(ctx context.Context, depID string) {
	dep, err := e.db.DeploymentByID(depID)
	if err != nil {
		e.log.Printf("deploy %s: load: %v", depID, err)
		return
	}
	if dep.Status != apitypes.DepQueued {
		return // canceled between enqueue and pickup
	}
	app, err := e.db.AppByID(dep.AppID)
	if err != nil {
		e.failSimple(dep, err)
		return
	}
	server, err := e.db.ServerByID(app.ServerID)
	if err != nil {
		e.failSimple(dep, err)
		return
	}

	mu := e.appLock(app.ID)
	mu.Lock()
	defer mu.Unlock()

	e.transition(dep, apitypes.DepRunning)
	lg := &lineLogger{eng: e, dep: dep, f: openLog(dep.LogFile)}
	defer lg.close()

	defer func() {
		if p := recover(); p != nil {
			lg.linef("internal error: %v", p)
			e.transition(dep, apitypes.DepFailed)
		}
	}()

	if err := e.runPipeline(ctx, dep, app, server, lg); err != nil {
		// deployBody already logged the failure + "old containers keep
		// running" footer to the deployment log.
		_ = e.db.SetDeploymentStatus(dep.ID, apitypes.DepFailed)
		_ = e.db.SetAppDeployResult(app.ID, appStatusAfterFailure(app), "")
		e.transition(dep, apitypes.DepFailed)
		e.nf.Dispatch("deploy.failed", map[string]any{"appId": app.ID, "appName": app.Name, "deploymentId": dep.ID})
		return
	}
	_ = e.db.SetDeploymentStatus(dep.ID, apitypes.DepSuccess)
	_ = e.db.SetAppDeployResult(app.ID, apitypes.AppRunning, dep.ID)
	e.transition(dep, apitypes.DepSuccess)
	e.nf.Dispatch("deploy.success", map[string]any{"appId": app.ID, "appName": app.Name, "deploymentId": dep.ID})
}

// runPipeline is the deploy body once connected: proxy ensure, staging,
// compose up, verify, symlink swap, prune. Split from process so tests drive
// it with a fake Runner and an in-process SFTP server.
func (e *Engine) runPipeline(ctx context.Context, dep store.Deployment, app store.App, server store.Server, lg *lineLogger) error {
	conn, err := e.pool.Conn(ctx, server)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	cl, err := conn.SFTP()
	if err != nil {
		return fmt.Errorf("sftp: %w", err)
	}
	defer cl.Close()
	exec, err := e.pool.Executor(ctx, server)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	return e.deployBody(ctx, dep, app, server, Adapter(exec), cl, lg)
}

func appStatusAfterFailure(a store.App) string {
	if a.LastGoodDeployment != "" {
		return apitypes.AppRunning
	}
	return a.Status
}

func (e *Engine) failSimple(dep store.Deployment, err error) {
	e.log.Printf("deploy %s: %v", dep.ID, err)
	appendLog(dep.LogFile, "deployment failed: "+err.Error()+"\n")
	_ = e.db.SetDeploymentStatus(dep.ID, apitypes.DepFailed)
	e.transition(dep, apitypes.DepFailed)
}

// transition updates the row and pushes the WS frame.
func (e *Engine) transition(dep store.Deployment, status string) {
	_ = e.db.SetDeploymentStatus(dep.ID, status)
	e.hub.Broadcast(apitypes.DeployStatusFrame{
		Type: apitypes.FrameDeployStatus, DeploymentID: dep.ID, Status: status,
	})
}

// lineLogger sends every sanitized line as a deploy.log frame and appends it
// to the deployment's on-disk log.
type lineLogger struct {
	eng *Engine
	dep store.Deployment
	f   *os.File
}

func openLog(p string) *os.File {
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	return f
}

func appendLog(p, line string) {
	if f := openLog(p); f != nil {
		_, _ = f.WriteString(line)
		f.Close()
	}
}

func (l *lineLogger) linef(format string, args ...any) {
	l.raw(fmt.Sprintf(format, args...))
}

func (l *lineLogger) raw(line string) {
	line = sanitizeANSI(line)
	if line == "" {
		return
	}
	if l.eng.hub != nil {
		l.eng.hub.Broadcast(apitypes.DeployLogFrame{
			Type: apitypes.FrameDeployLog, DeploymentID: l.dep.ID, Line: line,
		})
	}
	if l.f != nil {
		_, _ = l.f.WriteString(line + "\n")
	}
}

func (l *lineLogger) stream(rc io.ReadCloser) {
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		l.raw(sc.Text())
	}
}

func (l *lineLogger) close() {
	if l.f != nil {
		l.f.Close()
	}
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\r")

// sanitizeANSI strips escape sequences and carriage returns from compose
// output before it reaches logs and WS frames.
func sanitizeANSI(s string) string {
	return strings.TrimRight(ansiRe.ReplaceAllString(s, ""), " \t")
}

// ---------------------------------------------------------------- pipeline

// deployBody runs the release pipeline through the given Runner and SFTP
// client (pure, fully fake-able).
func (e *Engine) deployBody(ctx context.Context, dep store.Deployment, app store.App, server store.Server, r Runner, cl *sftp.Client, lg *lineLogger) error {
	if err := e.deployInner(ctx, dep, app, server, r, cl, lg); err != nil {
		lg.linef("deployment failed: %v", err)
		// compose up failed before any swap: the previous release's
		// containers (if any) are still serving.
		lg.linef("previous containers (if any) keep running — compose never swapped them")
		return err
	}
	return nil
}

func (e *Engine) deployInner(ctx context.Context, dep store.Deployment, app store.App, server store.Server, r Runner, cl *sftp.Client, lg *lineLogger) error {
	lg.linef("deploying app %s (%s) release %s, trigger %s", app.Name, app.ID, dep.Release, dep.Trigger)

	if e.EnsureProxy != nil {
		if err := e.EnsureProxy(ctx, r, cl, server); err != nil {
			return fmt.Errorf("proxy: %w", err)
		}
	}
	// The shared network app composes join; `|| true` makes it idempotent.
	// (Phase-S4's proxy.Ensure will own this too — creating it twice is a no-op.)
	if _, _, err := r.Exec(ctx, 15*time.Second, "docker network create "+networkName+" || true"); err != nil {
		return fmt.Errorf("create network: %w", err)
	}

	base, err := expandBaseDir(ctx, r, server.BaseDir)
	if err != nil {
		return err
	}
	dir := path.Join(base, "apps", app.ID, "releases", dep.Release)
	if dep.Trigger == apitypes.TriggerRollback {
		lg.linef("rollback: reusing release %s as-is", dep.Release)
		if _, _, err := r.Exec(ctx, 5*time.Second, "test -d "+docker.Quote(dir)); err != nil {
			return fmt.Errorf("release dir %s not found on server", dir)
		}
	} else if err := e.stage(ctx, r, cl, app, dir, lg); err != nil {
		return err
	}

	if err := cl.MkdirAll(dir); err != nil {
		return fmt.Errorf("sftp mkdir %s: %w", dir, err)
	}
	if err := WriteFileAtomic(cl, path.Join(dir, ".env"), envFile(app.EnvVars), 0o600); err != nil {
		return fmt.Errorf("write .env: %w", err)
	}

	project := "vpstui-" + app.ID
	dctx, cancel := context.WithTimeout(ctx, deployTimeout)
	defer cancel()

	lg.linef("pulling images")
	// Best-effort: compose stderr (e.g. "unknown flag: --ignore-pullable" on
	// implementations without it) is streamed but never fails the deploy —
	// `up` performs the authoritative pull. Deploy success is decided by the
	// compose-ps verification below, not by this command's exit.
	pull := "cd " + docker.Quote(dir) + " && docker compose --project-name " + docker.Quote(project) + " pull --ignore-pullable"
	if rc, err := r.Stream(dctx, pull); err == nil {
		lg.stream(rc)
	} else {
		lg.linef("pull failed: %v", err)
	}

	args := "-d --remove-orphans"
	if app.SourceType == "dockerfile" {
		args = "--build " + args // rebuilds are also how a pinned rollback of a dockerfile app refreshes; logged above
	}
	lg.linef("docker compose up %s", args)
	up := "cd " + docker.Quote(dir) + " && docker compose --project-name " + docker.Quote(project) + " up " + args
	rc, err := r.Stream(dctx, up)
	if err != nil {
		return fmt.Errorf("compose up: %w", err)
	}
	lg.stream(rc)

	ok, err := composeHealthy(ctx, r, dir, project)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if !ok {
		return fmt.Errorf("not all services are running after up")
	}

	if err := swapCurrent(cl, r, path.Join(base, "apps", app.ID), dep.Release); err != nil {
		return fmt.Errorf("symlink swap: %w", err)
	}
	if err := pruneReleases(cl, r, path.Join(base, "apps", app.ID, "releases"), keepReleases); err != nil {
		lg.linef("release prune failed (non-fatal): %v", err)
	}
	lg.linef("done: release %s is live", dep.Release)
	return nil
}

// stage materializes the source into the release directory: compose file and
// any uploaded payload.
func (e *Engine) stage(ctx context.Context, r Runner, cl *sftp.Client, app store.App, dir string, lg *lineLogger) error {
	if err := cl.MkdirAll(dir); err != nil {
		return fmt.Errorf("sftp mkdir %s: %w", dir, err)
	}
	switch app.SourceType {
	case "image":
		return writeRenderedCompose(cl, app, "", dir, e.AcmeEmail)
	case "compose":
		yaml, _ := app.SourceConfig["compose_yaml"].(string)
		if yaml == "" {
			return fmt.Errorf("source_config.compose_yaml is required")
		}
		if strings.Contains(yaml, "traefik.") {
			lg.linef("warning: user compose defines its own traefik labels — vpstui's labels win")
		}
		return WriteFileAtomic(cl, path.Join(dir, "compose.yaml"), []byte(yaml), 0o644)
	case "dockerfile":
		if _, _, err := r.Exec(ctx, 10*time.Second, "command -v git"); err != nil {
			return fmt.Errorf("dockerfile deploys need git on the target server (command -v git failed)")
		}
		repo, _ := app.SourceConfig["repo"].(string)
		branch, _ := app.SourceConfig["branch"].(string)
		if repo == "" {
			return fmt.Errorf("source_config.repo is required")
		}
		if branch == "" {
			branch = "main"
		}
		lg.linef("cloning %s (branch %s)", repo, branch)
		cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		out, errOut, err := r.Exec(cctx, 120*time.Second,
			"git clone --depth 1 --branch "+docker.Quote(branch)+" "+docker.Quote(repo)+" "+docker.Quote(path.Join(dir, "repo")))
		for _, line := range strings.Split(strings.TrimSpace(out+"\n"+errOut), "\n") {
			lg.raw(line)
		}
		if err != nil {
			return fmt.Errorf("git clone: %w", err)
		}
		return writeRenderedCompose(cl, app, "./repo", dir, e.AcmeEmail)
	case "static":
		local := filepath.Join(e.dataDir, "apps", app.ID, "public")
		if _, err := os.Stat(filepath.Join(local, "index.html")); err != nil {
			return fmt.Errorf("static source: no index.html under %s on the daemon host (upload it there)", local)
		}
		lg.linef("uploading public/ (%s)", local)
		if err := UploadDir(cl, os.DirFS(local), path.Join(dir, "public")); err != nil {
			return fmt.Errorf("upload public/: %w", err)
		}
		return writeRenderedCompose(cl, app, "", dir, e.AcmeEmail)
	case "template":
		return writeRenderedCompose(cl, app, "", dir, e.AcmeEmail)
	default:
		return fmt.Errorf("unknown source_type %q", app.SourceType)
	}
}

func writeRenderedCompose(cl *sftp.Client, app store.App, buildContext, dir, acmeEmail string) error {
	doc, err := RenderCompose(app, buildContext, acmeEmail)
	if err != nil {
		return err
	}
	return WriteFileAtomic(cl, path.Join(dir, "compose.yaml"), doc, 0o644)
}

// expandBaseDir resolves the server's base_dir to an absolute remote path.
// Rows registered with a relative path (e.g. "$HOME/...") are anchored at the
// remote user's home.
func expandBaseDir(ctx context.Context, r Runner, baseDir string) (string, error) {
	if strings.HasPrefix(baseDir, "/") {
		return strings.TrimSuffix(baseDir, "/"), nil
	}
	out, _, err := r.Exec(ctx, 10*time.Second, `printf %s "$HOME"`)
	if err != nil {
		return "", fmt.Errorf("resolve base_dir: %w", err)
	}
	return path.Join(strings.TrimSpace(out), strings.TrimPrefix(baseDir, "~/")), nil
}

type composePS struct {
	Name   string `json:"Name"`
	State  string `json:"State"`
	Health string `json:"Health"`
}

// composeHealthy verifies every service of the project reports running (and
// healthy, when the image defines healthchecks).
func composeHealthy(ctx context.Context, r Runner, dir, project string) (bool, error) {
	cmd := "cd " + docker.Quote(dir) + " && docker compose --project-name " + docker.Quote(project) + " ps --format json"
	out, errOut, err := r.Exec(ctx, 30*time.Second, cmd)
	if err != nil {
		return false, fmt.Errorf("%s: %w", strings.TrimSpace(errOut), err)
	}
	var any bool
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var svc composePS
		if json.Unmarshal([]byte(line), &svc) != nil {
			continue
		}
		any = true
		if svc.State != "running" {
			return false, nil
		}
		if svc.Health != "" && svc.Health != "healthy" {
			return false, nil
		}
	}
	return any, nil
}

// swapCurrent points <appDir>/current at releases/<release> atomically: new
// symlink at current.tmp, PosixRename over current (plain rename + chmod
// fallback per the spec's assumption).
func swapCurrent(cl *sftp.Client, r Runner, appDir, release string) error {
	target := "releases/" + release
	tmp, final := path.Join(appDir, "current.tmp"), path.Join(appDir, "current")
	if err := cl.Symlink(target, tmp); err != nil {
		// Fallback: shell-level atomic-enough swap.
		_, _, err := r.Exec(context.Background(), 10*time.Second,
			"ln -sfn "+docker.Quote(target)+" "+docker.Quote(final))
		return err
	}
	if err := cl.PosixRename(tmp, final); err != nil {
		if rerr := cl.Rename(tmp, final); rerr != nil {
			_ = cl.Remove(tmp)
			return fmt.Errorf("rename %s: %w (posix: %v)", final, rerr, err)
		}
	}
	return nil
}

// pruneReleases removes release directories beyond the newest keep, ordered
// by release token (timestamp-prefixed, so lexical == chronological).
func pruneReleases(cl *sftp.Client, r Runner, releasesDir string, keep int) error {
	infos, err := cl.ReadDir(releasesDir)
	if err != nil {
		return err
	}
	var names []string
	for _, in := range infos {
		if in.IsDir() {
			names = append(names, in.Name())
		}
	}
	sort.Strings(names)
	for i := 0; i < len(names)-keep; i++ {
		victim := path.Join(releasesDir, names[i])
		if _, _, err := r.Exec(context.Background(), 60*time.Second, "rm -rf "+docker.Quote(victim)); err != nil {
			return fmt.Errorf("prune %s: %w", victim, err)
		}
	}
	return nil
}

// Destroy tears an app's compose stack down (with volumes) and removes its
// release tree. Best-effort on connect failures so a dead server never wedges
// the delete; the DB rows cascade after this returns.
func (e *Engine) Destroy(ctx context.Context, appID string) error {
	app, err := e.db.AppByID(appID)
	if err != nil {
		return err
	}
	mu := e.appLock(appID)
	mu.Lock()
	defer mu.Unlock()

	deps, err := e.db.DeploymentsByApp(appID, 1000)
	if err == nil {
		for _, dep := range deps {
			if dep.Status == apitypes.DepQueued || dep.Status == apitypes.DepRunning {
				_ = e.db.SetDeploymentStatus(dep.ID, apitypes.DepCanceled)
			}
		}
	}

	server, err := e.db.ServerByID(app.ServerID)
	if err == nil {
		exec, cerr := e.pool.Executor(ctx, server)
		if cerr == nil {
			r := Adapter(exec)
			base, berr := expandBaseDir(ctx, r, server.BaseDir)
			appDir := path.Join(base, "apps", appID)
			if berr == nil {
				dir := currentReleaseDir(r, appDir)
				if dir != "" {
					down := "cd " + docker.Quote(dir) + " && docker compose --project-name " +
						docker.Quote("vpstui-"+appID) + " down -v --remove-orphans"
					if _, _, derr := r.Exec(ctx, 120*time.Second, down); derr != nil {
						e.log.Printf("app %s: compose down: %v", appID, derr)
					}
				}
				if _, _, rerr := r.Exec(ctx, 60*time.Second, "rm -rf "+docker.Quote(appDir)); rerr != nil {
					e.log.Printf("app %s: remove files: %v", appID, rerr)
				}
			}
		}
	}
	return e.db.DeleteApp(appID)
}

// currentReleaseDir resolves <appDir>/current to an absolute release dir via
// the shell; "" when it cannot be resolved.
func currentReleaseDir(r Runner, appDir string) string {
	out, _, err := r.Exec(context.Background(), 10*time.Second,
		"readlink -f "+docker.Quote(path.Join(appDir, "current")))
	if err != nil || strings.TrimSpace(out) == "" {
		return ""
	}
	return strings.TrimSpace(out)
}
