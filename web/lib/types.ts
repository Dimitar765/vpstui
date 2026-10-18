// Mirrors internal/apitypes/apitypes.go and the server DTOs. Field names are
// the JSON tags — do not rename without changing the Go side.
export interface HostStats {
  load1: number;
  load5: number;
  load15: number;
  memTotalMB: number;
  memUsedMB: number;
  numCPU: number;
  uptimeSec: number;
}

export interface Container {
  id: string;
  name: string;
  tag?: string;
  tagColor?: string;
  image: string;
  state: string;
  status: string;
  cpu: string;
  memPerc: string;
  memUsage: string;
  cpuPct?: number | null;
  memPct?: number | null;
}

export interface FleetServer {
  id: string;
  name: string;
  address: string;
  port: number;
  user: string;
  authType: string;
  keyPath?: string;
  baseDir: string;
  proxyHttpPort: number;
  proxyHttpsPort: number;
  monitorIntervalSec: number;
  status: string;
  lastSeenAt?: string | null;
  hasHostStats: boolean;
  hostStats?: HostStats | null;
  containers: Container[];
  containerCount: number;
  fetchedAt?: string | null;
}

export interface FleetResponse {
  servers: FleetServer[];
}

export interface FleetUpdateFrame {
  type: "fleet.update";
  servers: FleetServer[];
}

export interface ServerRow {
  id: string;
  name: string;
  address: string;
  port: number;
  user: string;
  authType: string;
  keyPath?: string;
  baseDir: string;
  monitorIntervalSec: number;
  proxyHttpPort: number;
  proxyHttpsPort: number;
  status: string;
  createdAt: string;
  lastSeenAt?: string | null;
  hasPassword: boolean;
  hasPassphrase: boolean;
}

export interface ServerTestResult {
  ok: boolean;
  error?: string;
}

export interface ErrorSample {
  host: string;
  container: string;
  sev: string;
  stamp?: string;
  text: string;
}

export interface ErrorGroup {
  sig: string;
  sev: string;
  count: number;
  hosts: string[];
  containers: string[];
  firstSeen: string;
  lastSeen: string;
  sample?: ErrorSample;
  examples: ErrorSample[];
}

export interface Me {
  id: string;
  email: string;
}

// ---------------------------------------------------------------- Phase C4

// App / Project / Environment / Deployment mirror internal/apitypes verbatim.
export type SourceType = "image" | "compose" | "dockerfile" | "static" | "template";

export interface App {
  id: string;
  environmentId: string;
  serverId: string;
  name: string;
  sourceType: SourceType;
  sourceConfig: Record<string, unknown>;
  envVars: Record<string, string>;
  domains: string[];
  port: number;
  autoDeploy: boolean;
  webhookToken: string;
  lastGoodDeployment?: string;
  status: string;
  updatedAt: string;
}

export interface Project {
  id: string;
  name: string;
  createdAt: string;
}

export interface Environment {
  id: string;
  projectId: string;
  name: string;
}

export interface Deployment {
  id: string;
  appId: string;
  status: "queued" | "running" | "success" | "failed" | "canceled";
  trigger: "manual" | "webhook" | "rollback";
  release: string;
  createdAt: string;
  startedAt?: string | null;
  finishedAt?: string | null;
}

export interface DeployLogFrame {
  type: "deploy.log" | "deploy.status";
  deploymentId: string;
  line?: string;
  done?: boolean;
  status?: string;
}

export interface AppReq {
  environmentId: string;
  serverId: string;
  name: string;
  sourceType: SourceType;
  sourceConfig: Record<string, unknown>;
  envVars: Record<string, string>;
  domains: string[];
  port: number;
  autoDeploy: boolean;
}

export interface DeployResp {
  deploymentId: string;
}

export interface Database {
  id: string;
  environmentId: string;
  serverId: string;
  dbType: "postgres" | "mysql" | "redis" | "mongo";
  name: string;
  config: Record<string, unknown>;
  status: string;
  createdAt: string;
  backup?: BackupSchedule | null;
}

export interface BackupSchedule {
  databaseId: string;
  cronSpec: string;
  retention: number;
  lastRunAt?: string | null;
}

export interface DatabaseReq {
  environmentId: string;
  serverId: string;
  dbType: Database["dbType"];
  name: string;
  publish: boolean;
  port?: number;
  backup?: { cronSpec: string; retention: number };
}

export interface Template {
  id: string;
  name: string;
  category: string;
  description: string;
  logo?: string;
  type: "image" | "compose";
  image?: string;
  port?: number;
}

export interface ApiKey {
  id: string;
  name: string;
  token?: string;
  createdAt: string;
  lastUsedAt?: string | null;
}

export interface NotificationChannel {
  id: string;
  kind: "discord" | "slack" | "telegram" | "webhook" | "email";
  config: Record<string, string>;
  events: string[];
  enabled: boolean;
}

export const NOTIFY_EVENTS = [
  "deploy.success",
  "deploy.failed",
  "server.offline",
  "alert.fired",
  "alert.resolved",
] as const;

export interface AlertRule {
  id: string;
  serverId?: string | null;
  metric: "load1" | "mem_pct" | "container_cpu_pct" | "container_mem_pct" | "container_down";
  threshold: number;
  durationSec: number;
  containerPattern?: string | null;
  enabled: boolean;
  state: "ok" | "firing";
  lastFiredAt?: string | null;
}
