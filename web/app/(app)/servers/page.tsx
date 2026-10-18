"use client";

import { useMemo, useState } from "react";
import Link from "next/link";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { get, post } from "@/lib/api";
import type { FleetResponse, ServerRow, ServerTestResult } from "@/lib/types";
import { useFleetStream } from "@/lib/ws";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { StatusPill } from "@/components/status";
import { AddServerDialog } from "@/components/add-server-dialog";
import { toast } from "sonner";

export default function ServersPage() {
  const qc = useQueryClient();
  const [testing, setTesting] = useState<string | null>(null);
  useFleetStream();

  const { data: rows, isLoading } = useQuery({
    queryKey: ["servers"],
    queryFn: () => get<ServerRow[]>("/api/servers"),
  });
  const { data: fleet } = useQuery({
    queryKey: ["fleet"],
    queryFn: () => get<FleetResponse>("/api/fleet"),
  });
  const statusById = useMemo(() => {
    const m = new Map<string, string>();
    for (const s of fleet?.servers ?? []) m.set(s.id, s.status);
    return m;
  }, [fleet]);

  const test = async (id: string, name: string) => {
    setTesting(id);
    try {
      const res = await post<ServerTestResult>(`/api/servers/${id}/test`);
      if (res.ok) toast.success(`${name}: connection OK`);
      else toast.error(`${name}: ${res.error ?? "probe failed"}`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "test failed");
    } finally {
      setTesting(null);
    }
  };

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["servers"] });
    void qc.invalidateQueries({ queryKey: ["fleet"] });
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Servers</h1>
          <p className="text-sm text-muted-foreground">
            Managed VPSes, agentless over SSH
          </p>
        </div>
        <AddServerDialog onCreated={refresh} />
      </div>
      {isLoading ? (
        <p className="text-sm text-muted-foreground">Loading servers…</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Address</TableHead>
              <TableHead>User</TableHead>
              <TableHead>Auth</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Containers</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {(rows ?? []).map((r) => (
              <TableRow key={r.id}>
                <TableCell className="font-medium">
                  <Link
                    href={`/servers/view?id=${r.id}`}
                    className="hover:underline"
                  >
                    {r.name}
                  </Link>
                </TableCell>
                <TableCell className="font-mono text-xs">
                  {r.address}:{r.port}
                </TableCell>
                <TableCell>{r.user}</TableCell>
                <TableCell className="capitalize">{r.authType}</TableCell>
                <TableCell>
                  <StatusPill status={statusById.get(r.id) ?? r.status} />
                </TableCell>
                <TableCell>
                  {fleet?.servers.find((s) => s.id === r.id)?.containerCount ?? "—"}
                </TableCell>
                <TableCell className="space-x-1 text-right whitespace-nowrap">
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={testing === r.id}
                    onClick={() => void test(r.id, r.name)}
                  >
                    {testing === r.id ? "testing…" : "test"}
                  </Button>
                  <Button variant="ghost" size="sm" asChild>
                    <Link href={`/servers/view?id=${r.id}`}>open</Link>
                  </Button>
                </TableCell>
              </TableRow>
            ))}
            {(rows?.length ?? 0) === 0 && (
              <TableRow>
                <TableCell
                  colSpan={7}
                  className="py-8 text-center text-sm text-muted-foreground"
                >
                  No servers yet — press “Add server”.
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      )}
    </div>
  );
}
