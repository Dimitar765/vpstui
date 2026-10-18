"use client";

// Database detail: connection URI (reveal + copy), backup schedule edit,
// Backup-now, backup history table (endpoint may land later — degrade to an
// empty-history state).

import { Suspense, useState } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { copyText } from "@/lib/clipboard";
import { ApiError, get, post, put } from "@/lib/api";
import type { BackupSchedule, Database } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { StatusBadge } from "@/components/status";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Check, Copy, Eye, EyeOff, HardDriveDownload } from "lucide-react";

interface BackupEntry {
  file: string;
  sizeBytes?: number;
  createdAt?: string;
  takenAt?: string;
}

function DbDetail({ id }: { id: string }) {
  const qc = useQueryClient();
  const db = useQuery({
    queryKey: ["database", id],
    queryFn: () => get<Database>(`/api/databases/${id}`),
    retry: false,
  });
  const [uri, setUri] = useState<string | null>(null);
  const [copiedUri, setCopiedUri] = useState(false);
  const [cronSpec, setCronSpec] = useState<string | null>(null);
  const [retention, setRetention] = useState<number | null>(null);
  const [backups, setBackups] = useState<BackupEntry[] | null>(null);
  const [backupsUnsupported, setBackupsUnsupported] = useState(false);
  const [backingUp, setBackingUp] = useState(false);

  const d = db.data;
  const cronDraft = cronSpec ?? d?.backup?.cronSpec ?? "";
  const retentionDraft = retention ?? d?.backup?.retention ?? 7;

  const revealUri = () => {
    if (uri !== null) {
      setUri(null);
      return;
    }
    get<{ uri: string }>(`/api/databases/${id}/connection`)
      .then((r) => setUri(r.uri))
      .catch((e: unknown) =>
        toast.error(e instanceof Error ? e.message : "Failed to fetch connection URI"),
      );
  };

  const saveSchedule = async () => {
    try {
      await put(`/api/databases/${id}`, {
        backup: { cronSpec: cronDraft.trim(), retention: retentionDraft },
      } as Partial<Database>);
      toast.success("Backup schedule saved");
      qc.invalidateQueries({ queryKey: ["database", id] });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Save failed");
    }
  };

  const backupNow = async () => {
    setBackingUp(true);
    try {
      const r = await post<{ file: string }>(`/api/databases/${id}/backup`);
      toast.success(`Backup written: ${r.file}`);
      qc.invalidateQueries({ queryKey: ["database", id] });
      loadBackups(true);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Backup failed");
    } finally {
      setBackingUp(false);
    }
  };

  const loadBackups = async (force = false) => {
    if (backups !== null && !force) return;
    try {
      const list = await get<BackupEntry[]>(`/api/databases/${id}/backups`);
      setBackups(list ?? []);
      setBackupsUnsupported(false);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) {
        setBackupsUnsupported(true);
        setBackups([]);
      } else {
        setBackups([]);
      }
    }
  };

  if (db.isLoading) {
    return <p className="text-sm text-muted-foreground">Loading database…</p>;
  }
  if (!d) {
    return (
      <p className="text-sm text-destructive">
        Database not found.{" "}
        <Link href="/databases" className="underline">
          Back to databases
        </Link>
      </p>
    );
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="ghost" size="sm" asChild>
          <Link href="/databases">← Databases</Link>
        </Button>
        <h1 className="text-xl font-semibold">{d.name}</h1>
        <StatusBadge status={d.status} />
      </div>

      <div className="flex flex-wrap items-center gap-2 rounded-md border p-3">
        <span className="text-sm text-muted-foreground">Connection URI</span>
        <code className="max-w-md truncate rounded bg-muted px-2 py-0.5 font-mono text-xs">
          {uri === null ? "••••••••••••••••" : uri}
        </code>
        <Button variant="ghost" size="icon" title="Reveal URI" onClick={revealUri}>
          {uri === null ? <Eye className="h-4 w-4" /> : <EyeOff className="h-4 w-4" />}
        </Button>
        <Button
          variant="ghost"
          size="icon"
          title="Copy URI"
          disabled={uri === null}
          onClick={() => {
            copyText(uri ?? "").then((ok) => {
              if (!ok) toast.error("Copy failed — select the text manually");
              else {
                setCopiedUri(true);
                setTimeout(() => setCopiedUri(false), 1500);
              }
            });
          }}
        >
          {copiedUri ? (
            <Check className="h-4 w-4 text-emerald-500" />
          ) : (
            <Copy className="h-4 w-4" />
          )}
        </Button>
      </div>

      <div className="space-y-3 rounded-md border p-4">
        <h2 className="font-medium">Backup schedule</h2>
        <div className="flex flex-wrap items-end gap-3">
          <div className="space-y-1.5">
            <Label htmlFor="cron">Cron spec</Label>
            <Input
              id="cron"
              className="w-48 font-mono text-sm"
              value={cronDraft}
              onChange={(e) => setCronSpec(e.target.value)}
              placeholder="0 4 * * *"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="ret">Retention (days)</Label>
            <Input
              id="ret"
              type="number"
              className="w-28 font-mono text-sm"
              value={retentionDraft}
              onChange={(e) => setRetention(Number(e.target.value) || 1)}
            />
          </div>
          <Button variant="outline" onClick={saveSchedule}>
            Save schedule
          </Button>
          <Button onClick={backupNow} disabled={backingUp}>
            <HardDriveDownload className="h-4 w-4" />
            {backingUp ? "Backing up…" : "Backup now"}
          </Button>
        </div>
        {d.backup?.lastRunAt && (
          <p className="text-xs text-muted-foreground">
            last run: {new Date(d.backup.lastRunAt).toLocaleString()}
          </p>
        )}
      </div>

      <div className="space-y-2">
        <h2 className="font-medium">Backup history</h2>
        {backupsUnsupported && (
          <p className="text-xs text-muted-foreground">
            Backup history endpoint not available yet.
          </p>
        )}
        {backups === null ? (
          <Button variant="outline" size="sm" onClick={() => loadBackups()}>
            Load history
          </Button>
        ) : backups.length === 0 ? (
          <p className="rounded-md border border-dashed p-6 text-center text-sm text-muted-foreground">
            No backups recorded yet.
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>File</TableHead>
                <TableHead>Size</TableHead>
                <TableHead>Taken</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {backups.map((b) => (
                <TableRow key={b.file}>
                  <TableCell className="font-mono text-xs">{b.file}</TableCell>
                  <TableCell className="text-xs">
                    {b.sizeBytes ? `${(b.sizeBytes / 1024).toFixed(1)} KiB` : "—"}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {b.takenAt || b.createdAt
                      ? new Date((b.takenAt || b.createdAt)!).toLocaleString()
                      : "—"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>
    </div>
  );
}

export default function DatabaseDetailPage() {
  return (
    <Suspense>
      <DbDetailInner />
    </Suspense>
  );
}

function DbDetailInner() {
  const params = useSearchParams();
  const id = params.get("id") ?? "";
  if (!id) {
    return (
      <p className="text-sm text-destructive">
        Missing id.{" "}
        <Link href="/databases" className="underline">
          Back to databases
        </Link>
      </p>
    );
  }
  return <DbDetail id={id} />;
}
