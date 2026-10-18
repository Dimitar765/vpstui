package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ---------------------------------------------------------------- projects

type Project struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

func (d *DB) CreateProject(p Project) error {
	_, err := d.db.Exec(`INSERT INTO projects(id, name, created_at) VALUES(?,?,?)`, p.ID, p.Name, now())
	return err
}

func (d *DB) Projects() ([]Project, error) {
	rows, err := d.db.Query(`SELECT id, name, created_at FROM projects ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		var ts int64
		if err := rows.Scan(&p.ID, &p.Name, &ts); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(ts, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) ProjectByID(id string) (Project, error) {
	var p Project
	var ts int64
	err := d.db.QueryRow(`SELECT id, name, created_at FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, err
	}
	p.CreatedAt = time.Unix(ts, 0).UTC()
	return p, nil
}

func (d *DB) DeleteProject(id string) error {
	return deleteByID(d, "projects", id)
}

// ---------------------------------------------------------------- environments

type Environment struct {
	ID        string
	ProjectID string
	Name      string
}

func (d *DB) CreateEnvironment(e Environment) error {
	_, err := d.db.Exec(`INSERT INTO environments(id, project_id, name) VALUES(?,?,?)`, e.ID, e.ProjectID, e.Name)
	return err
}

func (d *DB) EnvironmentsByProject(projectID string) ([]Environment, error) {
	rows, err := d.db.Query(`SELECT id, project_id, name FROM environments WHERE project_id = ? ORDER BY rowid`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Environment
	for rows.Next() {
		var e Environment
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.Name); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *DB) EnvironmentByID(id string) (Environment, error) {
	var e Environment
	err := d.db.QueryRow(`SELECT id, project_id, name FROM environments WHERE id = ?`, id).
		Scan(&e.ID, &e.ProjectID, &e.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return Environment{}, ErrNotFound
	}
	return e, err
}

func (d *DB) DeleteEnvironment(id string) error {
	return deleteByID(d, "environments", id)
}

// ---------------------------------------------------------------- apps

type App struct {
	ID                 string
	EnvironmentID      string
	ServerID           string
	Name               string
	SourceType         string // image | compose | dockerfile | static | template
	SourceConfig       map[string]any
	EnvVars            map[string]string
	Domains            []string
	Port               int // 0 = unset
	AutoDeploy         bool
	WebhookToken       string
	LastGoodDeployment string
	Status             string
	UpdatedAt          time.Time
}

const appCols = `id, environment_id, server_id, name, source_type, source_config, env_vars,
  domains, port, auto_deploy, webhook_token, last_good_deployment, status, updated_at`

func scanApp(r rowScanner) (App, error) {
	var a App
	var srcCfg, envVars, domains string
	var port sql.NullInt64
	var auto int
	var lastGood sql.NullString
	var ts int64
	if err := r.Scan(&a.ID, &a.EnvironmentID, &a.ServerID, &a.Name, &a.SourceType, &srcCfg,
		&envVars, &domains, &port, &auto, &a.WebhookToken, &lastGood, &a.Status, &ts); err != nil {
		return App{}, err
	}
	if err := json.Unmarshal([]byte(srcCfg), &a.SourceConfig); err != nil {
		return App{}, fmt.Errorf("app %s source_config: %w", a.ID, err)
	}
	if err := json.Unmarshal([]byte(envVars), &a.EnvVars); err != nil {
		return App{}, fmt.Errorf("app %s env_vars: %w", a.ID, err)
	}
	if err := json.Unmarshal([]byte(domains), &a.Domains); err != nil {
		return App{}, fmt.Errorf("app %s domains: %w", a.ID, err)
	}
	a.Port = int(port.Int64)
	a.AutoDeploy = auto == 1
	if lastGood.Valid {
		a.LastGoodDeployment = lastGood.String
	}
	a.UpdatedAt = time.Unix(ts, 0).UTC()
	return a, nil
}

func (a App) insertArgs() []any {
	srcCfg, _ := json.Marshal(a.SourceConfig)
	envVars, _ := json.Marshal(a.EnvVars)
	domains, _ := json.Marshal(a.Domains)
	var port any
	if a.Port != 0 {
		port = a.Port
	}
	return []any{a.ID, a.EnvironmentID, a.ServerID, a.Name, a.SourceType, string(srcCfg),
		string(envVars), string(domains), port, a.AutoDeploy, a.WebhookToken,
		a.LastGoodDeployment, a.Status, a.UpdatedAt.Unix()}
}

func (d *DB) CreateApp(a App) error {
	args := a.insertArgs()
	_, err := d.db.Exec(`INSERT INTO apps(id, environment_id, server_id, name, source_type, source_config,
      env_vars, domains, port, auto_deploy, webhook_token, last_good_deployment, status, updated_at)
      VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
	return err
}

func (d *DB) UpdateApp(a App) error {
	args := a.insertArgs()
	res, err := d.db.Exec(`UPDATE apps SET environment_id=?, server_id=?, name=?, source_type=?, source_config=?,
      env_vars=?, domains=?, port=?, auto_deploy=?, webhook_token=?, last_good_deployment=?, status=?, updated_at=?
      WHERE id=?`, append(args[1:], a.ID)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) Apps() ([]App, error) {
	rows, err := d.db.Query(`SELECT ` + appCols + ` FROM apps ORDER BY updated_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []App
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *DB) AppByID(id string) (App, error) {
	row := d.db.QueryRow(`SELECT `+appCols+` FROM apps WHERE id = ?`, id)
	a, err := scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return App{}, ErrNotFound
	}
	return a, err
}

func (d *DB) AppByWebhookToken(token string) (App, error) {
	row := d.db.QueryRow(`SELECT `+appCols+` FROM apps WHERE webhook_token = ?`, token)
	a, err := scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return App{}, ErrNotFound
	}
	return a, err
}

// SetAppDeployResult records the outcome of a deployment: status on the app
// and, on success, the deployment that produced the known-good release.
func (d *DB) SetAppDeployResult(appID, status, lastGoodDeployment string) error {
	_, err := d.db.Exec(`UPDATE apps SET status=?, last_good_deployment=?, updated_at=? WHERE id=?`,
		status, lastGoodDeployment, now(), appID)
	return err
}

func (d *DB) DeleteApp(id string) error {
	return deleteByID(d, "apps", id)
}

// ---------------------------------------------------------------- deployments

type Deployment struct {
	ID         string
	AppID      string
	Status     string // queued | running | success | failed | canceled
	Trigger    string // manual | webhook | rollback
	Release    string
	LogFile    string
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
}

const depCols = `id, app_id, status, trigger, release, log_file, created_at, started_at, finished_at`

func scanDeployment(r rowScanner) (Deployment, error) {
	var dep Deployment
	var created int64
	var started, finished sql.NullInt64
	if err := r.Scan(&dep.ID, &dep.AppID, &dep.Status, &dep.Trigger, &dep.Release,
		&dep.LogFile, &created, &started, &finished); err != nil {
		return Deployment{}, err
	}
	dep.CreatedAt = time.Unix(created, 0).UTC()
	if started.Valid {
		t := time.Unix(started.Int64, 0).UTC()
		dep.StartedAt = &t
	}
	if finished.Valid {
		t := time.Unix(finished.Int64, 0).UTC()
		dep.FinishedAt = &t
	}
	return dep, nil
}

func (d *DB) CreateDeployment(dep Deployment) error {
	_, err := d.db.Exec(`INSERT INTO deployments(id, app_id, status, trigger, release, log_file, created_at)
      VALUES(?,?,?,?,?,?,?)`, dep.ID, dep.AppID, dep.Status, dep.Trigger, dep.Release, dep.LogFile, dep.CreatedAt.Unix())
	return err
}

func (d *DB) DeploymentByID(id string) (Deployment, error) {
	row := d.db.QueryRow(`SELECT `+depCols+` FROM deployments WHERE id = ?`, id)
	dep, err := scanDeployment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, ErrNotFound
	}
	return dep, err
}

func (d *DB) DeploymentsByApp(appID string, limit int) ([]Deployment, error) {
	rows, err := d.db.Query(`SELECT `+depCols+` FROM deployments WHERE app_id = ?
      ORDER BY created_at DESC, id DESC LIMIT ?`, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Deployment
	for rows.Next() {
		dep, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dep)
	}
	return out, rows.Err()
}

// SetDeploymentStatus transitions one deployment, stamping started_at on the
// first move out of queued and finished_at on every terminal write.
func (d *DB) SetDeploymentStatus(id, status string) error {
	var q string
	switch status {
	case "running":
		q = `UPDATE deployments SET status=?, started_at=? WHERE id=?`
		_, err := d.db.Exec(q, status, now(), id)
		return err
	case "success", "failed", "canceled":
		q = `UPDATE deployments SET status=?, finished_at=? WHERE id=?`
		_, err := d.db.Exec(q, status, now(), id)
		return err
	default:
		return fmt.Errorf("invalid deployment status %q", status)
	}
}

// FailInterrupted marks every in-flight deployment failed (boot-time crash
// recovery) and returns their ids so the caller can annotate the log files.
func (d *DB) FailInterrupted() ([]Deployment, error) {
	rows, err := d.db.Query(`SELECT ` + depCols + ` FROM deployments WHERE status IN ('queued','running')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Deployment
	var ids []string
	for rows.Next() {
		dep, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dep)
		ids = append(ids, dep.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := d.SetDeploymentStatus(id, "failed"); err != nil {
			return out, err
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- shared

func deleteByID(d *DB, table, id string) error {
	res, err := d.db.Exec(`DELETE FROM `+table+` WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
