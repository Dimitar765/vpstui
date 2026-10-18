"use client";

// Per-server containers table with start/stop/restart/remove, mirroring the
// TUI rule: remove stays disabled while a container is running.

import { useState } from "react";
import { post } from "@/lib/api";
import type { Container } from "@/lib/types";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { toast } from "sonner";

type Action = "start" | "stop" | "restart" | "remove";

export function ContainersTable({
  serverId,
  containers,
  onChanged,
}: {
  serverId: string;
  containers: Container[];
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState<string | null>(null);

  const act = async (cid: string, action: Action) => {
    setBusy(`${cid}:${action}`);
    try {
      await post(`/api/servers/${serverId}/containers/${cid}/${action}`);
      toast(`Container ${action} ok`);
      onChanged();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : `${action} failed`);
    } finally {
      setBusy(null);
    }
  };

  if (containers.length === 0) {
    return (
      <p className="py-6 text-center text-sm text-muted-foreground">
        No containers on this server.
      </p>
    );
  }

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Name</TableHead>
          <TableHead>Image</TableHead>
          <TableHead>State</TableHead>
          <TableHead>Status</TableHead>
          <TableHead>CPU</TableHead>
          <TableHead>Memory</TableHead>
          <TableHead className="text-right">Actions</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {containers.map((c) => {
          const running = c.state === "running";
          return (
            <TableRow key={c.id}>
              <TableCell className="font-medium">{c.name}</TableCell>
              <TableCell className="max-w-56 truncate font-mono text-xs">
                {c.image}
              </TableCell>
              <TableCell>
                <Badge
                  variant={running ? "default" : c.state === "exited" ? "secondary" : "outline"}
                  className="capitalize"
                >
                  {c.state}
                </Badge>
              </TableCell>
              <TableCell className="text-xs text-muted-foreground">
                {c.status}
              </TableCell>
              <TableCell>{c.cpu}</TableCell>
              <TableCell>
                {c.memUsage} {c.memPerc && c.memPerc !== "-" ? `(${c.memPerc})` : ""}
              </TableCell>
              <TableCell className="space-x-1 text-right whitespace-nowrap">
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={running || busy !== null}
                  onClick={() => void act(c.id, "start")}
                >
                  start
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={!running || busy !== null}
                  onClick={() => void act(c.id, "stop")}
                >
                  stop
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={!running || busy !== null}
                  onClick={() => void act(c.id, "restart")}
                >
                  restart
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  className="text-destructive"
                  // TUI rule: a running container must be stopped before remove.
                  disabled={running || busy !== null}
                  onClick={() => void act(c.id, "remove")}
                >
                  remove
                </Button>
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}
