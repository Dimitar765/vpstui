package deploy

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"vpstui/internal/docker"
	"vpstui/internal/store"
)

// dbFakeRunner records every command; ls replies come from a canned map.
type dbFakeRunner struct {
	cmds  []string
	lsOut map[string]string // dir -> ls output
}

func (f *dbFakeRunner) Exec(_ context.Context, _ time.Duration, cmd string) (string, string, error) {
	f.cmds = append(f.cmds, cmd)
	for dir, out := range f.lsOut {
		if strings.HasPrefix(cmd, "ls -1t "+docker.Quote(dir)) {
			return out, "", nil
		}
	}
	return "", "", nil
}

func (f *dbFakeRunner) Stream(context.Context, string) (io.ReadCloser, error) { return nil, nil }

func TestDBSlug(t *testing.T) {
	cases := map[string]string{
		"Main DB":               "main_db",
		"  prod-pg 01 ":         "prod-pg_01",
		"!!!":                   "db",
		"":                      "db",
		strings.Repeat("x", 60): strings.Repeat("x", 40),
	}
	for in, want := range cases {
		if got := dbSlug(in); got != want {
			t.Errorf("dbSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDBComposePerType(t *testing.T) {
	cases := []struct {
		dbType  string
		image   string
		dataDir string
		envKey  string
	}{
		{"postgres", "postgres:16-alpine", "/var/lib/postgresql/data", "POSTGRES_PASSWORD"},
		{"mysql", "mysql:8", "/var/lib/mysql", "MYSQL_ROOT_PASSWORD"},
		{"redis", "redis:7-alpine", "/data", "REDIS_PASSWORD"},
		{"mongo", "mongo:7", "/data/db", "MONGO_INITDB_ROOT_PASSWORD"},
	}
	for _, tc := range cases {
		d := store.Database{ID: "abc123", DbType: tc.dbType, Name: "main", Config: map[string]any{}}
		body, err := dbCompose(d, "/srv/base", "main", "main", "pw123456")
		if err != nil {
			t.Fatalf("%s: %v", tc.dbType, err)
		}
		var doc struct {
			Services map[string]struct {
				Image         string            `yaml:"image"`
				ContainerName string            `yaml:"container_name"`
				Volumes       []string          `yaml:"volumes"`
				Ports         []string          `yaml:"ports"`
				Environment   map[string]string `yaml:"environment"`
				Labels        map[string]string `yaml:"labels"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal(body, &doc); err != nil {
			t.Fatalf("%s: yaml: %v", tc.dbType, err)
		}
		svc := doc.Services["db"]
		if svc.Image != tc.image {
			t.Errorf("%s image = %s", tc.dbType, svc.Image)
		}
		if svc.ContainerName != "db-abc123" {
			t.Errorf("%s container_name = %s", tc.dbType, svc.ContainerName)
		}
		wantVol := "/srv/base/volumes/db-abc123:" + tc.dataDir + ":Z"
		if len(svc.Volumes) != 1 || svc.Volumes[0] != wantVol {
			t.Errorf("%s volumes = %v, want [%s]", tc.dbType, svc.Volumes, wantVol)
		}
		if len(svc.Ports) != 0 {
			t.Errorf("%s: ports published without publish flag: %v", tc.dbType, svc.Ports)
		}
		if svc.Labels != nil {
			t.Errorf("%s: traefik labels present: %v", tc.dbType, svc.Labels)
		}
		if _, ok := svc.Environment[tc.envKey]; !ok {
			t.Errorf("%s: env %s missing", tc.dbType, tc.envKey)
		}
	}
}

func TestDBComposePublishLoops(t *testing.T) {
	d := store.Database{ID: "p1", DbType: "postgres", Name: "main",
		Config: map[string]any{"publish": true, "port": 55432}}
	body, err := dbCompose(d, "/b", "u", "n", "p")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "127.0.0.1:55432:5432") {
		t.Fatalf("expected 127.0.0.1-only bind, got:\n%s", body)
	}
	// Default host port = the image's canonical port.
	d.Config = map[string]any{"publish": true}
	body, _ = dbCompose(d, "/b", "u", "n", "p")
	if !strings.Contains(string(body), "127.0.0.1:5432:5432") {
		t.Fatalf("default published port wrong:\n%s", body)
	}
}

func TestDBURIShapes(t *testing.T) {
	cases := map[string]string{
		"postgres": "postgres://u:p%40ss@127.0.0.1:5432/n",
		"mysql":    "mysql://u:p%40ss@127.0.0.1:3306/n",
		"redis":    "redis://:p%40ss@127.0.0.1:6379/0",
		"mongo":    "mongodb://u:p%40ss@127.0.0.1:27017/n",
	}
	for typ, want := range cases {
		if got := dbURI(typ, "u", "n", "p%40ss", "127.0.0.1:"+dbSpecs[typ].cport); got != want {
			t.Errorf("%s uri = %s, want %s", typ, got, want)
		}
	}
}

func TestBuildBackupCmdPerType(t *testing.T) {
	pg := buildBackupCmd("postgres", "u", "n", "db-1", "/b/backups/db-1/t.dump")
	if !strings.Contains(pg, "pg_dump -Fc -U 'u' 'n'") || !strings.Contains(pg, "> '/b/backups/db-1/t.dump'") {
		t.Errorf("postgres cmd: %s", pg)
	}
	my := buildBackupCmd("mysql", "u", "n", "db-1", "/b/t.sql")
	if !strings.Contains(my, "mysqldump") || !strings.Contains(my, `"$MYSQL_ROOT_PASSWORD"`) {
		t.Errorf("mysql cmd: %s", my)
	}
	rd := buildBackupCmd("redis", "u", "n", "db-1", "/b/t.rdb")
	if !strings.Contains(rd, "BGSAVE") || !strings.Contains(rd, "docker cp") ||
		!strings.Contains(rd, "db-1:/data/dump.rdb") {
		t.Errorf("redis cmd: %s", rd)
	}
	mg := buildBackupCmd("mongo", "u", "n", "db-1", "/b/t.archive")
	if !strings.Contains(mg, "mongodump --archive") || !strings.Contains(mg, `"$MONGO_INITDB_ROOT_PASSWORD"`) {
		t.Errorf("mongo cmd: %s", mg)
	}
	for _, typ := range []string{"postgres", "mysql", "redis", "mongo"} {
		cmd := buildBackupCmd(typ, "u", "n", "db-1", "/b/f")
		if strings.Count(cmd, "'")%2 != 0 || strings.Contains(cmd, `"u"`) {
			// quote discipline sanity: balanced single quotes from docker.Quote
			t.Errorf("%s: unbalanced quoting: %s", typ, cmd)
		}
	}
}

func TestPruneBackupsRetention(t *testing.T) {
	fr := &dbFakeRunner{lsOut: map[string]string{
		"/b/backups/db-1": "c.dump\nb.dump\na.dump\nd.dump\n",
	}}
	if err := pruneBackups(context.Background(), fr, "/b/backups/db-1", 2); err != nil {
		t.Fatal(err)
	}
	var removed []string
	for _, c := range fr.cmds {
		if strings.HasPrefix(c, "rm -f ") {
			removed = append(removed, strings.Trim(strings.TrimPrefix(c, "rm -f "), "'"))
		}
	}
	// newest two kept (c, b — ls -1t order), two oldest removed.
	want := []string{"/b/backups/db-1/a.dump", "/b/backups/db-1/d.dump"}
	if !reflect.DeepEqual(removed, want) {
		t.Errorf("removed = %v, want %v", removed, want)
	}
}

func TestNewDBPasswordShape(t *testing.T) {
	pw, err := newDBPassword()
	if err != nil {
		t.Fatal(err)
	}
	if len(pw) != 24 {
		t.Errorf("password length = %d, want 24 hex chars", len(pw))
	}
	for _, c := range pw {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Errorf("non-hex char %q", c)
		}
	}
}

func TestDBLayoutHelpers(t *testing.T) {
	if got := dbProject("x"); got != "vpstui-db-x" {
		t.Errorf("project = %s", got)
	}
	if got := dbVolume("/b", "x"); got != "/b/volumes/db-x" {
		t.Errorf("volume = %s", got)
	}
	if got := dbBackupDir("/b", "x"); got != "/b/backups/db-x" {
		t.Errorf("backup dir = %s", got)
	}
	if got := dbComposeDir("/b", "x"); got != "/b/databases/db-x" {
		t.Errorf("compose dir = %s", got)
	}
}
