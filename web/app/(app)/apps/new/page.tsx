"use client";

// App create wizard: source step (Image / Compose / Dockerfile-git / Static /
// Template), server + environment selection, review/create, then deep-link to
// the app detail for deploy.

import { Suspense, useMemo, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { get, post } from "@/lib/api";
import type { App, AppReq, Environment, Project, Template } from "@/lib/types";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Label } from "@/components/ui/label";
import {
  AppConfigForm,
  cleanSourceConfig,
  type AppConfig,
} from "@/components/app-config-form";

function WizardInner() {
  const router = useRouter();
  const params = useSearchParams();
  const initialEnv = params.get("environmentId") ?? "";
  const initialTemplate = params.get("templateId") ?? "";

  const servers = useQuery({
    queryKey: ["servers"],
    queryFn: () => get<{ id: string; name: string }[]>("/api/servers"),
  });
  const projects = useQuery({
    queryKey: ["projects"],
    queryFn: () => get<Project[]>("/api/projects"),
  });
  const envs = useQuery({
    queryKey: ["environments-all"],
    queryFn: async () => {
      const out: Environment[] = [];
      for (const p of projects.data ?? []) {
        out.push(...((await get<{ environments: Environment[] }>(`/api/projects/${p.id}`)).environments ?? []));
      }
      return out;
    },
    enabled: !!projects.data,
  });
  const templates = useQuery({
    queryKey: ["templates"],
    queryFn: () => get<Template[]>("/api/templates"),
  });

  const [step, setStep] = useState(initialTemplate ? 1 : 0);
  const [config, setConfig] = useState<AppConfig>(() => ({
    name: "",
    sourceType: initialTemplate ? "template" : "image",
    sourceConfig: (initialTemplate ? { template_id: initialTemplate } : {}) as Record<string, string>,
    envVars: {},
    domains: [],
    port: 80,
    autoDeploy: true,
  }));
  const [serverId, setServerId] = useState("");
  const [environmentId, setEnvironmentId] = useState(initialEnv);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const envById = useMemo(
    () => new Map((envs.data ?? []).map((e) => [e.id, e])),
    [envs.data],
  );

  const canSource = config.sourceType !== "template" || !!config.sourceConfig.template_id;
  const canProceed =
    canSource &&
    (config.sourceType === "template" ||
      config.sourceType === "compose" ||
      Object.values(config.sourceConfig).some((v) => !!v));

  const create = async () => {
    setCreating(true);
    setError(null);
    const req: AppReq = {
      environmentId,
      serverId,
      name: config.name.trim(),
      sourceType: config.sourceType,
      sourceConfig: cleanSourceConfig(config),
      envVars: Object.fromEntries(
        Object.entries(config.envVars).filter(([k]) => k.trim() !== ""),
      ),
      domains: config.domains,
      port: config.port,
      autoDeploy: config.autoDeploy,
    };
    try {
      const app = await post<App>("/api/apps", req);
      router.push(`/apps/view?id=${app.id}`);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setCreating(false);
    }
  };

  const ready = !!serverId && !!environmentId && config.name.trim() !== "";

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <div>
        <h1 className="text-xl font-semibold">Create app</h1>
        <p className="text-sm text-muted-foreground">
          Step {step + 1} of 2 — {step === 0 ? "source" : "server & environment"}
        </p>
      </div>

      {step === 0 && (
        <div className="space-y-6">
          <AppConfigForm value={config} onChange={setConfig} />
          {config.sourceType === "template" && (
            <div className="space-y-1.5">
              <Label>Template</Label>
              <Select
                value={config.sourceConfig.template_id ?? ""}
                onValueChange={(v) => setConfig((c) => ({
                  ...c,
                  sourceConfig: { ...c.sourceConfig, template_id: v },
                }))}
              >
                <SelectTrigger className="w-72">
                  <SelectValue placeholder="Pick a template" />
                </SelectTrigger>
                <SelectContent>
                  {(templates.data ?? []).map((t) => (
                    <SelectItem key={t.id} value={t.id}>
                      {t.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
          <div className="flex justify-end">
            <Button disabled={!canProceed} onClick={() => setStep(1)}>
              Next
            </Button>
          </div>
        </div>
      )}

      {step === 1 && (
        <div className="space-y-6">
          <div className="space-y-1.5">
            <Label>Server</Label>
            <Select value={serverId} onValueChange={setServerId}>
              <SelectTrigger className="w-72">
                <SelectValue placeholder="Pick a server" />
              </SelectTrigger>
              <SelectContent>
                {(servers.data ?? []).map((s) => (
                  <SelectItem key={s.id} value={s.id}>
                    {s.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label>Environment</Label>
            <Select value={environmentId} onValueChange={setEnvironmentId}>
              <SelectTrigger className="w-72">
                <SelectValue placeholder="Pick an environment" />
              </SelectTrigger>
              <SelectContent>
                {(envs.data ?? []).map((e) => (
                  <SelectItem key={e.id} value={e.id}>
                    {envById.get(e.id)?.name ?? e.name}
                    {" · "}
                    {(projects.data ?? []).find((p) => p.id === e.projectId)?.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {(envs.data?.length ?? 0) === 0 && (
              <p className="text-xs text-muted-foreground">
                No environments yet — create a project under Projects first.
              </p>
            )}
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
          <div className="flex justify-between">
            <Button variant="outline" onClick={() => setStep(0)}>
              Back
            </Button>
            <Button disabled={!ready || creating} onClick={create}>
              {creating ? "Creating…" : "Create app"}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

export default function NewAppPage() {
  return (
    <Suspense>
      <WizardInner />
    </Suspense>
  );
}
