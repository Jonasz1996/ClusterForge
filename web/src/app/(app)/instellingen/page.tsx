"use client";

import { useQueryClient } from "@tanstack/react-query";
import QRCode from "qrcode";
import { useState, type FormEvent } from "react";
import { AssistantSettingsCard } from "@/components/assistant/AssistantSettings";
import { AgentUpgradeSettingsCard } from "@/components/inventory/AgentUpgrades";
import { ApiTokensCard } from "@/components/settings/ApiTokensCard";
import { SessionsCard } from "@/components/settings/SessionsCard";
import { DocsTargetCard } from "@/components/docs/DocsTargetCard";
import { SelfLocationCard } from "@/components/risk/SelfLocationCard";
import { Alert, Button, Card, Input, Label, PageHeader } from "@/components/ui";
import { api, ApiError, unwrap } from "@/lib/api/client";
import { meQueryKey, useMe } from "@/lib/auth";
import { useIsAdmin } from "@/lib/inventory";
import { useTheme, type Theme } from "@/lib/theme";

const sections: { id: string; label: string }[] = [
  { id: "tweestap", label: "Tweestapsverificatie" },
  { id: "wachtwoord", label: "Wachtwoord" },
  { id: "sessies", label: "Sessies" },
  { id: "weergave", label: "Weergave" },
  { id: "assistent", label: "Assistent" },
  { id: "documentatie-github", label: "Documentatie naar GitHub" },
  { id: "locatie", label: "Locatie van ClusterForge" },
  { id: "agents-bijwerken", label: "Agents bijwerken" },
  { id: "dashboard-api", label: "Dashboard-API" },
];

export default function SettingsPage() {
  const isAdmin = useIsAdmin();
  return (
    <div className="space-y-6">
      <PageHeader title="Instellingen" description="Je eigen account bovenaan, daaronder de instellingen van ClusterForge zelf." />
      <nav aria-label="Onderdelen" className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
        {sections.map((s) => (
          <a key={s.id} href={`#${s.id}`} className="text-brand-700 hover:underline dark:text-sky-300">
            {s.label}
          </a>
        ))}
      </nav>
      <div id="tweestap" className="scroll-mt-20">
        <TotpCard />
      </div>
      <div id="wachtwoord" className="scroll-mt-20">
        <PasswordCard />
      </div>
      <div id="sessies" className="scroll-mt-20">
        <SessionsCard />
      </div>
      <div id="weergave" className="scroll-mt-20">
        <AppearanceCard />
      </div>
      <div id="assistent" className="scroll-mt-20">
        <AssistantSettingsCard isAdmin={isAdmin} />
      </div>
      <div id="documentatie-github" className="scroll-mt-20">
        <DocsTargetCard isAdmin={isAdmin} />
      </div>
      <div id="locatie" className="scroll-mt-20">
        <SelfLocationCard isAdmin={isAdmin} />
      </div>
      <div id="agents-bijwerken" className="scroll-mt-20">
        <AgentUpgradeSettingsCard isAdmin={isAdmin} />
      </div>
      <div id="dashboard-api" className="scroll-mt-20">
        <ApiTokensCard isAdmin={isAdmin} />
      </div>
    </div>
  );
}

// AppearanceCard kiest het thema; de keuze geldt voor deze browser.
function AppearanceCard() {
  const [theme, setTheme] = useTheme();
  const opts: { v: Theme; label: string }[] = [
    { v: "system", label: "Zoals het systeem" },
    { v: "light", label: "Licht" },
    { v: "dark", label: "Donker" },
  ];
  return (
    <Card title="Weergave">
      <fieldset>
        <legend className="mb-2 text-sm text-slate-600 dark:text-slate-400">
          Thema van de interface, voor deze browser. Ook te wisselen met de knop rechtsboven. Zoeken kan overal met{" "}
          <kbd className="rounded border border-slate-300 px-1 text-xs dark:border-slate-700">Ctrl K</kbd> of{" "}
          <kbd className="rounded border border-slate-300 px-1 text-xs dark:border-slate-700">/</kbd>.
        </legend>
        <div className="flex flex-wrap gap-2">
          {opts.map((o) => (
            <label
              key={o.v}
              className={`cursor-pointer rounded-md border px-3 py-2 text-sm ${
                theme === o.v
                  ? "border-brand-500 bg-brand-50 font-medium text-brand-700 dark:bg-slate-800 dark:text-white"
                  : "border-slate-300 dark:border-slate-700"
              }`}
            >
              <input type="radio" name="theme" value={o.v} checked={theme === o.v} onChange={() => setTheme(o.v)} className="sr-only" />
              {o.label}
            </label>
          ))}
        </div>
      </fieldset>
    </Card>
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
              Gebruik een authenticator-app zoals Aegis, 2FAS of 1Password. ClusterForge kan al je servers aansturen; een tweede
              factor is sterk aangeraden.
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
