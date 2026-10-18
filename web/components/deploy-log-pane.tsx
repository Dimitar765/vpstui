"use client";

// Live deployment log pane. Autoscrolls while pinned to the bottom; scrolling
// up pauses and shows a `↓ N new` resume badge (same UX contract as the TUI
// log view).

import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { useDeploymentStream } from "@/lib/ws";

export function DeployLogPane({ deploymentId }: { deploymentId: string }) {
  const stream = useDeploymentStream(deploymentId);
  const boxRef = useRef<HTMLDivElement>(null);
  const pinnedRef = useRef(true);
  const [newCount, setNewCount] = useState(0);
  const lines = stream.lines;

  useEffect(() => {
    if (pinnedRef.current && boxRef.current) {
      boxRef.current.scrollTop = boxRef.current.scrollHeight;
    } else {
      setNewCount((n) => n + 1);
    }
  }, [lines.length]);

  const onScroll = () => {
    const el = boxRef.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 32;
    pinnedRef.current = atBottom;
    if (atBottom) setNewCount(0);
  };

  const resume = () => {
    pinnedRef.current = true;
    setNewCount(0);
    const el = boxRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  };

  return (
    <div className="relative">
      <div
        ref={boxRef}
        onScroll={onScroll}
        className="h-96 overflow-y-auto rounded-md bg-zinc-950 p-3 font-mono text-xs leading-relaxed text-zinc-200"
      >
        {lines.length === 0 ? (
          <p className="text-zinc-500">
            {stream.done ? "no log output recorded" : "waiting for log output…"}
          </p>
        ) : (
          lines.map((l, i) => (
            <div key={i} className="whitespace-pre-wrap break-all">
              {l}
            </div>
          ))
        )}
      </div>
      {newCount > 0 && (
        <Button
          size="sm"
          className="absolute bottom-3 right-3"
          onClick={resume}
          title="Scroll to bottom"
        >
          ↓ {newCount} new
        </Button>
      )}
      {stream.error && (
        <Badge variant="destructive" className="absolute top-3 right-3">
          {stream.error}
        </Badge>
      )}
    </div>
  );
}
