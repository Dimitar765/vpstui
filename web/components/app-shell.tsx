"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { usePathname, useRouter } from "next/navigation";
import { get, ApiError } from "@/lib/api";
import { logout } from "@/lib/ws";
import type { Me } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import {
  LayoutDashboard,
  Server,
  FolderKanban,
  Database,
  Blocks,
  Settings,
  LogOut,
} from "lucide-react";

const NAV = [
  { href: "/dashboard", label: "Dashboard", icon: LayoutDashboard, enabled: true },
  { href: "/servers", label: "Servers", icon: Server, enabled: true },
  { href: "/projects", label: "Projects", icon: FolderKanban, enabled: true },
  { href: "/databases", label: "Databases", icon: Database, enabled: true },
  { href: "/templates", label: "Templates", icon: Blocks, enabled: true },
  { href: "/settings", label: "Settings", icon: Settings, enabled: true },
];

export function AppShell({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const pathname = usePathname();
  const [me, setMe] = useState<Me | null>(null);
  const [checked, setChecked] = useState(false);

  useEffect(() => {
    get<Me>("/api/me")
      .then((m) => {
        setMe(m);
        setChecked(true);
      })
      .catch((err: unknown) => {
        // 401 already bounced to /login via the api wrapper; anything else
        // (daemon briefly down) still gatekeeps the shell.
        if (!(err instanceof ApiError) || err.status !== 401) {
          setChecked(true);
        }
      });
  }, []);

  if (!checked) {
    return (
      <div className="flex min-h-screen items-center justify-center text-sm text-muted-foreground">
        Checking session…
      </div>
    );
  }

  return (
    <div className="flex min-h-screen">
      <aside className="flex w-56 shrink-0 flex-col border-r bg-sidebar">
        <div className="px-5 py-5 text-lg font-semibold tracking-tight">
          vpstui
        </div>
        <nav className="flex flex-1 flex-col gap-1 px-3">
          {NAV.map(({ href, label, icon: Icon, enabled }) => {
            const active = pathname === href || pathname.startsWith(href + "/");
            if (enabled) {
              return (
                <Link
                  key={href}
                  href={href}
                  className={`flex items-center gap-2.5 rounded-md px-3 py-2 text-sm ${
                    active
                      ? "bg-sidebar-accent font-medium text-sidebar-accent-foreground"
                      : "text-muted-foreground hover:bg-sidebar-accent/50 hover:text-foreground"
                  }`}
                >
                  <Icon className="h-4 w-4" />
                  {label}
                </Link>
              );
            }
            return (
              <span
                key={href}
                title="Coming in a later phase"
                className="flex cursor-not-allowed items-center gap-2.5 rounded-md px-3 py-2 text-sm text-muted-foreground/50"
              >
                <Icon className="h-4 w-4" />
                {label}
              </span>
            );
          })}
        </nav>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 items-center justify-between border-b px-6">
          <div className="text-sm text-muted-foreground">
            {NAV.find((n) => pathname.startsWith(n.href))?.label ?? ""}
          </div>
          <div className="flex items-center gap-3">
            {me && <span className="text-sm text-muted-foreground">{me.email}</span>}
            <Separator orientation="vertical" className="h-5" />
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                void logout().then(() => router.replace("/login"));
              }}
            >
              <LogOut className="h-4 w-4" /> Log out
            </Button>
          </div>
        </header>
        <main className="flex-1 overflow-y-auto p-6">{children}</main>
      </div>
    </div>
  );
}
