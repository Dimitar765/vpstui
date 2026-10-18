"use client";

// Add-server dialog: mirrors the TUI host form fields, including the
// advanced ones (base_dir, proxy ports, monitor interval). Deliberately
// plain controlled state — no form library.

import { useState } from "react";
import { post } from "@/lib/api";
import type { ServerRow } from "@/lib/types";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { toast } from "sonner";
import { Plus } from "lucide-react";

interface FormState {
  name: string;
  address: string;
  port: string;
  user: string;
  authType: "key" | "password" | "agent";
  keyPath: string;
  password: string;
  baseDir: string;
  proxyHttpPort: string;
  proxyHttpsPort: string;
  monitorIntervalSec: string;
}

const EMPTY: FormState = {
  name: "",
  address: "",
  port: "22",
  user: "root",
  authType: "key",
  keyPath: "",
  password: "",
  baseDir: "",
  proxyHttpPort: "",
  proxyHttpsPort: "",
  monitorIntervalSec: "",
};

const num = (v: string): number | undefined => {
  if (v.trim() === "") return undefined;
  const n = Number(v);
  return Number.isFinite(n) ? n : undefined;
};

export function AddServerDialog({ onCreated }: { onCreated: (s: ServerRow) => void }) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [form, setForm] = useState<FormState>(EMPTY);
  const set = (k: keyof FormState, v: string) =>
    setForm((f) => ({ ...f, [k]: v }));

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (busy) return;
    if (!form.name.trim() || !form.address.trim() || !form.user.trim()) {
      toast.error("Name, address and user are required");
      return;
    }
    setBusy(true);
    try {
      const created = await post<ServerRow>("/api/servers", {
        name: form.name.trim(),
        address: form.address.trim(),
        port: num(form.port) ?? 22,
        user: form.user.trim(),
        authType: form.authType,
        keyPath: form.keyPath.trim(),
        password: form.password,
        baseDir: form.baseDir.trim(),
        proxyHttpPort: num(form.proxyHttpPort),
        proxyHttpsPort: num(form.proxyHttpsPort),
        monitorIntervalSec: num(form.monitorIntervalSec),
      });
      toast.success(`Server ${created.name} added`);
      setOpen(false);
      setForm(EMPTY);
      onCreated(created);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "add failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm">
          <Plus className="h-4 w-4" /> Add server
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Add server</DialogTitle>
          <DialogDescription>
            Register a VPS for agentless SSH management.
          </DialogDescription>
        </DialogHeader>
        <form className="space-y-4" onSubmit={submit}>
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="sv-name">Name</Label>
              <Input
                id="sv-name"
                value={form.name}
                onChange={(e) => set("name", e.target.value)}
                required
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="sv-user">User</Label>
              <Input
                id="sv-user"
                value={form.user}
                onChange={(e) => set("user", e.target.value)}
                required
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="sv-address">Address</Label>
              <Input
                id="sv-address"
                placeholder="203.0.113.7"
                value={form.address}
                onChange={(e) => set("address", e.target.value)}
                required
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="sv-port">SSH port</Label>
              <Input
                id="sv-port"
                type="number"
                value={form.port}
                onChange={(e) => set("port", e.target.value)}
              />
            </div>
          </div>
          <div className="space-y-1.5">
            <Label>Auth type</Label>
            <Select
              value={form.authType}
              onValueChange={(v) => set("authType", v)}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="key">SSH key</SelectItem>
                <SelectItem value="password">Password</SelectItem>
                <SelectItem value="agent">SSH agent</SelectItem>
              </SelectContent>
            </Select>
          </div>
          {form.authType === "key" && (
            <div className="space-y-1.5">
              <Label htmlFor="sv-keypath">Key path</Label>
              <Input
                id="sv-keypath"
                placeholder="~/.ssh/id_ed25519"
                value={form.keyPath}
                onChange={(e) => set("keyPath", e.target.value)}
              />
            </div>
          )}
          {form.authType === "password" && (
            <div className="space-y-1.5">
              <Label htmlFor="sv-password">Password</Label>
              <Input
                id="sv-password"
                type="password"
                value={form.password}
                onChange={(e) => set("password", e.target.value)}
              />
            </div>
          )}

          <Separator />
          <p className="text-xs font-medium text-muted-foreground">Advanced</p>
          <div className="space-y-1.5">
            <Label htmlFor="sv-basedir">Base dir (remote deploy root)</Label>
            <Input
              id="sv-basedir"
              placeholder="/opt/vpstui"
              value={form.baseDir}
              onChange={(e) => set("baseDir", e.target.value)}
            />
          </div>
          <div className="grid grid-cols-3 gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="sv-ph">Proxy HTTP port</Label>
              <Input
                id="sv-ph"
                type="number"
                placeholder="80"
                value={form.proxyHttpPort}
                onChange={(e) => set("proxyHttpPort", e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="sv-phs">Proxy HTTPS port</Label>
              <Input
                id="sv-phs"
                type="number"
                placeholder="443"
                value={form.proxyHttpsPort}
                onChange={(e) => set("proxyHttpsPort", e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="sv-mi">Monitor interval (s)</Label>
              <Input
                id="sv-mi"
                type="number"
                placeholder="15"
                value={form.monitorIntervalSec}
                onChange={(e) => set("monitorIntervalSec", e.target.value)}
              />
            </div>
          </div>
          <DialogFooter>
            <Button type="submit" disabled={busy}>
              {busy ? "Adding…" : "Add server"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
