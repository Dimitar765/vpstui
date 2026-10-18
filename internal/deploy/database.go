package deploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"gopkg.in/yaml.v3"

	"vpstui/internal/apitypes"
	"vpstui/internal/docker"
	"vpstui/internal/secret"
	"vpstui/internal/store"
)

// Managed database types: image, canonical data dir (the bind-mount target),
// container port and backup file extension.
type dbSpec struct {
	image   string
	dataDir string
	cport   string
	ext     string
}

var dbSpecs = map[string]dbSpec{
	"postgres": {image: "postgres:16-alpine", dataDir: "/var/lib/postgresql/data", cport: "5432", ext: "dump"},
	"mysql":    {image: "mysql:8", dataDir: "/var/lib/mysql", cport: "3306", ext: "sql"},
	"redis":    {image: "redis:7-alpine", dataDir: "/data", cport: "6379", ext: "rdb"},
	"mongo":    {image: "mongo:7", dataDir: "/data/db", cport: "27017", ext: "archive"},
}

// dbProject / dbVolume / dbBackupDir / dbContainer are the remote layout
// conventions every database shares: compose project vpstui-db-<id>, data
// under volumes/db-<id>, dumps under backups/db-<id>.
func dbProject(id string) string      { return "vpstui-db-" + id }
func dbVolume(base, id string) string { return path.Join(base, "volumes", "db-"+id) }
func dbBackupDir(base, id string) string {
	return path.Join(base, "backups", "db-"+id)
}
func dbComposeDir(base, id string) string { return path.Join(base, "databases", "db-"+id) }
func dbContainer(id string) string        { return "db-" + id }

// dbSlug turns the display name into a valid SQL identifier: lowercase,
// [a-z0-9-_], capped at 40 chars. Empty input falls back to "db".
func dbSlug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := strings.Trim(b.String(), "-_")
	if s == "" {
		return "db"
	}
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// newDBPassword returns a fresh 24-hex-char password.
func newDBPassword() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// dbCredentials returns the stored credential set, generating (and persisting
// via secret.Set) on first ensure so a re-ensure never rotates passwords.
func dbCredentials(ctx context.Context, d store.Database) (user, name, pw string, err error) {
	name = dbSlug(d.Name)
	user = name
	if pw, err = secret.Get("db:" + d.ID + ":password"); err != nil || pw == "" {
		if pw, err = newDBPassword(); err != nil {
			return "", "", "", fmt.Errorf("generate password: %w", err)
		}
		if err = secret.Set("db:"+d.ID+":password", pw); err != nil {
			return "", "", "", fmt.Errorf("store password secret: %w", err)
		}
	}
	if v, gerr := secret.Get("db:" + d.ID + ":user"); gerr == nil && v != "" {
		user = v
	} else if err = secret.Set("db:"+d.ID+":user", user); err != nil {
		return "", "", "", fmt.Errorf("store user secret: %w", err)
	}
	if v, gerr := secret.Get("db:" + d.ID + ":name"); gerr == nil && v != "" {
		name = v
	} else if err = secret.Set("db:"+d.ID+":name", name); err != nil {
		return "", "", "", fmt.Errorf("store name secret: %w", err)
	}
	_ = ctx
	return user, name, pw, nil
}

// dbCompose renders the compose body: no Traefik labels (databases are not
// HTTP), ports bound to 127.0.0.1 only when publish is set.
func dbCompose(d store.Database, base, user, name, pw string) ([]byte, error) {
	spec, ok := dbSpecs[d.DbType]
	if !ok {
		return nil, fmt.Errorf("unknown db type %q", d.DbType)
	}
	env := map[string]string{}
	var command any
	switch d.DbType {
	case "postgres":
		env = map[string]string{"POSTGRES_USER": user, "POSTGRES_PASSWORD": pw, "POSTGRES_DB": name}
	case "mysql":
		env = map[string]string{
			"MYSQL_ROOT_PASSWORD": pw, "MYSQL_DATABASE": name,
			"MYSQL_USER": user, "MYSQL_PASSWORD": pw,
		}
	case "redis":
		env = map[string]string{"REDIS_PASSWORD": pw}
		command = []string{"sh", "-c", `exec redis-server --requirepass "$REDIS_PASSWORD"`}
	case "mongo":
		env = map[string]string{
			"MONGO_INITDB_ROOT_USERNAME": user, "MONGO_INITDB_ROOT_PASSWORD": pw,
			"MONGO_INITDB_DATABASE": name,
		}
	}
	svc := map[string]any{
		"image":          spec.image,
		"container_name": dbContainer(d.ID),
		"restart":        "unless-stopped",
		"environment":    env,
		"volumes":        []string{dbVolume(base, d.ID) + ":" + spec.dataDir + ":Z"},
	}
	if command != nil {
		svc["command"] = command
	}
	if pub, _ := d.Config["publish"].(bool); pub {
		port := fmt.Sprintf("%v", d.Config["port"])
		if port == "<nil>" {
			port = spec.cport
		}
		svc["ports"] = []string{"127.0.0.1:" + port + ":" + spec.cport}
	}
	return yaml.Marshal(map[string]any{"services": map[string]any{"db": svc}})
}

// EnsureDatabase renders and applies the database's compose stack. It is
// idempotent: credentials are created once and `compose up -d` converges.
func EnsureDatabase(ctx context.Context, r Runner, cl *sftp.Client, d store.Database, server store.Server) error {
	base, err := expandBaseDir(ctx, r, server.BaseDir)
	if err != nil {
		return err
	}
	user, name, pw, err := dbCredentials(ctx, d)
	if err != nil {
		return err
	}
	dir := dbComposeDir(base, d.ID)
	body, err := dbCompose(d, base, user, name, pw)
	if err != nil {
		return err
	}
	if err := cl.MkdirAll(dir); err != nil {
		return fmt.Errorf("mkdir compose dir: %w", err)
	}
	if err := cl.MkdirAll(dbVolume(base, d.ID)); err != nil {
		return fmt.Errorf("mkdir volume dir: %w", err)
	}
	if err := WriteFileAtomic(cl, path.Join(dir, "docker-compose.yml"), body, 0o600); err != nil {
		return err
	}
	up := "cd " + docker.Quote(dir) + " && docker compose --project-name " +
		docker.Quote(dbProject(d.ID)) + " up -d --remove-orphans"
	if _, stderr, err := r.Exec(ctx, 180*time.Second, up); err != nil {
		return fmt.Errorf("compose up: %w (%s)", err, strings.TrimSpace(stderr))
	}
	return nil
}

// dbURI renders the per-type admin URI. Host is loopback: published ports
// always bind 127.0.0.1.
func dbURI(dbType, user, name, pw, host string) string {
	switch dbType {
	case "postgres":
		return fmt.Sprintf("postgres://%s:%s@%s/%s", user, pw, host, name)
	case "mysql":
		return fmt.Sprintf("mysql://%s:%s@%s/%s", user, pw, host, name)
	case "redis":
		return fmt.Sprintf("redis://:%s@%s/0", pw, host)
	case "mongo":
		return fmt.Sprintf("mongodb://%s:%s@%s/%s", user, pw, host, name)
	}
	return ""
}

// ConnectionString builds the per-type admin URI with credentials from the
// secret store. Databases bind 127.0.0.1 when published, so the host is
// always loopback.
func ConnectionString(ctx context.Context, r Runner, d store.Database) (string, error) {
	user, name, pw, err := dbCredentials(ctx, d)
	if err != nil {
		return "", err
	}
	spec, ok := dbSpecs[d.DbType]
	if !ok {
		return "", fmt.Errorf("unknown db type %q", d.DbType)
	}
	host := "127.0.0.1:" + spec.cport
	if pub, _ := d.Config["publish"].(bool); pub {
		if p := d.Config["port"]; p != nil {
			host = "127.0.0.1:" + fmt.Sprintf("%v", p)
		}
	}
	return dbURI(d.DbType, user, name, pw, host), nil
}

// ------------------------------------------------------- engine operations

// DatabaseSession dials the database's server for a synchronous operation.
// The returned cleanup closes the SFTP client only (the SSH conn stays pooled).
func (e *Engine) DatabaseSession(ctx context.Context, serverID string) (Runner, *sftp.Client, func(), error) {
	server, err := e.db.ServerByID(serverID)
	if err != nil {
		return nil, nil, nil, err
	}
	conn, err := e.pool.Conn(ctx, server)
	if err != nil {
		return nil, nil, nil, err
	}
	exec, err := e.pool.Executor(ctx, server)
	if err != nil {
		return nil, nil, nil, err
	}
	r := Adapter(exec)
	cl, err := conn.SFTP()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sftp: %w", err)
	}
	return r, cl, func() { _ = cl.Close() }, nil
}

// EnsureDatabaseSync provisions (or converges) a managed database and flips
// its row to running.
func (e *Engine) EnsureDatabaseSync(ctx context.Context, databaseID string) error {
	d, err := e.db.DatabaseByID(databaseID)
	if err != nil {
		return err
	}
	r, cl, done, err := e.DatabaseSession(ctx, d.ServerID)
	if err != nil {
		return err
	}
	defer done()
	server, err := e.db.ServerByID(d.ServerID)
	if err != nil {
		return err
	}
	if err := EnsureDatabase(ctx, r, cl, d, server); err != nil {
		_ = e.db.SetDatabaseStatus(d.ID, "failed")
		return err
	}
	return e.db.SetDatabaseStatus(d.ID, "running")
}

// DestroyDatabase tears the stack down with volumes, removes the remote files
// and the credential secrets, then deletes the rows. Best-effort remote
// cleanup: a dead server never wedges the delete.
func (e *Engine) DestroyDatabase(ctx context.Context, databaseID string) error {
	d, err := e.db.DatabaseByID(databaseID)
	if err != nil {
		return err
	}
	if server, serr := e.db.ServerByID(d.ServerID); serr == nil {
		if r, _, done, derr := e.DatabaseSession(ctx, d.ServerID); derr == nil {
			defer done()
			if base, berr := expandBaseDir(ctx, r, server.BaseDir); berr == nil {
				down := "cd " + docker.Quote(dbComposeDir(base, d.ID)) +
					" && docker compose --project-name " + docker.Quote(dbProject(d.ID)) +
					" down -v --remove-orphans"
				if _, _, err := r.Exec(ctx, 120*time.Second, down); err != nil {
					e.log.Printf("database %s: compose down: %v", d.ID, err)
				}
				for _, dir := range []string{dbComposeDir(base, d.ID), dbBackupDir(base, d.ID)} {
					if _, _, err := r.Exec(ctx, 60*time.Second, "rm -rf "+docker.Quote(dir)); err != nil {
						e.log.Printf("database %s: remove %s: %v", d.ID, dir, err)
					}
				}
				// The data volume dir may be chown'ed by the container's
				// rootless subuid (postgres/mysql chown their data dir), so a
				// plain rm can hit EACCES; retry inside the userns.
				vol := dbVolume(base, d.ID)
				if _, _, err := r.Exec(ctx, 60*time.Second, "rm -rf "+docker.Quote(vol)); err != nil {
					if _, _, uerr := r.Exec(ctx, 60*time.Second,
						"podman unshare rm -rf "+docker.Quote(vol)); uerr != nil {
						e.log.Printf("database %s: remove %s: %v (unshare: %v)", d.ID, vol, err, uerr)
					}
				}
			}
		}
	}
	for _, k := range []string{"password", "user", "name"} {
		_ = secret.Delete("db:" + d.ID + ":" + k)
	}
	return e.db.DeleteDatabase(d.ID)
}

// DatabaseConnectionString resolves the admin URI for a database row.
func (e *Engine) DatabaseConnectionString(ctx context.Context, databaseID string) (string, error) {
	d, err := e.db.DatabaseByID(databaseID)
	if err != nil {
		return "", err
	}
	r, _, done, err := e.DatabaseSession(ctx, d.ServerID)
	if err != nil {
		return "", err
	}
	defer done()
	return ConnectionString(ctx, r, d)
}

// ListBackups lists the database's backup directory newest first — the
// history feed behind GET /api/databases/{id}/backups. A database that has
// not produced a dump yet (missing dir) yields an empty list, not an error.
func (e *Engine) ListBackups(ctx context.Context, databaseID string) ([]apitypes.BackupEntry, error) {
	d, err := e.db.DatabaseByID(databaseID)
	if err != nil {
		return nil, err
	}
	server, err := e.db.ServerByID(d.ServerID)
	if err != nil {
		return nil, err
	}
	r, cl, done, err := e.DatabaseSession(ctx, d.ServerID)
	if err != nil {
		return nil, err
	}
	defer done()
	base, err := expandBaseDir(ctx, r, server.BaseDir)
	if err != nil {
		return nil, err
	}
	return listBackups(cl, dbBackupDir(base, d.ID))
}

// listBackups reads the dir over SFTP (no shell parsing) and orders entries
// newest first. Non-regular files are skipped.
func listBackups(cl *sftp.Client, dir string) ([]apitypes.BackupEntry, error) {
	infos, err := cl.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []apitypes.BackupEntry{}, nil
		}
		return nil, fmt.Errorf("list backups %s: %w", dir, err)
	}
	out := make([]apitypes.BackupEntry, 0, len(infos))
	for _, fi := range infos {
		if !fi.Mode().IsRegular() {
			continue
		}
		out = append(out, apitypes.BackupEntry{
			Name: fi.Name(), Size: fi.Size(), Modified: fi.ModTime().UTC(),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}
