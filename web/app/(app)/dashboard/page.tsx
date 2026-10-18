"use client";

import { useFleet, useFleetStream } from "@/lib/ws";
import { ServerCard } from "@/components/server-card";
import { ErrorsPanel } from "@/components/errors-panel";

export default function DashboardPage() {
  const { data: servers, isLoading } = useFleet();
  useFleetStream();

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold">Dashboard</h1>
        <p className="text-sm text-muted-foreground">
          Live fleet overview via /api/ws
        </p>
      </div>
      {isLoading ? (
        <p className="text-sm text-muted-foreground">Loading fleet…</p>
      ) : (servers?.length ?? 0) === 0 ? (
        <p className="rounded-md border border-dashed p-8 text-center text-sm text-muted-foreground">
          No servers registered yet — add one under Servers.
        </p>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {servers!.map((s) => (
            <ServerCard key={s.id} server={s} />
          ))}
        </div>
      )}
      <ErrorsPanel />
    </div>
  );
}
