"use client";

// Server detail. Query-param route (`?id=…`) instead of /servers/[id]:
// static export cannot prerender dynamic segments from live data.
import { Suspense } from "react";
import { useState } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { get, ApiError } from "@/lib/api";
import type { FleetResponse, ServerRow } from "@/lib/types";
import { useFleetStream } from "@/lib/ws";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { StatusPill } from "@/components/status";
import { ContainersTable } from "@/components/containers-table";

function Detail({ id }: { id: string }) {
  const qc = useQueryClient();
  useFleetStream();
  const [err, setErr] = useState<string | null>(null);

  const { data: row } = useQuery({
    queryKey: ["server", id],
    queryFn: () => get<ServerRow>(`/api/servers/${id}`),
    retry: false,
  });
  const { data: fleet, isLoading } = useQuery({
    queryKey: ["fleet"],
    queryFn: () => get<FleetResponse>("/api/fleet"),
  });
  const server = fleet?.servers.find((s) => s.id === id) ?? null;
  const online = server?.status === "online";

  if (!row && !isLoading) {
    if (err === null) {
      // Distinguish 404 from transient failure once.
      get<ServerRow>(`/api/servers/${id}`).catch((e: unknown) => {
        if (e instanceof ApiError && e.status === 404) setErr("Server not found.");
        else setErr("Waiting for fleet data…");
      });
    }
    return (
      <p className="text-sm text-destructive">
        {err ?? "Loading…"}{" "}
        <Link href="/servers" className="underline">
          back to servers
        </Link>
      </p>
    );
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <Button variant="ghost" size="sm" asChild>
          <Link href="/servers">← Servers</Link>
        </Button>
        <h1 className="text-xl font-semibold">{row?.name ?? id}</h1>
        {server && <StatusPill status={server.status} />}
      </div>

      {server && !online && (
        <div className="rounded-md border border-destructive/40 bg-destructive/10 p-4 text-sm">
          Server is offline — last seen{" "}
          {server.lastSeenAt ? new Date(server.lastSeenAt).toLocaleString() : "never"}.
          Live data is stale until it comes back.
        </div>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Connection</CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-2 gap-x-8 gap-y-2 text-sm md:grid-cols-3">
          <Field label="Address" value={row ? `${row.address}:${row.port}` : ""} mono />
          <Field label="User" value={row?.user ?? ""} />
          <Field label="Auth" value={row?.authType ?? ""} />
          <Field label="Key path" value={row?.keyPath || "—"} mono />
          <Field label="Base dir" value={row?.baseDir ?? ""} mono />
          <Field
            label="Proxy ports"
            value={row ? `${row.proxyHttpPort} / ${row.proxyHttpsPort}` : ""}
          />
          <Field label="Monitor interval" value={row ? `${row.monitorIntervalSec}s` : ""} />
          <Field
            label="Host stats"
            value={
              server?.hostStats
                ? `load1 ${server.hostStats.load1.toFixed(2)} · mem ${server.hostStats.memUsedMB}/${server.hostStats.memTotalMB} MB`
                : "—"
            }
          />
          <Field
            label="Uptime"
            value={
              server?.hostStats
                ? `${Math.floor(server.hostStats.uptimeSec / 3600)}h ${Math.floor((server.hostStats.uptimeSec % 3600) / 60)}m`
                : "—"
            }
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Containers</CardTitle>
        </CardHeader>
        <CardContent>
          {server ? (
            <ContainersTable
              serverId={id}
              containers={server.containers ?? []}
              onChanged={() => void qc.invalidateQueries({ queryKey: ["fleet"] })}
            />
          ) : (
            <p className="text-sm text-muted-foreground">No snapshot yet.</p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className={mono ? "font-mono text-xs break-all" : "break-all"}>{value}</div>
    </div>
  );
}

export default function ServerDetailPage() {
  return (
    <Suspense fallback={<p className="text-sm text-muted-foreground">Loading…</p>}>
      <DetailFromParams />
    </Suspense>
  );
}

function DetailFromParams() {
  const params = useSearchParams();
  return <Detail id={params.get("id") ?? ""} />;
}
