"use client";

import Link from "next/link";
import { useFleetHistory } from "@/lib/ws";
import type { FleetServer } from "@/lib/types";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { StatusDot } from "./status";
import { Sparkline } from "./sparkline";

function memPct(s: FleetServer): number | null {
  if (!s.hostStats || !s.hostStats.memTotalMB) return null;
  return (s.hostStats.memUsedMB / s.hostStats.memTotalMB) * 100;
}

export function ServerCard({ server }: { server: FleetServer }) {
  const loadHist = useFleetHistory((s) =>
    s.id === server.id && s.hostStats ? s.hostStats.load1 : null,
  );
  const memHist = useFleetHistory((s) => (s.id === server.id ? memPct(s) : null));
  const hs = server.hostStats;

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-base">
          <Link href={`/servers/view?id=${server.id}`} className="hover:underline">
            {server.name}
          </Link>
        </CardTitle>
        <StatusDot status={server.status} />
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="text-xs text-muted-foreground">
          {server.user}@{server.address}:{server.port}
        </div>
        <div className="grid grid-cols-2 gap-4">
          <div>
            <div className="text-xs text-muted-foreground">
              load1{hs ? ` · ${hs.load1.toFixed(2)}` : ""}
            </div>
            <Sparkline data={loadHist.current.get(server.id) ?? []} max={8} />
          </div>
          <div>
            <div className="text-xs text-muted-foreground">
              mem{hs ? ` · ${memPct(server)?.toFixed(0)}% (${hs.memUsedMB}/${hs.memTotalMB} MB)` : ""}
            </div>
            <Sparkline data={memHist.current.get(server.id) ?? []} />
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Badge variant="secondary">{server.containerCount} containers</Badge>
          {server.status !== "online" && (
            <Badge variant="destructive">offline</Badge>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
