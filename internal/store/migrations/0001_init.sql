-- vpstui-server initial schema. Applied by store.Migrate inside one
-- transaction, tracked by PRAGMA user_version.
CREATE TABLE users(id TEXT PRIMARY KEY, email TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE api_keys(id TEXT PRIMARY KEY, name TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL, last_used_at INTEGER);
CREATE TABLE sessions(token_hash TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL);
CREATE TABLE servers(id TEXT PRIMARY KEY, name TEXT NOT NULL, address TEXT NOT NULL,
  port INTEGER NOT NULL, user TEXT NOT NULL, auth_type TEXT NOT NULL, key_path TEXT NOT NULL DEFAULT '',
  base_dir TEXT NOT NULL DEFAULT '/opt/vpstui', monitor_interval_sec INTEGER NOT NULL DEFAULT 15,
  proxy_http_port INTEGER NOT NULL DEFAULT 80, proxy_https_port INTEGER NOT NULL DEFAULT 443,
  created_at INTEGER NOT NULL, last_seen_at INTEGER, status TEXT NOT NULL DEFAULT 'unknown');
CREATE TABLE projects(id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL);
CREATE TABLE environments(id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name TEXT NOT NULL, UNIQUE(project_id, name));
CREATE TABLE apps(id TEXT PRIMARY KEY,
  environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  server_id TEXT NOT NULL REFERENCES servers(id),
  name TEXT NOT NULL, source_type TEXT NOT NULL CHECK(source_type IN
    ('image','compose','dockerfile','static','template')),
  source_config TEXT NOT NULL DEFAULT '{}',
  env_vars TEXT NOT NULL DEFAULT '{}',
  domains TEXT NOT NULL DEFAULT '[]',
  port INTEGER,
  auto_deploy INTEGER NOT NULL DEFAULT 1, webhook_token TEXT UNIQUE,
  last_good_deployment TEXT, status TEXT NOT NULL DEFAULT 'undeployed',
  updated_at INTEGER NOT NULL, UNIQUE(environment_id, name));
CREATE TABLE deployments(id TEXT PRIMARY KEY,
  app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  status TEXT NOT NULL CHECK(status IN ('queued','running','success','failed','canceled')),
  trigger TEXT NOT NULL CHECK(trigger IN ('manual','webhook','rollback')),
  release TEXT NOT NULL,
  log_file TEXT NOT NULL, created_at INTEGER NOT NULL,
  started_at INTEGER, finished_at INTEGER);
CREATE INDEX idx_deployments_app ON deployments(app_id, created_at DESC);
CREATE TABLE databases(id TEXT PRIMARY KEY,
  environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  server_id TEXT NOT NULL REFERENCES servers(id),
  db_type TEXT NOT NULL CHECK(db_type IN ('postgres','mysql','redis','mongo')),
  name TEXT NOT NULL, config TEXT NOT NULL DEFAULT '{}', status TEXT NOT NULL DEFAULT 'undeployed',
  created_at INTEGER NOT NULL, UNIQUE(environment_id, name));
CREATE TABLE backup_schedules(database_id TEXT PRIMARY KEY REFERENCES databases(id) ON DELETE CASCADE,
  cron_spec TEXT NOT NULL DEFAULT '0 4 * * *', retention INTEGER NOT NULL DEFAULT 7, last_run_at INTEGER);
CREATE TABLE notification_channels(id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK(kind IN ('discord','slack','telegram','webhook','email')),
  config TEXT NOT NULL, events TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1);
CREATE TABLE alert_rules(id TEXT PRIMARY KEY,
  server_id TEXT REFERENCES servers(id) ON DELETE CASCADE,
  container_pattern TEXT,
  metric TEXT NOT NULL CHECK(metric IN
    ('load1','mem_pct','container_cpu_pct','container_mem_pct','container_down')),
  threshold REAL, duration_sec INTEGER NOT NULL DEFAULT 300,
  enabled INTEGER NOT NULL DEFAULT 1, state TEXT NOT NULL DEFAULT 'ok', last_fired_at INTEGER);
CREATE TABLE metric_samples(ts INTEGER NOT NULL, server_id TEXT NOT NULL,
  load1 REAL, load5 REAL, load15 REAL, mem_used_mb REAL, mem_total_mb REAL,
  PRIMARY KEY(server_id, ts));
CREATE TABLE container_samples(ts INTEGER NOT NULL, server_id TEXT NOT NULL,
  container_id TEXT NOT NULL, name TEXT NOT NULL, cpu_pct REAL, mem_pct REAL, state TEXT,
  PRIMARY KEY(server_id, container_id, ts));
CREATE INDEX idx_apps_env ON apps(environment_id);
