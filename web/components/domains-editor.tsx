"use client";

// Domain chips + container port for proxy routing.

import { useState } from "react";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { X } from "lucide-react";

export function DomainsEditor({
  domains,
  port,
  onDomains,
  onPort,
}: {
  domains: string[];
  port: number;
  onDomains: (next: string[]) => void;
  onPort: (next: number) => void;
}) {
  const [draft, setDraft] = useState("");

  const add = () => {
    const d = draft.trim().replace(/^https?:\/\//, "");
    if (!d || domains.includes(d)) {
      setDraft("");
      return;
    }
    onDomains([...domains, d]);
    setDraft("");
  };

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2">
        <Input
          className="flex-1 font-mono text-sm"
          value={draft}
          placeholder="app.example.com"
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              add();
            }
          }}
        />
        <Button type="button" variant="outline" size="sm" onClick={add}>
          Add domain
        </Button>
      </div>
      {domains.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {domains.map((d) => (
            <Badge key={d} variant="secondary" className="font-mono">
              {d}
              <button
                type="button"
                className="ml-1"
                onClick={() => onDomains(domains.filter((x) => x !== d))}
              >
                <X className="h-3 w-3" />
              </button>
            </Badge>
          ))}
        </div>
      )}
      <div className="flex items-center gap-2">
        <span className="text-sm text-muted-foreground">Container port</span>
        <Input
          className="w-28 font-mono text-sm"
          type="number"
          value={port || ""}
          placeholder="80"
          onChange={(e) => onPort(Number(e.target.value) || 0)}
        />
      </div>
    </div>
  );
}
