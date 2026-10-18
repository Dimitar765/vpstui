"use client";

// Templates: catalog grid → deploy dialog (server, environment, name,
// domain) → deep-link to the created app. The catalog endpoint may not be
// live yet — degrade to an empty-catalog state with retry.

import { useState } from "react";
import { useRouter } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { get, post } from "@/lib/api";
import type { App, Environment, Project, Template } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { RefreshCw } from "lucide-react";

export default function TemplatesPage() {
  const catalog = useQuery({
    queryKey: ["templates"],
    queryFn: () => get<Template[]>("/api/templates"),
    retry: false,
  });
  const [deploying, setDeploying] = useState<Template | null>(null);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Templates</h1>
          <p className="text-sm text-muted-foreground">
            One-click services, deployed as regular apps.
          </p>
        </div>
        <Button variant="outline" onClick={() => catalog.refetch()}>
          <RefreshCw className="h-4 w-4" /> Refresh
        </Button>
      </div>

      {catalog.isLoading ? (
        <p className="text-sm text-muted-foreground">Loading catalog…</p>
      ) : catalog.isError ? (
        <div className="rounded-md border border-dashed p-8 text-center">
          <p className="text-sm text-muted-foreground">
            Template catalog is not available right now
            {catalog.error instanceof Error ? ` (${catalog.error.message})` : ""}.
          </p>
          <Button className="mt-3" variant="outline" onClick={() => catalog.refetch()}>
            <RefreshCw className="h-4 w-4" /> Retry
          </Button>
        </div>
      ) : (catalog.data?.length ?? 0) === 0 ? (
        <div className="rounded-md border border-dashed p-8 text-center">
          <p className="text-sm text-muted-foreground">The catalog is empty.</p>
          <Button className="mt-3" variant="outline" onClick={() => catalog.refetch()}>
            <RefreshCw className="h-4 w-4" /> Retry
          </Button>
        </div>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {catalog.data!.map((t) => (
            <Card key={t.id}>
              <CardHeader className="flex flex-row items-center gap-3">
                {t.logo && (
                  // eslint-disable-next-line @next/next/no-img-element
                  <img src={t.logo} alt="" className="h-8 w-8 rounded object-contain" />
                )}
                <CardTitle className="text-base">{t.name}</CardTitle>
                <Badge variant="outline" className="ml-auto">
                  {t.category}
                </Badge>
              </CardHeader>
              <CardContent className="space-y-3">
                <p className="text-sm text-muted-foreground">{t.description}</p>
                <Button size="sm" onClick={() => setDeploying(t)}>
                  Deploy
                </Button>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      <DeployDialog template={deploying} onClose={() => setDeploying(null)} />
    </div>
  );
}

function DeployDialog({
  template,
  onClose,
}: {
  template: Template | null;
  onClose: () => void;
}) {
  const router = useRouter();
  const [name, setName] = useState("");
  const [serverId, setServerId] = useState("");
  const [environmentId, setEnvironmentId] = useState("");
  const [domain, setDomain] = useState("");
  const [busy, setBusy] = useState(false);

  const projects = useQuery({
    queryKey: ["projects"],
    queryFn: () => get<Project[]>("/api/projects"),
    enabled: !!template,
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
    enabled: !!template && !!projects.data,
  });
  const servers = useQuery({
    queryKey: ["servers"],
    queryFn: () => get<{ id: string; name: string }[]>("/api/servers"),
    enabled: !!template,
  });

  const deploy = async () => {
    if (!template || !serverId || !environmentId || !name.trim()) return;
    setBusy(true);
    try {
      const body = {
        serverId,
        environmentId,
        name: name.trim(),
        domains: domain.trim() ? [domain.trim().replace(/^https?:\/\//, "")] : [],
      };
      // The handler creates an app row (source_type=template) and returns it;
      // older shapes may return just {appId} or {deploymentId}.
      const r = await post<App & { appId?: string }>("/api/templates/" + template.id + "/deploy", body);
      toast.success(`${template.name} deployed`);
      const appId = r?.id ?? r?.appId;
      if (appId) router.push(`/apps/view?id=${appId}`);
      else onClose();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Deploy failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={!!template} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Deploy {template?.name}</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="tpl-name">Name</Label>
            <Input
              id="tpl-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={template ? template.id : "my-uptime-kuma"}
            />
          </div>
          <div className="space-y-1.5">
            <Label>Server</Label>
            <Select value={serverId} onValueChange={setServerId}>
              <SelectTrigger>
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
              <SelectTrigger>
                <SelectValue placeholder="Pick an environment" />
              </SelectTrigger>
              <SelectContent>
                {(envs.data ?? []).map((e) => (
                  <SelectItem key={e.id} value={e.id}>
                    {e.name} · {(projects.data ?? []).find((p) => p.id === e.projectId)?.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="tpl-domain">Domain (optional)</Label>
            <Input
              id="tpl-domain"
              className="font-mono text-sm"
              value={domain}
              onChange={(e) => setDomain(e.target.value)}
              placeholder="kuma.example.com"
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={!name.trim() || !serverId || !environmentId || busy}
            onClick={deploy}
          >
            {busy ? "Deploying…" : "Deploy"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
