"use client";

// Environment detail. Query-param route (`?projectId=…`) instead of
// /projects/[id]: static export cannot prerender dynamic segments.
import { Suspense } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { get } from "@/lib/api";
import type { App, Environment, Project } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { StatusBadge } from "@/components/status";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Plus } from "lucide-react";

function EnvDetail({ id }: { id: string }) {
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
  const apps = useQuery({ queryKey: ["apps"], queryFn: () => get<App[]>("/api/apps") });

  const env = envs.data?.find((e) => e.id === id);
  const envApps = (apps.data ?? []).filter((a) => a.environmentId === id);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Button variant="ghost" size="sm" asChild>
            <Link href="/projects">← Projects</Link>
          </Button>
          <h1 className="text-xl font-semibold">{env?.name ?? "Environment"}</h1>
        </div>
        <Button asChild>
          <Link href={`/apps/new?environmentId=${id}`}>
            <Plus className="h-4 w-4" /> New app
          </Link>
        </Button>
      </div>

      {apps.isLoading ? (
        <p className="text-sm text-muted-foreground">Loading apps…</p>
      ) : envApps.length === 0 ? (
        <p className="rounded-md border border-dashed p-8 text-center text-sm text-muted-foreground">
          No apps in this environment yet — create one.
        </p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Source</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Domains</TableHead>
              <TableHead>Updated</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {envApps.map((a) => (
              <TableRow key={a.id}>
                <TableCell>
                  <Link href={`/apps/view?id=${a.id}`} className="font-medium hover:underline">
                    {a.name}
                  </Link>
                </TableCell>
                <TableCell>
                  <Badge variant="outline">{a.sourceType}</Badge>
                </TableCell>
                <TableCell>
                  <StatusBadge status={a.status} />
                </TableCell>
                <TableCell className="font-mono text-xs">
                  {a.domains?.join(", ") || "—"}
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {new Date(a.updatedAt).toLocaleString()}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}

export default function EnvironmentPage() {
  return (
    <Suspense>
      <EnvDetailInner />
    </Suspense>
  );
}

function EnvDetailInner() {
  const params = useSearchParams();
  const id = params.get("projectId") ?? "";
  if (!id) {
    return (
      <p className="text-sm text-destructive">
        Missing projectId. <Link href="/projects" className="underline">Back to projects</Link>
      </p>
    );
  }
  return <EnvDetail id={id} />;
}
