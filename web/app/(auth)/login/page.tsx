"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { post, setToken, getToken } from "@/lib/api";
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

export default function LoginPage() {
  const router = useRouter();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (getToken()) router.replace("/dashboard");
  }, [router]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const res = await post<{ token: string }>("/api/auth/login", {
        email: email.trim(),
        password,
      });
      setToken(res.token);
      router.replace("/dashboard");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "login failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex min-h-screen items-center justify-center p-6">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>Log in</CardTitle>
          <CardDescription>vpstui control plane</CardDescription>
        </CardHeader>
        <form onSubmit={(e) => void submit(e)}>
          <CardContent className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="li-email">Email</Label>
              <Input
                id="li-email"
                type="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                required
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="li-password">Password</Label>
              <Input
                id="li-password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </div>
          </CardContent>
          <CardFooter className="flex items-center justify-between">
            <Link
              href="/setup"
              className="text-sm text-muted-foreground hover:underline"
            >
              First run? Set up
            </Link>
            <Button type="submit" disabled={busy}>
              {busy ? "Logging in…" : "Log in"}
            </Button>
          </CardFooter>
        </form>
      </Card>
    </div>
  );
}
