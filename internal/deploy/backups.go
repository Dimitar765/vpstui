package deploy

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"vpstui/internal/docker"
	"vpstui/internal/store"
)

// backupTimeout bounds one dump; a wedged link fails the backup, never a deploy.
const backupTimeout = 10 * time.Minute

// RunBackup performs an immediate backup: dump through the Runner into
// <base>/backups/db-<id>/<ts>.<ext>, prune beyond retention and stamp
// last_run_at. Returns the created file path.
func (e *Engine) RunBackup(ctx context.Context, databaseID string) (string, error) {
	d, err := e.db.DatabaseByID(databaseID)
	if err != nil {
		return "", err
	}
	// A manual backup is valid without a schedule row: fall back to the
	// schema-default retention (7). Other lookup errors are real failures.
	retention := 7
	if sched, err := e.db.BackupScheduleByDatabase(databaseID); err == nil {
		if sched.Retention > 0 {
			retention = sched.Retention
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	server, err := e.db.ServerByID(d.ServerID)
	if err != nil {
		return "", err
	}
	r, _, done, err := e.DatabaseSession(ctx, d.ServerID)
	if err != nil {
		return "", err
	}
	defer done()

	base, err := expandBaseDir(ctx, r, server.BaseDir)
	if err != nil {
		return "", err
	}
	spec, ok := dbSpecs[d.DbType]
	if !ok {
		return "", fmt.Errorf("unknown db type %q", d.DbType)
	}
	user, name, _, err := dbCredentials(ctx, d)
	if err != nil {
		return "", err
	}
	dir := dbBackupDir(base, d.ID)
	if _, _, err := r.Exec(ctx, 30*time.Second, "mkdir -p "+docker.Quote(dir)); err != nil {
		return "", fmt.Errorf("mkdir backup dir: %w", err)
	}
	ts := time.Now().UTC().Format("20060102-150405")
	file := path.Join(dir, ts+"."+spec.ext)
	container := dbContainer(d.ID)

	var cmd string
	cmd = buildBackupCmd(d.DbType, user, name, container, file)
	if _, stderr, err := r.Exec(ctx, backupTimeout, cmd); err != nil {
		return "", fmt.Errorf("dump %s: %w (%s)", d.DbType, err, strings.TrimSpace(stderr))
	}
	if err := pruneBackups(ctx, r, dir, retention); err != nil {
		// The dump exists; pruning is housekeeping — log-level severity.
		e.log.Printf("database %s: retention prune: %v", d.ID, err)
	}
	if err := e.db.SetBackupLastRun(d.ID, time.Now().UTC()); err != nil {
		return "", err
	}
	return file, nil
}

// buildBackupCmd renders the remote dump command for one db type. Custom
// formats stream to stdout and the remote shell redirect lands them at file;
// redis BGSAVEs in-container then copies the RDB out via docker cp.
func buildBackupCmd(dbType, user, name, container, file string) string {
	switch dbType {
	case "postgres":
		return "docker exec " + docker.Quote(container) + " pg_dump -Fc -U " +
			docker.Quote(user) + " " + docker.Quote(name) + " > " + docker.Quote(file)
	case "mysql":
		script := `exec mysqldump -uroot -p"$MYSQL_ROOT_PASSWORD" ` + name
		return "docker exec " + docker.Quote(container) + " sh -c " + docker.Quote(script) +
			" > " + docker.Quote(file)
	case "redis":
		script := `redis-cli --no-auth-warning -a "$REDIS_PASSWORD" BGSAVE && sleep 2`
		save := "docker exec " + docker.Quote(container) + " sh -c " + docker.Quote(script)
		cp := "docker cp " + docker.Quote(container+":/data/dump.rdb") + " " + docker.Quote(file)
		return save + " && " + cp
	case "mongo":
		script := `exec mongodump --archive -u ` + user +
			` -p "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin`
		return "docker exec " + docker.Quote(container) + " sh -c " + docker.Quote(script) +
			" > " + docker.Quote(file)
	}
	return ""
}

// pruneBackups deletes the oldest dumps beyond retention, newest kept by
// mtime ordering (`ls -1t`).
func pruneBackups(ctx context.Context, r Runner, dir string, retention int) error {
	out, _, err := r.Exec(ctx, 30*time.Second, "ls -1t "+docker.Quote(dir))
	if err != nil {
		return err
	}
	files := strings.Fields(out)
	keep := retention
	if keep > len(files) {
		keep = len(files)
	}
	for _, f := range files[keep:] {
		if _, _, err := r.Exec(ctx, 30*time.Second,
			"rm -f "+docker.Quote(path.Join(dir, f))); err != nil {
			return fmt.Errorf("delete %s: %w", f, err)
		}
	}
	return nil
}

// BackupScheduler keeps one robfig/cron instance in sync with the
// backup_schedules table. Jobs run detached (own goroutine), stream their
// logs to the daemon logger and never block deployments.
type BackupScheduler struct {
	e   *Engine
	c   *cron.Cron
	log *log.Logger
}

// NewBackupScheduler assembles a stopped scheduler; Start launches it.
func NewBackupScheduler(e *Engine, logger *log.Logger) *BackupScheduler {
	if logger == nil {
		logger = log.Default()
	}
	return &BackupScheduler{
		e:   e,
		c:   cron.New(),
		log: logger,
	}
}

// Reload re-registers every schedule row. Bad specs are logged and skipped —
// one bad row never takes the scheduler down (the API rejects them at create
// time anyway).
func (s *BackupScheduler) Reload() {
	for _, entry := range s.c.Entries() {
		s.c.Remove(entry.ID)
	}
	scheds, err := s.e.db.BackupSchedules()
	if err != nil {
		s.log.Printf("backup scheduler: list schedules: %v", err)
		return
	}
	for _, sc := range scheds {
		dbID := sc.DatabaseID
		if _, err := s.c.AddFunc(sc.CronSpec, func() {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), backupTimeout)
				defer cancel()
				file, err := s.e.RunBackup(ctx, dbID)
				if err != nil {
					s.log.Printf("backup %s: FAILED: %v", dbID, err)
					return
				}
				s.log.Printf("backup %s: wrote %s", dbID, file)
			}()
		}); err != nil {
			s.log.Printf("backup scheduler: bad cron spec %q for %s: %v", sc.CronSpec, sc.DatabaseID, err)
		}
	}
}

// Start loads the current schedules and runs the loop. Contained goroutine:
// cancelling it on server shutdown is best-effort and currently not wired —
// process exit reaps it. Call Reload after schedule CRUD.
func (s *BackupScheduler) Start() {
	s.Reload()
	s.c.Start()
}

// Stop halts the cron loop and waits for in-flight ticks (jobs themselves run
// detached and are bounded by backupTimeout).
func (s *BackupScheduler) Stop() {
	<-s.c.Stop().Done()
}
