"use client";

import { useQueryClient } from "@tanstack/react-query";
import QRCode from "qrcode";
import { useState, type FormEvent } from "react";
import { Alert, Button, Card, Input, Label } from "@/components/ui";
import { api, ApiError, unwrap } from "@/lib/api/client";
import { meQueryKey, useMe } from "@/lib/auth";

export default function SettingsPage() {
  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-semibold tracking-tight">Instellingen</h1>
      <TotpCard />
      <PasswordCard />
    </div>
  );
}

function errorText(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.code === "invalid_totp") return "Deze code klopt niet of is al gebruikt.";
    if (err.code === "invalid_credentials") return "Het wachtwoord klopt niet.";
    return err.message;
  }
  return "Er ging iets mis.";
}

function TotpCard() {
  const me = useMe();
  const qc = useQueryClient();
  const [setup, setSetup] = useState<{ secret: string; qr: string } | null>(null);
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const enabled = me.data?.user.totp_enabled ?? false;

  async function begin() {
    setError(null);
    setDone(null);
    setBusy(true);
    try {
      const res = unwrap(await api.POST("/auth/totp/setup"));
      setSetup({ secret: res.secret, qr: await QRCode.toDataURL(res.otpauth_url, { margin: 1, width: 192 }) });
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  async function confirm(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      unwrap(await api.POST("/auth/totp/enable", { body: { code } }));
      setSetup(null);
      setCode("");
      setDone("Tweestapsverificatie staat aan. Vanaf nu vraagt de login ook een code.");
      await qc.invalidateQueries({ queryKey: meQueryKey });
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  async function disable(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      unwrap(await api.POST("/auth/totp/disable", { body: { password, code } }));
      setPassword("");
      setCode("");
      setDone("Tweestapsverificatie staat uit.");
      await qc.invalidateQueries({ queryKey: meQueryKey });
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Tweestapsverificatie (TOTP)">
      <div className="space-y-4">
        {error && <Alert>{error}</Alert>}
        {done && <Alert kind="success">{done}</Alert>}
        {!enabled && !setup && (
          <>
            <p className="text-sm text-slate-600 dark:text-slate-400">
              Gebruik een authenticator-app zoals Aegis, 2FAS of 1Password. ClusterForge kan al je servers
              aansturen; een tweede factor is sterk aangeraden.
            </p>
            <Button onClick={begin} disabled={busy}>
              Instellen
            </Button>
          </>
        )}
        {!enabled && setup && (
          <form onSubmit={confirm} className="space-y-4">
            <div className="flex flex-col gap-4 sm:flex-row sm:items-start">
              {/* eslint-disable-next-line @next/next/no-img-element -- data-URL, geen optimalisatie nodig */}
              <img src={setup.qr} alt="QR-code voor je authenticator-app" className="h-48 w-48 rounded bg-white p-1" />
              <div className="space-y-2 text-sm">
                <p>Scan de QR-code, of voer deze sleutel handmatig in:</p>
                <code className="block break-all rounded bg-slate-100 px-2 py-1 font-mono text-xs dark:bg-slate-800">
                  {setup.secret}
                </code>
              </div>
            </div>
            <div className="max-w-48">
              <Label htmlFor="totp-code">Code uit de app</Label>
              <Input
                id="totp-code"
                inputMode="numeric"
                autoComplete="one-time-code"
                pattern="[0-9]{6}"
                maxLength={6}
                required
                value={code}
                onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
                className="font-mono tracking-widest"
              />
            </div>
            <Button type="submit" disabled={busy}>
              Activeren
            </Button>
          </form>
        )}
        {enabled && (
          <form onSubmit={disable} className="space-y-4">
            <p className="text-sm text-slate-600 dark:text-slate-400">
              Tweestapsverificatie staat aan. Uitzetten vraagt je wachtwoord en een geldige code.
            </p>
            <div className="grid gap-4 sm:grid-cols-2">
              <div>
                <Label htmlFor="disable-password">Wachtwoord</Label>
                <Input
                  id="disable-password"
                  type="password"
                  autoComplete="current-password"
                  required
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </div>
              <div>
                <Label htmlFor="disable-code">Code uit de app</Label>
                <Input
                  id="disable-code"
                  inputMode="numeric"
                  pattern="[0-9]{6}"
                  maxLength={6}
                  required
                  value={code}
                  onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
                  className="font-mono tracking-widest"
                />
              </div>
            </div>
            <Button type="submit" variant="secondary" disabled={busy}>
              Uitzetten
            </Button>
          </form>
        )}
      </div>
    </Card>
  );
}

function PasswordCard() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [repeat, setRepeat] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setDone(false);
    if (next !== repeat) {
      setError("De nieuwe wachtwoorden komen niet overeen.");
      return;
    }
    setBusy(true);
    try {
      unwrap(await api.POST("/auth/password", { body: { current_password: current, new_password: next } }));
      setCurrent("");
      setNext("");
      setRepeat("");
      setDone(true);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Wachtwoord wijzigen">
      <form onSubmit={onSubmit} className="space-y-4">
        {error && <Alert>{error}</Alert>}
        {done && <Alert kind="success">Wachtwoord gewijzigd. Andere sessies zijn uitgelogd.</Alert>}
        <div className="grid gap-4 sm:grid-cols-3">
          <div>
            <Label htmlFor="current">Huidig wachtwoord</Label>
            <Input
              id="current"
              type="password"
              autoComplete="current-password"
              required
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
            />
          </div>
          <div>
            <Label htmlFor="new">Nieuw wachtwoord</Label>
            <Input
              id="new"
              type="password"
              autoComplete="new-password"
              minLength={12}
              required
              value={next}
              onChange={(e) => setNext(e.target.value)}
            />
          </div>
          <div>
            <Label htmlFor="repeat">Herhaal nieuw wachtwoord</Label>
            <Input
              id="repeat"
              type="password"
              autoComplete="new-password"
              minLength={12}
              required
              value={repeat}
              onChange={(e) => setRepeat(e.target.value)}
            />
          </div>
        </div>
        <p className="text-xs text-slate-500">Minstens 12 tekens.</p>
        <Button type="submit" disabled={busy}>
          Wijzigen
        </Button>
      </form>
    </Card>
  );
}
