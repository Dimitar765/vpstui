"use client";

// App detail: status header, Deploy/Rollback (confirmed), deployments
// timeline, config edit, webhook URL, delete.

import { Suspense, useEffect, useState } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { copyText } from "@/lib/clipboard";
import { get, post, put, del } from "@/lib/api";
import type { App, AppReq, Deployment } from "@/lib/types";
import { isTerminal } from "@/lib/ws";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { StatusBadge } from "@/components/status";
import {
  AppConfigForm,
  appConfigFromApp,
  cleanSourceConfig,
  type AppConfig,
} from "@/components/app-config-form";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Check, Copy, Pencil, Rocket, Trash2, Undo2 } from "lucide-react";

function AppDetail({ id }: { id: string }) {
  const qc = useQueryClient();
  const app = useQuery({
    queryKey: ["app", id],
    queryFn: () => get<App>(`/api/apps/${id}`),
    retry: false,
  });
  const deployments = useQuery({
    queryKey: ["deployments", id],
    queryFn: () => get<Deployment[]>(`/api/apps/${id}/deployments`),
  });

  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<AppConfig | null>(null);
  const [confirmDeploy, setConfirmDeploy] = useState(false);
  const [confirmRollback, setConfirmRollback] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [copiedHook, setCopiedHook] = useState(false);

  const a = app.data;

  useEffect(() => {
    if (a && !draft) setDraft(appConfigFromApp(a));
  }, [a, draft]);

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["app", id] });
    qc.invalidateQueries({ queryKey: ["deployments", id] });
    qc.invalidateQueries({ queryKey: ["apps"] });
  };

  const hookUrl =
    typeof window !== "undefined" && a
      ? `${window.location.origin}/api/hooks/${a.webhookToken}`
      : "";

  const doDeploy = async () => {
    setConfirmDeploy(false);
    try {
      const r = await post<{ deploymentId: string }>(`/api/apps/${id}/deploy`);
      toast.success("Deployment queued");
      if (r?.deploymentId) {
        window.location.href = `/deployments/view?id=${r.deploymentId}`;
        return;
      }
      refresh();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Deploy failed");
    }
  };

  const doRollback = async () => {
    setConfirmRollback(false);
    try {
      const r = await post<{ deploymentId: string }>(`/api/apps/${id}/rollback`);
      toast.success("Rollback queued");
      if (r?.deploymentId) {
        window.location.href = `/deployments/view?id=${r.deploymentId}`;
        return;
      }
      refresh();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Rollback failed");
    }
  };

  const saveConfig = async () => {
    if (!draft || !a) return;
    const req: AppReq = {
      environmentId: a.environmentId,
      serverId: a.serverId,
      name: draft.name.trim(),
      sourceType: draft.sourceType,
      sourceConfig: cleanSourceConfig(draft),
      envVars: Object.fromEntries(
        Object.entries(draft.envVars).filter(([k]) => k.trim() !== ""),
      ),
      domains: draft.domains,
      port: draft.port,
      autoDeploy: draft.autoDeploy,
    };
    try {
      await put(`/api/apps/${id}`, req);
      toast.success("Config saved");
      setEditing(false);
      setDraft(null);
      refresh();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Save failed");
    }
  };

  const toggleAutoDeploy = async (autoDeploy: boolean) => {
    if (!a) return;
    await put(`/api/apps/${id}`, {
      environmentId: a.environmentId,
      serverId: a.serverId,
      name: a.name,
      sourceType: a.sourceType,
      sourceConfig: a.sourceConfig,
      envVars: a.envVars,
      domains: a.domains,
      port: a.port,
      autoDeploy,
    });
    refresh();
  };

  if (app.isLoading) {
    return <p className="text-sm text-muted-foreground">Loading app…</p>;
  }
  if (!a) {
    return (
      <p className="text-sm text-destructive">
        App not found.{" "}
        <Link href="/projects" className="underline">
          Back to projects
        </Link>
      </p>
    );
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="ghost" size="sm" asChild>
          <Link href={`/projects/view?projectId=${a.environmentId}`}>
            ← Environment
          </Link>
        </Button>
        <h1 className="text-xl font-semibold">{a.name}</h1>
        <StatusBadge status={a.status} />
        <Badge variant="outline">{a.sourceType}</Badge>
        <div className="ml-auto flex items-center gap-2">
          <Button onClick={() => setConfirmDeploy(true)}>
            <Rocket className="h-4 w-4" /> Deploy
          </Button>
          <Button
            variant="outline"
            disabled={!a.lastGoodDeployment}
            title={
              a.lastGoodDeployment
                ? `Roll back to release ${a.lastGoodDeployment}`
                : "No successful deployment to roll back to"
            }
            onClick={() => setConfirmRollback(true)}
          >
            <Undo2 className="h-4 w-4" /> Rollback
          </Button>
          <Button
            variant="ghost"
            size="icon"
            title="Edit config"
            onClick={() => {
              setDraft(appConfigFromApp(a));
              setEditing(true);
            }}
          >
            <Pencil className="h-4 w-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            title={confirmDelete ? "Click again to confirm" : "Delete app"}
            onClick={() => {
              if (confirmDelete) {
                del(`/api/apps/${id}`).then(() => {
                  window.location.href = "/projects";
                });
              } else {
                setConfirmDelete(true);
              }
            }}
          >
            <Trash2 className={`h-4 w-4 ${confirmDelete ? "" : "opacity-60"} text-destructive`} />
          </Button>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-4 rounded-md border p-3">
        <div className="flex items-center gap-2">
          <span className="text-sm text-muted-foreground">Webhook URL</span>
          <code className="max-w-md truncate rounded bg-muted px-2 py-0.5 font-mono text-xs">
            {hookUrl}
          </code>
          <Button
            variant="ghost"
            size="icon"
            title="Copy webhook URL"
            onClick={() => {
              copyText(hookUrl).then((ok) => {
                if (!ok) toast.error("Copy failed — select the text manually");
                else {
                  setCopiedHook(true);
                  setTimeout(() => setCopiedHook(false), 1500);
                }
              });
            }}
          >
            {copiedHook ? (
              <Check className="h-4 w-4 text-emerald-500" />
            ) : (
              <Copy className="h-4 w-4" />
            )}
          </Button>
        </div>
        <div className="flex items-center gap-2">
          <Switch
            id="ad-toggle"
            checked={a.autoDeploy}
            onCheckedChange={toggleAutoDeploy}
          />
          <Label htmlFor="ad-toggle">Auto-deploy</Label>
        </div>
        {a.lastGoodDeployment && (
          <span className="text-xs text-muted-foreground">
            last good release: <code className="font-mono">{a.lastGoodDeployment}</code>
          </span>
        )}
      </div>

      <div>
        <h2 className="mb-2 font-medium">Deployments</h2>
        {(deployments.data?.length ?? 0) === 0 ? (
          <p className="rounded-md border border-dashed p-6 text-center text-sm text-muted-foreground">
            No deployments yet — hit Deploy to ship the first release.
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Status</TableHead>
                <TableHead>Trigger</TableHead>
                <TableHead>Release</TableHead>
                <TableHead>Duration</TableHead>
                <TableHead>Created</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {deployments.data!.map((d) => {
                const dur =
                  d.startedAt && d.finishedAt
                    ? `${Math.max(1, Math.round((+new Date(d.finishedAt) - +new Date(d.startedAt)) / 1000))}s`
                    : d.status === "running" || d.status === "queued"
                      ? "…"
                      : "—";
                return (
                  <TableRow key={d.id}>
                    <TableCell>
                      <StatusBadge status={d.status} />
                    </TableCell>
                    <TableCell>
                      <Badge variant="outline">{d.trigger}</Badge>
                    </TableCell>
                    <TableCell className="font-mono text-xs">{d.release || "—"}</TableCell>
                    <TableCell className="text-xs">{dur}</TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {new Date(d.createdAt).toLocaleString()}
                    </TableCell>
                    <TableCell>
                      <Button variant="ghost" size="sm" asChild>
                        <Link href={`/deployments/view?id=${d.id}`}>logs</Link>
                      </Button>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </div>

      {editing && draft && (
        <Dialog open onOpenChange={(v) => !v && setEditing(false)}>
          <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
            <DialogHeader>
              <DialogTitle>Edit app config</DialogTitle>
            </DialogHeader>
            <AppConfigForm value={draft} onChange={setDraft} />
            <DialogFooter>
              <Button variant="outline" onClick={() => setEditing(false)}>
                Cancel
              </Button>
              <Button onClick={saveConfig}>Save</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}

      <Dialog open={confirmDeploy} onOpenChange={setConfirmDeploy}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Deploy {a.name}?</DialogTitle>
            <DialogDescription>
              A new deployment will be queued and run on the app&apos;s server.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmDeploy(false)}>
              Cancel
            </Button>
            <Button onClick={doDeploy}>Deploy</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={confirmRollback} onOpenChange={setConfirmRollback}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Roll back {a.name}?</DialogTitle>
            <DialogDescription>
              Restores release{" "}
              <code className="font-mono">{a.lastGoodDeployment}</code>.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmRollback(false)}>
              Cancel
            </Button>
            <Button onClick={doRollback}>Rollback</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

export default function AppDetailPage() {
  return (
    <Suspense>
      <AppDetailInner />
    </Suspense>
  );
}

function AppDetailInner() {
  const params = useSearchParams();
  const id = params.get("id") ?? "";
  if (!id) {
    return (
      <p className="text-sm text-destructive">
        Missing id.{" "}
        <Link href="/projects" className="underline">
          Back to projects
        </Link>
      </p>
    );
  }
  return <AppDetail id={id} />;
}
