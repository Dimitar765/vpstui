"use client";

import { Badge } from "@/components/ui/badge";

export function StatusDot({ status }: { status: string }) {
  const online = status === "online";
  const color = online
    ? "bg-emerald-500"
    : status === "offline"
      ? "bg-red-500"
      : "bg-amber-400";
  return (
    <span className="inline-flex items-center gap-1.5 text-sm capitalize">
      <span className={`h-2 w-2 rounded-full ${color}`} />
      {status}
    </span>
  );
}

export function StatusPill({ status }: { status: string }) {
  const variant =
    status === "online"
      ? "default"
      : status === "offline"
        ? "destructive"
        : "secondary";
  return (
    <Badge variant={variant} className="capitalize">
      {status}
    </Badge>
  );
}

export function StatusBadge({ status }: { status: string }) {
  const variant =
    status === "running" || status === "success" || status === "online"
      ? "default"
      : status === "failed" || status === "offline"
        ? "destructive"
        : "secondary";
  return (
    <Badge variant={variant} className="capitalize">
      {status}
    </Badge>
  );
}
