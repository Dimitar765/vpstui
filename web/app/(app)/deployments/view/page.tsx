"use client";

// Deployment detail: live log pane (WS deploy.log/deploy.status), status
// transitions, terminal footer.

import { Suspense, useEffect, useState } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { get } from "@/lib/api";
import type { App, Deployment } from "@/lib/types";
import { isTerminal, useDeploymentStream } from "@/lib/ws";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { StatusBadge } from "@/components/status";
import { DeployLogPane } from "@/components/deploy-log-pane";

function DeploymentDetail({ id }: { id: string }) {
  const qc = useQueryClient();
  const dep = useQuery({
    queryKey: ["deployment", id],
    queryFn: () => get<Deployment>(`/api/deployments/${id}`),
  });
  const stream = useDeploymentStream(id);
  const app = useQuery({
    queryKey: ["app", dep.data?.appId ?? ""],
    queryFn: () => get<App>(`/api/apps/${dep.data!.appId}`),
    enabled: !!dep.data?.appId,
  });

  // Fold WS status transitions into the query cache so the header stays live.
  useEffect(() => {
    if (stream.status) {
      qc.setQueryData<Deployment>(["deployment", id], (d) =>
        d ? { ...d, status: stream.status as Deployment["status"] } : d,
      );
    }
  }, [stream.status, qc, id]);

  const d = dep.data;
  const failed = d?.status === "failed";

  if (dep.isLoading) {
    return <p className="text-sm text-muted-foreground">Loading deployment…</p>;
  }
  if (!d) {
    return (
      <p className="text-sm text-destructive">
        Deployment not found.{" "}
        <Link href="/projects" className="underline">
          Back to projects
        </Link>
      </p>
    );
  }

  const dur =
    d.startedAt && d.finishedAt
      ? `${Math.max(1, Math.round((+new Date(d.finishedAt) - +new Date(d.startedAt)) / 1000))}s`
      : !isTerminal(d.status)
        ? "running…"
        : "—";

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="ghost" size="sm" asChild>
          <Link href={`/apps/view?id=${d.appId}`}>← {app.data?.name ?? "App"}</Link>
        </Button>
        <h1 className="text-xl font-semibold">Deployment</h1>
        <StatusBadge status={d.status} />
        <Badge variant="outline">{d.trigger}</Badge>
        {d.release && <Badge variant="secondary">release {d.release}</Badge>}
        <span className="text-xs text-muted-foreground">
          created {new Date(d.createdAt).toLocaleString()} · {dur}
        </span>
      </div>

      <DeployLogPane deploymentId={id} />

      {isTerminal(d.status) && (
        <p className="text-sm text-muted-foreground">
          {d.status === "success" && "Deployment finished successfully."}
          {d.status === "canceled" && "Deployment was canceled."}
          {failed &&
            "Deployment failed — the previous release's containers were left running; nothing was taken down."}
        </p>
      )}
    </div>
  );
}

export default function DeploymentPage() {
  return (
    <Suspense>
      <DeploymentInner />
    </Suspense>
  );
}

function DeploymentInner() {
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
  return <DeploymentDetail id={id} />;
}
