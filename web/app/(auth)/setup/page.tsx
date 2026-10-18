"use client";

// First-boot setup. Detection: GET /api/me — 200 means already configured
// (→ dashboard), 409 means configured-but-bad-token handled by the api layer;
// 401 with no admin anywhere shows the form. A 409 from POST /api/setup means
// the admin appeared mid-flight → login.

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { post, setToken, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { toast } from "sonner";

export default function SetupPage() {
  const router = useRouter();
  const [checking, setChecking] = useState(true);
  const [busy, setBusy] = useState(false);
  const [token, setToken] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  useEffect(() => {
    // api() would bounce a 401 to /login, so probe with raw fetch here: an
    // unauthenticated /api/me is expected while the admin is missing.
    fetch("/api/me", { cache: "no-store" })
      .then((res) => {
        if (res.ok) router.replace("/dashboard");
        else setChecking(false);
      })
      .catch(() => setChecking(false));
  }, [router]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      await post("/api/setup", { token: token.trim(), email: email.trim(), password });
      const login = await post<{ token: string }>("/api/auth/login", {
        email: email.trim(),
        password,
      });
      setToken(login.token);
      router.replace("/dashboard");
    } catch (err) {
      const status = err instanceof ApiError ? err.status : 0;
      if (status === 409) {
        toast.info("Admin already exists — log in instead.");
        router.replace("/login");
      } else {
        toast.error(err instanceof Error ? err.message : "setup failed");
      }
    } finally {
      setBusy(false);
    }
  };

  if (checking) {
    return (
      <div className="flex min-h-screen items-center justify-center text-sm text-muted-foreground">
        Checking setup state…
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center p-6">
      <Card className="w-full max-w-md">
        <CardHeader>
          <CardTitle>Welcome to vpstui</CardTitle>
          <CardDescription>
            Create the admin account. Paste the setup token the daemon printed
            on first start.
          </CardDescription>
        </CardHeader>
        <form onSubmit={(e) => void submit(e)}>
          <CardContent className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="su-token">Setup token</Label>
              <Input
                id="su-token"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder="from vpstui-server stdout"
                required
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="su-email">Email</Label>
              <Input
                id="su-email"
                type="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                required
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="su-password">Password</Label>
              <Input
                id="su-password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                minLength={8}
                required
              />
              <p className="text-xs text-muted-foreground">At least 8 characters.</p>
            </div>
          </CardContent>
          <CardFooter className="flex items-center justify-between">
            <Link href="/login" className="text-sm text-muted-foreground hover:underline">
              Already set up? Log in
            </Link>
            <Button type="submit" disabled={busy}>
              {busy ? "Creating…" : "Create admin"}
            </Button>
          </CardFooter>
        </form>
      </Card>
    </div>
  );
}
