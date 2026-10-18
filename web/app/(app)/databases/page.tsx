"use client";

// Databases: list with status + create wizard (type, server, environment,
// name, publish switch, backup schedule cron + retention).

import { useState } from "react";
import Link from "next/link";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { get, post } from "@/lib/api";
import type { Database, DatabaseReq, Environment, Project } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { StatusBadge } from "@/components/status";
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Plus } from "lucide-react";

const DB_TYPES = ["postgres", "mysql", "redis", "mongo"] as const;

export default function DatabasesPage() {
  const qc = useQueryClient();
  const databases = useQuery({
    queryKey: ["databases"],
    queryFn: () => get<Database[]>("/api/databases"),
  });
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

  const [open, setOpen] = useState(false);
  const [dbType, setDbType] = useState<DatabaseReq["dbType"]>("postgres");
  const [name, setName] = useState("");
  const [serverId, setServerId] = useState("");
  const [environmentId, setEnvironmentId] = useState("");
  const [publish, setPublish] = useState(false);
  const [cronSpec, setCronSpec] = useState("");
  const [retention, setRetention] = useState(7);
  const [creating, setCreating] = useState(false);

  const serverName = (id: string) =>
    servers.data?.find((s) => s.id === id)?.name ?? id;

  const create = async () => {
    setCreating(true);
    try {
      const req: DatabaseReq = {
        dbType,
        name: name.trim(),
        serverId,
        environmentId,
        publish,
        ...(cronSpec.trim() ? { backup: { cronSpec: cronSpec.trim(), retention } } : {}),
      };
      await post<Database>("/api/databases", req);
      toast.success("Database provisioned");
      setOpen(false);
      qc.invalidateQueries({ queryKey: ["databases"] });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Provision failed");
    } finally {
      setCreating(false);
    }
  };

  const ready = dbType && name.trim() && serverId && environmentId;

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Databases</h1>
          <p className="text-sm text-muted-foreground">
            Managed database instances across your fleet.
          </p>
        </div>
        <Button
          onClick={() => {
            setName("");
            setServerId("");
            setEnvironmentId("");
            setDbType("postgres");
            setPublish(false);
            setCronSpec("");
            setRetention(7);
            setOpen(true);
          }}
        >
          <Plus className="h-4 w-4" /> New database
        </Button>
      </div>

      {databases.isLoading ? (
        <p className="text-sm text-muted-foreground">Loading databases…</p>
      ) : (databases.data?.length ?? 0) === 0 ? (
        <p className="rounded-md border border-dashed p-8 text-center text-sm text-muted-foreground">
          No databases yet — provision one to get started.
        </p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Type</TableHead>
              <TableHead>Server</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Backup</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {databases.data!.map((d) => (
              <TableRow key={d.id}>
                <TableCell>
                  <Link
                    href={`/databases/view?id=${d.id}`}
                    className="font-medium hover:underline"
                  >
                    {d.name}
                  </Link>
                </TableCell>
                <TableCell>
                  <Badge variant="outline">{d.dbType}</Badge>
                </TableCell>
                <TableCell className="text-sm">{serverName(d.serverId)}</TableCell>
                <TableCell>
                  <StatusBadge status={d.status} />
                </TableCell>
                <TableCell className="font-mono text-xs">
                  {d.backup ? d.backup.cronSpec : "—"}
                </TableCell>
                <TableCell>
                  <Button variant="ghost" size="sm" asChild>
                    <Link href={`/databases/view?id=${d.id}`}>open</Link>
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>New database</DialogTitle>
          </DialogHeader>
          <div className="space-y-4">
            <div className="space-y-1.5">
              <Label>Type</Label>
              <Select
                value={dbType}
                onValueChange={(v) => setDbType(v as DatabaseReq["dbType"])}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {DB_TYPES.map((t) => (
                    <SelectItem key={t} value={t}>
                      {t}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="db-name">Name</Label>
              <Input
                id="db-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="app-db"
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
            <div className="flex items-center gap-2">
              <Switch id="db-publish" checked={publish} onCheckedChange={setPublish} />
              <Label htmlFor="db-publish">
                Publish port to host (accessible outside the docker network)
              </Label>
            </div>
            <div className="space-y-1.5">
              <Label>Backup schedule (cron, optional)</Label>
              <div className="flex gap-2">
                <Input
                  className="font-mono text-sm"
                  value={cronSpec}
                  onChange={(e) => setCronSpec(e.target.value)}
                  placeholder="0 4 * * *"
                />
                <Input
                  className="w-24 font-mono text-sm"
                  type="number"
                  value={retention}
                  onChange={(e) => setRetention(Number(e.target.value) || 1)}
                  title="Retention (days)"
                />
              </div>
              <p className="text-xs text-muted-foreground">
                Leave empty for no scheduled backups. Retention = days to keep dumps.
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button disabled={!ready || creating} onClick={create}>
              {creating ? "Provisioning…" : "Create"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
