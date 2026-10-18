"use client";

// Shared app config fields: source type picker + per-type fields, env vars,
// domains, auto-deploy. Used by both the create wizard and the app detail
// edit form, so the two never drift.

import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { EnvVarsEditor } from "@/components/env-vars-editor";
import { DomainsEditor } from "@/components/domains-editor";
import type { App, SourceType } from "@/lib/types";

export interface AppConfig {
  name: string;
  sourceType: SourceType;
  sourceConfig: Record<string, string>;
  envVars: Record<string, string>;
  domains: string[];
  port: number;
  autoDeploy: boolean;
}

export function appConfigFromApp(a: App): AppConfig {
  const sc: Record<string, string> = {};
  for (const [k, v] of Object.entries(a.sourceConfig ?? {})) {
    sc[k] = typeof v === "string" ? v : JSON.stringify(v);
  }
  return {
    name: a.name,
    sourceType: a.sourceType,
    sourceConfig: sc,
    envVars: a.envVars ?? {},
    domains: a.domains ?? [],
    port: a.port,
    autoDeploy: a.autoDeploy,
  };
}

/** Strips per-type noise so only the active source type's keys are sent. */
export function cleanSourceConfig(cfg: AppConfig): Record<string, string> {
  const keep: Record<SourceType, string[]> = {
    image: ["image"],
    compose: ["compose_yaml"],
    dockerfile: ["repo", "branch", "dockerfile", "context_dir"],
    static: ["image"],
    template: ["template_id"],
  };
  const out: Record<string, string> = {};
  for (const k of keep[cfg.sourceType]) {
    if (cfg.sourceConfig[k]) out[k] = cfg.sourceConfig[k];
  }
  return out;
}

const SOURCE_TYPES: { value: SourceType; label: string }[] = [
  { value: "image", label: "Docker image" },
  { value: "compose", label: "Compose YAML" },
  { value: "dockerfile", label: "Dockerfile (git repo)" },
  { value: "static", label: "Static site" },
  { value: "template", label: "Template" },
];

export function AppConfigForm({
  value,
  onChange,
  showName = true,
}: {
  value: AppConfig;
  onChange: (next: AppConfig) => void;
  showName?: boolean;
}) {
  const set = (patch: Partial<AppConfig>) => onChange({ ...value, ...patch });
  const setCfg = (patch: Record<string, string>) =>
    set({ sourceConfig: { ...value.sourceConfig, ...patch } });

  return (
    <div className="space-y-6">
      {showName && (
        <div className="space-y-1.5">
          <Label htmlFor="app-name">Name</Label>
          <Input
            id="app-name"
            value={value.name}
            onChange={(e) => set({ name: e.target.value })}
            placeholder="my-app"
          />
        </div>
      )}

      <div className="space-y-1.5">
        <Label>Source type</Label>
        <Select
          value={value.sourceType}
          onValueChange={(v) => set({ sourceType: v as SourceType })}
        >
          <SelectTrigger className="w-72">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {SOURCE_TYPES.map((t) => (
              <SelectItem key={t.value} value={t.value}>
                {t.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {value.sourceType === "image" && (
        <div className="space-y-1.5">
          <Label htmlFor="src-image">Image</Label>
          <Input
            id="src-image"
            className="font-mono text-sm"
            value={value.sourceConfig.image ?? ""}
            onChange={(e) => setCfg({ image: e.target.value })}
            placeholder="docker.io/library/nginx:1.27-alpine"
          />
        </div>
      )}

      {value.sourceType === "compose" && (
        <div className="space-y-1.5">
          <Label htmlFor="src-compose">Compose YAML</Label>
          <Textarea
            id="src-compose"
            className="min-h-48 font-mono text-xs"
            value={value.sourceConfig.compose_yaml ?? ""}
            onChange={(e) => setCfg({ compose_yaml: e.target.value })}
            placeholder={"services:\n  web:\n    image: nginx:1.27-alpine"}
          />
        </div>
      )}

      {value.sourceType === "dockerfile" && (
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="src-repo">Git repository</Label>
            <Input
              id="src-repo"
              className="font-mono text-sm"
              value={value.sourceConfig.repo ?? ""}
              onChange={(e) => setCfg({ repo: e.target.value })}
              placeholder="https://github.com/user/repo.git"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="src-branch">Branch</Label>
            <Input
              id="src-branch"
              className="font-mono text-sm"
              value={value.sourceConfig.branch ?? ""}
              onChange={(e) => setCfg({ branch: e.target.value })}
              placeholder="main"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="src-dockerfile">Dockerfile path</Label>
            <Input
              id="src-dockerfile"
              className="font-mono text-sm"
              value={value.sourceConfig.dockerfile ?? ""}
              onChange={(e) => setCfg({ dockerfile: e.target.value })}
              placeholder="Dockerfile"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="src-context">Context directory</Label>
            <Input
              id="src-context"
              className="font-mono text-sm"
              value={value.sourceConfig.context_dir ?? ""}
              onChange={(e) => setCfg({ context_dir: e.target.value })}
              placeholder="."
            />
          </div>
        </div>
      )}

      {value.sourceType === "static" && (
        <div className="space-y-1.5">
          <Label htmlFor="src-static">Web server image (serves static files)</Label>
          <Input
            id="src-static"
            className="font-mono text-sm"
            value={value.sourceConfig.image ?? ""}
            onChange={(e) => setCfg({ image: e.target.value })}
            placeholder="docker.io/library/nginx:1.27-alpine"
          />
          <p className="text-xs text-muted-foreground">
            Static assets are uploaded to the container&apos;s web root.
          </p>
        </div>
      )}

      <div className="space-y-1.5">
        <Label>Environment variables</Label>
        <EnvVarsEditor value={value.envVars} onChange={(envVars) => set({ envVars })} />
      </div>

      <div className="space-y-1.5">
        <Label>Domains</Label>
        <DomainsEditor
          domains={value.domains}
          port={value.port}
          onDomains={(domains) => set({ domains })}
          onPort={(port) => set({ port })}
        />
      </div>

      <div className="flex items-center gap-2">
        <Switch
          id="auto-deploy"
          checked={value.autoDeploy}
          onCheckedChange={(autoDeploy) => set({ autoDeploy })}
        />
        <Label htmlFor="auto-deploy">Auto-deploy on webhook push</Label>
      </div>
    </div>
  );
}
