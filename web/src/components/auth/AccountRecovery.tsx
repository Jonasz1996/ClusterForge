"use client";

import { useQueryClient } from "@tanstack/react-query";
import QRCode from "qrcode";
import { useEffect, useState, type FormEvent } from "react";
import { Alert, Button, Input, Label } from "@/components/ui";
import { api, ApiError, unwrap } from "@/lib/api/client";
import type { components } from "@/lib/api/schema";
import { meQueryKey } from "@/lib/auth";

type RecoveryHelp = components["schemas"]["RecoveryHelp"];

const box = "space-y-4 rounded-lg border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900";

// RecoveryForm is "Kan niet inloggen?": wie zijn gebruikersnaam invult,
// krijgt het commando voor de systeembeheerder. De tekst is altijd dezelfde,
// of het account nu bestaat of niet.
export function RecoveryForm({ initial, onBack }: { initial: string; onBack: () => void }) {
  const [username, setUsername] = useState(initial);
  const [help, setHelp] = useState<RecoveryHelp | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      setHelp(unwrap(await api.POST("/auth/recovery", { body: { username: username.trim() } })));
    } catch (err) {
      if (err instanceof ApiError && err.status === 429) setError("Te veel aanvragen. Probeer het over een minuut opnieuw.");
      else setError(err instanceof Error ? err.message : "Er ging iets mis.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={box}>
      <h2 className="text-base font-semibold">Kan niet inloggen?</h2>
      {error && <Alert>{error}</Alert>}
      {!help ? (
        <form onSubmit={onSubmit} className="space-y-4">
          <p className="text-sm text-slate-600 dark:text-slate-400">
            Wachtwoord vergeten of je authenticator-app kwijt? Vul de gebruikersnaam in waarmee je inlogt. Je krijgt het commando
            waarmee je systeembeheerder je account herstelt.
          </p>
          <div>
            <Label htmlFor="recover-username">Gebruikersnaam</Label>
            <Input
              id="recover-username"
              autoComplete="username"
              autoFocus
              required
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
          </div>
          <Button type="submit" disabled={busy} className="w-full">
            {busy ? "Bezig…" : "Toon het commando"}
          </Button>
        </form>
      ) : (
        <div className="space-y-4 text-sm">
          <p className="font-medium">Vraag je systeembeheerder dit uit te voeren:</p>
          {help.commands.map((c) => (
            <div key={c.command} className="space-y-1">
              <p className="text-xs text-slate-500 dark:text-slate-400">{c.label}</p>
              <CommandLine text={c.command} />
            </div>
          ))}
          <p className="text-slate-600 dark:text-slate-400">
            Je krijgt dan een tijdelijk wachtwoord waarmee je de toegang tot je account herstelt. Het werkt één keer en verloopt na{" "}
            {help.ttl_minutes} minuten. Daarna stel je meteen een nieuw wachtwoord en een nieuwe tweestapsverificatie in.
          </p>
          <p className="text-xs text-slate-500 dark:text-slate-400">
            Bestaat dit account niet, dan meldt het commando dat aan je beheerder.
          </p>
        </div>
      )}
      <Button type="button" variant="ghost" className="w-full" onClick={onBack}>
        Terug naar inloggen
      </Button>
    </div>
  );
}

function CommandLine({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex items-start gap-2 rounded-md bg-slate-900 p-2 text-slate-100">
      <code className="min-w-0 flex-1 break-words font-mono text-xs leading-5">{text}</code>
      <button
        type="button"
        className="shrink-0 rounded px-2 py-0.5 text-xs text-slate-300 hover:bg-slate-700"
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(text);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          } catch {
            // Geen klembord (http zonder TLS): selecteren en kopiëren kan nog.
          }
        }}
      >
        {copied ? "Gekopieerd" : "Kopieer"}
      </button>
    </div>
  );
}

function errorText(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.code === "invalid_totp") return "Deze code klopt niet of is al gebruikt. Wacht op de volgende code.";
    if (err.status === 429) return "Te veel pogingen. Probeer het over een minuut opnieuw.";
    return err.message;
  }
  return err instanceof Error ? err.message : "Er ging iets mis.";
}

// AccountSetup is de verplichte stap na een reset: eerst een nieuw
// wachtwoord, dan een nieuwe tweestapsverificatie. Pas daarna kan de
// gebruiker verder.
export function AccountSetup({
  mustChangePassword,
  onLogout,
}: {
  mustChangePassword: boolean;
  onLogout: () => void;
}) {
  return (
    <div className={box}>
      {mustChangePassword ? <NewPassword /> : <NewTotp />}
      <Button type="button" variant="ghost" className="w-full" onClick={onLogout}>
        Uitloggen
      </Button>
    </div>
  );
}

function NewPassword() {
  const qc = useQueryClient();
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    if (pw !== pw2) {
      setError("De wachtwoorden komen niet overeen.");
      return;
    }
    setBusy(true);
    try {
      unwrap(await api.POST("/auth/reset/password", { body: { new_password: pw } }));
      await qc.invalidateQueries({ queryKey: meQueryKey });
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <h2 className="text-base font-semibold">Stap 1 van 2: nieuw wachtwoord</h2>
      <p className="text-sm text-slate-600 dark:text-slate-400">
        Je account is hersteld met een tijdelijk wachtwoord. Kies nu je eigen wachtwoord van minstens 12 tekens.
      </p>
      {error && <Alert>{error}</Alert>}
      <div>
        <Label htmlFor="new-password">Nieuw wachtwoord</Label>
        <Input id="new-password" type="password" autoComplete="new-password" autoFocus required minLength={12} value={pw} onChange={(e) => setPw(e.target.value)} />
      </div>
      <div>
        <Label htmlFor="new-password-2">Herhaal het wachtwoord</Label>
        <Input id="new-password-2" type="password" autoComplete="new-password" required minLength={12} value={pw2} onChange={(e) => setPw2(e.target.value)} />
      </div>
      <Button type="submit" disabled={busy} className="w-full">
        {busy ? "Bezig…" : "Wachtwoord instellen"}
      </Button>
    </form>
  );
}

function NewTotp() {
  const qc = useQueryClient();
  const [setup, setSetup] = useState<{ secret: string; qr: string } | null>(null);
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let stop = false;
    (async () => {
      try {
        const res = unwrap(await api.POST("/auth/totp/setup"));
        const qr = await QRCode.toDataURL(res.otpauth_url, { margin: 1, width: 192 });
        if (!stop) setSetup({ secret: res.secret, qr });
      } catch (err) {
        if (!stop) setError(errorText(err));
      }
    })();
    return () => {
      stop = true;
    };
  }, []);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      unwrap(await api.POST("/auth/totp/enable", { body: { code } }));
      await qc.invalidateQueries({ queryKey: meQueryKey });
    } catch (err) {
      setError(errorText(err));
      setCode("");
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <h2 className="text-base font-semibold">Stap 2 van 2: tweestapsverificatie</h2>
      <p className="text-sm text-slate-600 dark:text-slate-400">
        Je oude tweestapsverificatie is uitgeschakeld. Verwijder het oude ClusterForge-item uit je authenticator-app en scan deze
        nieuwe code.
      </p>
      {error && <Alert>{error}</Alert>}
      {setup ? (
        <>
          {/* eslint-disable-next-line @next/next/no-img-element -- data-URL, geen optimalisatie nodig */}
          <img src={setup.qr} alt="QR-code voor je authenticator-app" className="mx-auto h-48 w-48 rounded bg-white p-1" />
          <p className="text-xs text-slate-500 dark:text-slate-400">
            Of voer deze sleutel handmatig in: <code className="break-all font-mono">{setup.secret}</code>
          </p>
          <div>
            <Label htmlFor="setup-code">Code uit de app</Label>
            <Input
              id="setup-code"
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
          <Button type="submit" disabled={busy} className="w-full">
            {busy ? "Bezig…" : "Bevestigen en verder"}
          </Button>
        </>
      ) : (
        !error && <p className="text-sm text-slate-500">Bezig…</p>
      )}
    </form>
  );
}
