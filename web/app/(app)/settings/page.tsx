"use client";

// Settings: API keys (reveal-once create, revoke), notification channels
// (kind-specific fields, event checkboxes, test), alert rules (metric,
// threshold, duration, container pattern, enable), admin password change.
// Notifications/alerts/password endpoints land in a later wave — every tab
// renders regardless and degrades gracefully on 404.

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { copyText } from "@/lib/clipboard";
import { ApiError, del, get, post, put } from "@/lib/api";
import {
  AlertRule,
  ApiKey,
  NOTIFY_EVENTS,
  NotificationChannel,
} from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Check, Copy, Plus, Trash2 } from "lucide-react";

export default function SettingsPage() {
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold">Settings</h1>
        <p className="text-sm text-muted-foreground">
          API access, notifications, alert rules and admin credentials.
        </p>
      </div>
      <Tabs defaultValue="keys">
        <TabsList>
          <TabsTrigger value="keys">API keys</TabsTrigger>
          <TabsTrigger value="notifications">Notifications</TabsTrigger>
          <TabsTrigger value="alerts">Alert rules</TabsTrigger>
          <TabsTrigger value="password">Password</TabsTrigger>
        </TabsList>
        <TabsContent value="keys">
          <ApiKeysTab />
        </TabsContent>
        <TabsContent value="notifications">
          <NotificationsTab />
        </TabsContent>
        <TabsContent value="alerts">
          <AlertsTab />
        </TabsContent>
        <TabsContent value="password">
          <PasswordTab />
        </TabsContent>
      </Tabs>
    </div>
  );
}

// ---------------------------------------------------------------- API keys

function ApiKeysTab() {
  const qc = useQueryClient();
  const keys = useQuery({ queryKey: ["keys"], queryFn: () => get<ApiKey[]>("/api/keys") });
  const [name, setName] = useState("");
  const [created, setCreated] = useState<ApiKey | null>(null);
  const [copied, setCopied] = useState(false);

  const create = async () => {
    if (!name.trim()) return;
    try {
      const k = await post<ApiKey>("/api/keys", { name: name.trim() });
      setCreated(k);
      setName("");
      qc.invalidateQueries({ queryKey: ["keys"] });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Key creation failed");
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex items-end gap-2">
        <div className="space-y-1.5">
          <Label htmlFor="key-name">Key name</Label>
          <Input
            id="key-name"
            className="w-64"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="ci-token"
            onKeyDown={(e) => {
              if (e.key === "Enter") create();
            }}
          />
        </div>
        <Button disabled={!name.trim()} onClick={create}>
          <Plus className="h-4 w-4" /> Create key
        </Button>
      </div>

      {created?.token && (
        <div className="flex flex-wrap items-center gap-2 rounded-md border border-emerald-600/40 bg-emerald-500/10 p-3">
          <span className="text-sm">
            New key — copy it now, it is shown only once:
          </span>
          <code className="rounded bg-muted px-2 py-0.5 font-mono text-xs">
            {created.token}
          </code>
          <Button
            variant="ghost"
            size="icon"
            onClick={() => {
              copyText(created.token ?? "").then((ok) => {
                if (!ok) toast.error("Copy failed — select the text manually");
                else {
                  setCopied(true);
                  setTimeout(() => setCopied(false), 1500);
                }
              });
            }}
          >
            {copied ? (
              <Check className="h-4 w-4 text-emerald-500" />
            ) : (
              <Copy className="h-4 w-4" />
            )}
          </Button>
        </div>
      )}

      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
            <TableHead>Created</TableHead>
            <TableHead>Last used</TableHead>
            <TableHead />
          </TableRow>
        </TableHeader>
        <TableBody>
          {(keys.data ?? []).map((k) => (
            <TableRow key={k.id}>
              <TableCell className="font-medium">{k.name}</TableCell>
              <TableCell className="text-xs text-muted-foreground">
                {new Date(k.createdAt).toLocaleString()}
              </TableCell>
              <TableCell className="text-xs text-muted-foreground">
                {k.lastUsedAt ? new Date(k.lastUsedAt).toLocaleString() : "never"}
              </TableCell>
              <TableCell>
                <Button
                  variant="ghost"
                  size="icon"
                  title="Revoke key"
                  onClick={() =>
                    del(`/api/keys/${k.id}`).then(() =>
                      qc.invalidateQueries({ queryKey: ["keys"] }),
                    )
                  }
                >
                  <Trash2 className="h-4 w-4 text-destructive" />
                </Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

// ---------------------------------------------------------- notifications

const CHANNEL_FIELDS: Record<string, { key: string; label: string; placeholder?: string }[]> = {
  discord: [{ key: "webhook_url", label: "Discord webhook URL" }],
  slack: [{ key: "webhook_url", label: "Slack webhook URL" }],
  telegram: [
    { key: "bot_token", label: "Bot token" },
    { key: "chat_id", label: "Chat ID" },
  ],
  webhook: [{ key: "url", label: "Webhook URL", placeholder: "http://…" }],
  email: [
    { key: "to", label: "To" },
    { key: "smtp_host", label: "SMTP host" },
    { key: "smtp_user", label: "SMTP user" },
    { key: "smtp_pass", label: "SMTP password" },
  ],
};

function NotificationsTab() {
  const qc = useQueryClient();
  const channels = useQuery({
    queryKey: ["notifications"],
    queryFn: () => get<NotificationChannel[]>("/api/notifications"),
    retry: false,
  });
  const [kind, setKind] = useState<NotificationChannel["kind"]>("webhook");
  const [config, setConfig] = useState<Record<string, string>>({});
  const [events, setEvents] = useState<Record<string, boolean>>({});
  const unsupported = channels.isError && channels.error instanceof ApiError;

  const create = async () => {
    const chosen = Object.entries(events)
      .filter(([, v]) => v)
      .map(([k]) => k);
    try {
      await post("/api/notifications", { kind, config, events: chosen, enabled: true });
      toast.success("Channel added");
      setConfig({});
      setEvents({});
      qc.invalidateQueries({ queryKey: ["notifications"] });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to add channel");
    }
  };

  const ready = Object.values(config).some((v) => v.trim() !== "");

  return (
    <div className="space-y-4">
      <div className="space-y-3 rounded-md border p-4">
        <div className="flex flex-wrap items-end gap-3">
          <div className="space-y-1.5">
            <Label>Kind</Label>
            <select
              className="h-9 rounded-md border bg-background px-3 text-sm"
              value={kind}
              onChange={(e) => {
                setKind(e.target.value as NotificationChannel["kind"]);
                setConfig({});
              }}
            >
              {Object.keys(CHANNEL_FIELDS).map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </select>
          </div>
          {CHANNEL_FIELDS[kind].map((f) => (
            <div key={f.key} className="space-y-1.5">
              <Label htmlFor={`ch-${f.key}`}>{f.label}</Label>
              <Input
                id={`ch-${f.key}`}
                className="w-64"
                type={/pass|token/.test(f.key) ? "password" : "text"}
                placeholder={f.placeholder}
                value={config[f.key] ?? ""}
                onChange={(e) => setConfig((c) => ({ ...c, [f.key]: e.target.value }))}
              />
            </div>
          ))}
          <Button disabled={!ready} onClick={create}>
            <Plus className="h-4 w-4" /> Add channel
          </Button>
        </div>
        <div className="flex flex-wrap gap-4">
          {NOTIFY_EVENTS.map((ev) => (
            <label key={ev} className="flex items-center gap-1.5 text-sm">
              <input
                type="checkbox"
                checked={events[ev] ?? false}
                onChange={(e) => setEvents((s) => ({ ...s, [ev]: e.target.checked }))}
              />
              {ev}
            </label>
          ))}
        </div>
      </div>

      {unsupported ? (
        <p className="text-xs text-muted-foreground">
          Notification endpoints are not available yet — the form is ready and will
          work once the backend lands.
        </p>
      ) : null}

      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Kind</TableHead>
            <TableHead>Events</TableHead>
            <TableHead>Enabled</TableHead>
            <TableHead />
          </TableRow>
        </TableHeader>
        <TableBody>
          {(channels.data ?? []).map((c) => (
            <TableRow key={c.id}>
              <TableCell>
                <Badge variant="outline">{c.kind}</Badge>
              </TableCell>
              <TableCell className="text-xs">{c.events?.join(", ")}</TableCell>
              <TableCell>
                <Switch checked={c.enabled} disabled />
              </TableCell>
              <TableCell>
                <div className="flex gap-1">
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() =>
                      post(`/api/notifications/${c.id}/test`)
                        .then(() => toast.success("Test sent"))
                        .catch((e: unknown) =>
                          toast.error(e instanceof Error ? e.message : "Test failed"),
                        )
                    }
                  >
                    Test
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={() =>
                      del(`/api/notifications/${c.id}`).then(() =>
                        qc.invalidateQueries({ queryKey: ["notifications"] }),
                      )
                    }
                  >
                    <Trash2 className="h-4 w-4 text-destructive" />
                  </Button>
                </div>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

// ---------------------------------------------------------------- alerts

const METRICS = [
  "load1",
  "mem_pct",
  "container_cpu_pct",
  "container_mem_pct",
  "container_down",
] as const;

function AlertsTab() {
  const qc = useQueryClient();
  const rules = useQuery({
    queryKey: ["alerts"],
    queryFn: () => get<AlertRule[]>("/api/alerts"),
    retry: false,
  });
  const [metric, setMetric] = useState<AlertRule["metric"]>("load1");
  const [threshold, setThreshold] = useState("1");
  const [durationSec, setDurationSec] = useState(300);
  const [containerPattern, setContainerPattern] = useState("");
  const [serverId, setServerId] = useState("");
  const servers = useQuery({
    queryKey: ["servers"],
    queryFn: () => get<{ id: string; name: string }[]>("/api/servers"),
  });
  const unsupported = rules.isError && rules.error instanceof ApiError;

  const create = async () => {
    try {
      await post("/api/alerts", {
        metric,
        threshold: Number(threshold),
        durationSec,
        containerPattern: containerPattern.trim() || null,
        serverId: serverId || null,
        enabled: true,
      });
      toast.success("Alert rule added");
      qc.invalidateQueries({ queryKey: ["alerts"] });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to add rule");
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end gap-3 rounded-md border p-4">
        <div className="space-y-1.5">
          <Label>Metric</Label>
          <select
            className="h-9 rounded-md border bg-background px-3 text-sm"
            value={metric}
            onChange={(e) => setMetric(e.target.value as AlertRule["metric"])}
          >
            {METRICS.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="al-th">Threshold</Label>
          <Input
            id="al-th"
            className="w-28 font-mono text-sm"
            value={threshold}
            onChange={(e) => setThreshold(e.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="al-dur">Duration (sec)</Label>
          <Input
            id="al-dur"
            type="number"
            className="w-28 font-mono text-sm"
            value={durationSec}
            onChange={(e) => setDurationSec(Number(e.target.value) || 0)}
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="al-cp">Container pattern (optional)</Label>
          <Input
            id="al-cp"
            className="w-48 font-mono text-sm"
            value={containerPattern}
            onChange={(e) => setContainerPattern(e.target.value)}
            placeholder="^nginx-"
          />
        </div>
        <div className="space-y-1.5">
          <Label>Server</Label>
          <select
            className="h-9 rounded-md border bg-background px-3 text-sm"
            value={serverId}
            onChange={(e) => setServerId(e.target.value)}
          >
            <option value="">all servers</option>
            {(servers.data ?? []).map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </select>
        </div>
        <Button onClick={create}>
          <Plus className="h-4 w-4" /> Add rule
        </Button>
      </div>

      {unsupported ? (
        <p className="text-xs text-muted-foreground">
          Alert endpoints are not available yet — the form is ready and will work
          once the backend lands.
        </p>
      ) : null}

      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Rule</TableHead>
            <TableHead>Server</TableHead>
            <TableHead>State</TableHead>
            <TableHead>Enabled</TableHead>
            <TableHead />
          </TableRow>
        </TableHeader>
        <TableBody>
          {(rules.data ?? []).map((r) => (
            <TableRow key={r.id}>
              <TableCell className="font-mono text-xs">
                {r.metric} &gt; {r.threshold} for {r.durationSec}s
                {r.containerPattern ? ` · ${r.containerPattern}` : ""}
              </TableCell>
              <TableCell className="text-xs">
                {r.serverId
                  ? (servers.data?.find((s) => s.id === r.serverId)?.name ??
                    r.serverId)
                  : "all"}
              </TableCell>
              <TableCell>
                <Badge variant={r.state === "firing" ? "destructive" : "secondary"}>
                  {r.state}
                </Badge>
              </TableCell>
              <TableCell>
                <Switch
                  checked={r.enabled}
                  onCheckedChange={(enabled) =>
                    put(`/api/alerts/${r.id}`, { ...r, enabled }).then(() =>
                      qc.invalidateQueries({ queryKey: ["alerts"] }),
                    )
                  }
                />
              </TableCell>
              <TableCell>
                <Button
                  variant="ghost"
                  size="icon"
                  onClick={() =>
                    del(`/api/alerts/${r.id}`).then(() =>
                      qc.invalidateQueries({ queryKey: ["alerts"] }),
                    )
                  }
                >
                  <Trash2 className="h-4 w-4 text-destructive" />
                </Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

// ---------------------------------------------------------------- password

function PasswordTab() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [repeat, setRepeat] = useState("");

  const change = async () => {
    if (next !== repeat) {
      toast.error("New passwords do not match");
      return;
    }
    try {
      await post("/api/auth/password", { current, next });
      toast.success("Password changed");
      setCurrent("");
      setNext("");
      setRepeat("");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Password change failed");
    }
  };

  return (
    <div className="max-w-sm space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="pw-cur">Current password</Label>
        <Input
          id="pw-cur"
          type="password"
          value={current}
          onChange={(e) => setCurrent(e.target.value)}
        />
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="pw-next">New password</Label>
        <Input
          id="pw-next"
          type="password"
          value={next}
          onChange={(e) => setNext(e.target.value)}
        />
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="pw-rep">Repeat new password</Label>
        <Input
          id="pw-rep"
          type="password"
          value={repeat}
          onChange={(e) => setRepeat(e.target.value)}
        />
      </div>
      <Button disabled={!current || !next} onClick={change}>
        Change password
      </Button>
    </div>
  );
}
