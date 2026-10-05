"use client";

import { useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { useEffect, useState, type FormEvent } from "react";
import { Alert, Button, Input, Label } from "@/components/ui";
import { api, ApiError, setCsrfToken, unwrap } from "@/lib/api/client";
import { meQueryKey, useMe } from "@/lib/auth";

export default function LoginPage() {
  const router = useRouter();
  const qc = useQueryClient();
  const me = useMe();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [needsCode, setNeedsCode] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (me.data) router.replace("/");
  }, [me.data, router]);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const res = unwrap(
        await api.POST("/auth/login", {
          body: { username, password, ...(needsCode ? { totp_code: code } : {}) },
        }),
      );
      setCsrfToken(res.csrf_token);
      qc.setQueryData(meQueryKey, res);
      router.replace("/");
    } catch (err) {
      if (err instanceof ApiError && err.code === "totp_required") {
        setNeedsCode(true);
      } else if (err instanceof ApiError && err.code === "invalid_totp") {
        setError("Deze code klopt niet of is al gebruikt. Wacht op de volgende code.");
        setCode("");
      } else if (err instanceof ApiError && err.code === "invalid_credentials") {
        setError("Onjuiste gebruikersnaam of wachtwoord.");
      } else if (err instanceof ApiError && err.status === 429) {
        setError("Te veel pogingen. Probeer het over een minuut opnieuw.");
      } else {
        setError(err instanceof Error ? err.message : "Inloggen mislukt.");
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="flex min-h-screen items-center justify-center px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 text-center">
          <Logo />
          <h1 className="mt-3 text-xl font-semibold">Inloggen bij ClusterForge</h1>
        </div>
        <form
          onSubmit={onSubmit}
          className="space-y-4 rounded-lg border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900"
        >
          {error && <Alert>{error}</Alert>}
          {!needsCode ? (
            <>
              <div>
                <Label htmlFor="username">Gebruikersnaam</Label>
                <Input
                  id="username"
                  autoComplete="username"
                  autoFocus
                  required
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                />
              </div>
              <div>
                <Label htmlFor="password">Wachtwoord</Label>
                <Input
                  id="password"
                  type="password"
                  autoComplete="current-password"
                  required
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </div>
            </>
          ) : (
            <div>
              <Label htmlFor="code">Code uit je authenticator-app</Label>
              <Input
                id="code"
                inputMode="numeric"
                autoComplete="one-time-code"
                pattern="[0-9]{6}"
                maxLength={6}
                autoFocus
                required
                value={code}
                onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
                className="text-center font-mono text-lg tracking-[0.4em]"
              />
            </div>
          )}
          <Button type="submit" disabled={busy} className="w-full">
            {busy ? "Bezig…" : needsCode ? "Bevestigen" : "Inloggen"}
          </Button>
          {needsCode && (
            <Button
              type="button"
              variant="ghost"
              className="w-full"
              onClick={() => {
                setNeedsCode(false);
                setCode("");
                setError(null);
              }}
            >
              Terug
            </Button>
          )}
        </form>
      </div>
    </main>
  );
}

function Logo() {
  return (
    <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-xl bg-brand-600 text-white">
      <svg viewBox="0 0 24 24" className="h-7 w-7" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden>
        <rect x="3" y="4" width="18" height="5" rx="1.5" />
        <rect x="3" y="15" width="18" height="5" rx="1.5" />
        <path d="M7 9v6M17 9v6" />
      </svg>
    </div>
  );
}
