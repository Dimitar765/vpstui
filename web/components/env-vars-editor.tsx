"use client";

// Key/value rows editor for app env vars. Values whose key looks like a
// secret are masked with a per-row reveal toggle.

import { useState } from "react";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Eye, EyeOff, Plus, Trash2 } from "lucide-react";

const SECRET_RE =
  /secret|password|passwd|token|key|credential|private|auth|dsn|uri|pwd/i;

export function looksSecret(key: string): boolean {
  return SECRET_RE.test(key);
}

export function EnvVarsEditor({
  value,
  onChange,
}: {
  value: Record<string, string>;
  onChange: (next: Record<string, string>) => void;
}) {
  const [revealed, setRevealed] = useState<Record<string, boolean>>({});
  const entries = Object.entries(value);

  const set = (oldKey: string, key: string, v: string) => {
    const next: Record<string, string> = {};
    for (const [k, val] of entries) {
      next[k === oldKey ? key : k] = k === oldKey ? v : val;
    }
    if (!(oldKey in value)) next[key] = v;
    onChange(next);
  };

  const remove = (key: string) => {
    const next = { ...value };
    delete next[key];
    setRevealed(({ [key]: _drop, ...rest }) => rest);
    onChange(next);
  };

  return (
    <div className="space-y-2">
      {entries.map(([k, v]) => {
        const secret = looksSecret(k);
        const shown = !secret || revealed[k];
        return (
          <div key={k} className="flex items-center gap-2">
            <Input
              className="w-48 font-mono text-sm"
              value={k}
              onChange={(e) => set(k, e.target.value, v)}
              placeholder="KEY"
            />
            <Input
              className="flex-1 font-mono text-sm"
              type={shown ? "text" : "password"}
              value={v}
              onChange={(e) => set(k, k, e.target.value)}
              placeholder="value"
            />
            {secret && (
              <Button
                type="button"
                variant="ghost"
                size="icon"
                onClick={() => setRevealed((r) => ({ ...r, [k]: !r[k] }))}
                title={shown ? "Hide value" : "Reveal value"}
              >
                {shown ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
              </Button>
            )}
            <Button
              type="button"
              variant="ghost"
              size="icon"
              onClick={() => remove(k)}
              title="Remove variable"
            >
              <Trash2 className="h-4 w-4 text-destructive" />
            </Button>
          </div>
        );
      })}
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => {
          let key = "VAR";
          let i = 1;
          while (key in value) key = `VAR${++i}`;
          set("", key, "");
        }}
      >
        <Plus className="h-4 w-4" /> Add variable
      </Button>
    </div>
  );
}
