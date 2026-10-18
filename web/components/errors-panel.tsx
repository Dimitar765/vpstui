"use client";

import { useQuery } from "@tanstack/react-query";
import { get } from "@/lib/api";
import type { ErrorGroup } from "@/lib/types";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Copy } from "lucide-react";
import { toast } from "sonner";

export function ErrorsPanel() {
  const { data } = useQuery({
    queryKey: ["fleet-errors"],
    queryFn: () => get<{ groups?: ErrorGroup[] } | ErrorGroup[]>(
      "/api/fleet/errors",
    ),
    refetchInterval: 30_000,
  });
  const groups: ErrorGroup[] = Array.isArray(data)
    ? data
    : (data?.groups ?? []);

  const copy = (text: string, what: string) => {
    void navigator.clipboard.writeText(text).then(
      () => toast(`Copied ${what}`),
      () => toast.error("Copy failed"),
    );
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Fleet errors</CardTitle>
      </CardHeader>
      <CardContent>
        {groups.length === 0 ? (
          <p className="text-sm text-muted-foreground">No error groups.</p>
        ) : (
          <ul className="space-y-3">
            {groups.map((g) => (
              <li key={g.sig} className="rounded-md border p-3">
                <div className="flex items-center gap-2">
                  <Badge
                    variant={g.sev === "error" ? "destructive" : "secondary"}
                  >
                    {g.sev}
                  </Badge>
                  <span className="text-sm font-medium">×{g.count}</span>
                  <span className="truncate text-xs text-muted-foreground">
                    {g.hosts.join(", ")} · {g.containers.join(", ")}
                  </span>
                  <div className="ml-auto flex gap-1">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => copy(g.sig, "signature")}
                    >
                      <Copy className="h-3.5 w-3.5" /> sig
                    </Button>
                    {g.sample && (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => copy(g.sample!.text, "sample")}
                      >
                        <Copy className="h-3.5 w-3.5" /> text
                      </Button>
                    )}
                  </div>
                </div>
                {g.sample && (
                  <pre className="mt-2 max-h-24 overflow-auto rounded bg-muted p-2 font-mono text-xs whitespace-pre-wrap">
                    {g.sample.stamp ? `${g.sample.stamp} ` : ""}
                    {g.sample.text}
                  </pre>
                )}
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}
