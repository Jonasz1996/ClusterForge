"use client";

import { useState, type FormEvent } from "react";
import { Alert, Button, Field, Input } from "@/components/ui";
import { useProbeProxmox, type ProxmoxConnection, type ProxmoxInput } from "@/lib/proxmox";

// ConnectionForm koppelt Proxmox of wijzigt een koppeling. Bij wijzigen blijft
// het token-secret ongewijzigd als het veld leeg is.
export function ConnectionForm({
  initial,
  submitLabel,
  onSubmit,
  onCancel,
}: {
  initial?: ProxmoxConnection;
  submitLabel: string;
  onSubmit: (v: ProxmoxInput) => Promise<void>;
  onCancel: () => void;
}) {
  const [name, setName] = useState(initial?.name ?? "");
  const [apiUrl, setApiUrl] = useState(initial?.api_url ?? "");
  const [tokenId, setTokenId] = useState(initial?.token_id ?? "clusterforge@pve!cf");
  const [secret, setSecret] = useState("");
  const [fingerprint, setFingerprint] = useState(initial?.tls_fingerprint ?? "");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const probe = useProbeProxmox();

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setPending(true);
    try {
      await onSubmit({ name, api_url: apiUrl, token_id: tokenId, token_secret: secret, tls_fingerprint: fingerprint });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Opslaan mislukt");
    } finally {
      setPending(false);
    }
  };

  return (
    <form onSubmit={submit} className="space-y-4">
      {error && <Alert>{error}</Alert>}
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Naam" htmlFor="pve-name" hint="Hoe je deze omgeving noemt, bijvoorbeeld Thuislab.">
          <Input id="pve-name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} />
        </Field>
        <Field label="API-adres" htmlFor="pve-url" hint="Een host van je cluster, bijvoorbeeld pve1.lan of https://10.0.0.5:8006.">
          <Input id="pve-url" value={apiUrl} onChange={(e) => setApiUrl(e.target.value)} required placeholder="pve1.lan" />
        </Field>
        <Field label="Token-id" htmlFor="pve-token" hint="gebruiker@realm!tokennaam">
          <Input id="pve-token" value={tokenId} onChange={(e) => setTokenId(e.target.value)} required className="font-mono" />
        </Field>
        <Field
          label="Token-secret"
          htmlFor="pve-secret"
          hint={initial ? "Leeg laten om het huidige te houden." : "Het secret dat Proxmox één keer toont."}
        >
          <Input
            id="pve-secret"
            type="password"
            autoComplete="off"
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            required={!initial}
            className="font-mono"
          />
        </Field>
      </div>
      <Field
        label="Vingerafdruk van het certificaat"
        htmlFor="pve-fp"
        hint="SHA-256, zoals Proxmox hem toont bij Host → System → Certificates. Leeg als het certificaat door een bekende CA is uitgegeven."
      >
        <div className="flex gap-2">
          <Input
            id="pve-fp"
            value={fingerprint}
            onChange={(e) => setFingerprint(e.target.value)}
            className="font-mono text-xs"
            placeholder="AB:CD:…"
          />
          <Button
            type="button"
            variant="secondary"
            className="shrink-0"
            disabled={apiUrl.trim() === "" || probe.isPending}
            onClick={() => probe.mutate(apiUrl)}
          >
            Ophalen
          </Button>
        </div>
      </Field>
      {probe.isError && <Alert>{probe.error.message}</Alert>}
      {probe.data && (
        <div className="space-y-2 rounded-md border border-slate-200 p-3 text-sm dark:border-slate-700">
          <p>
            {probe.data.api_url} stuurt dit certificaat
            {probe.data.trusted ? ", uitgegeven door een vertrouwde CA" : ""}:
          </p>
          <p className="font-mono text-xs break-all">{probe.data.fingerprint}</p>
          <p className="text-xs text-slate-500">
            {probe.data.subject} · uitgegeven door {probe.data.issuer} · geldig tot{" "}
            {new Date(probe.data.not_after).toLocaleDateString("nl-BE")}
          </p>
          <p className="text-xs text-slate-500">
            Vergelijk hem met de vingerafdruk in Proxmox voor je hem gebruikt; zo weet je zeker dat je met je eigen host praat.
          </p>
          <Button type="button" variant="secondary" onClick={() => setFingerprint(probe.data!.fingerprint)}>
            Deze vingerafdruk gebruiken
          </Button>
        </div>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" onClick={onCancel}>
          Annuleren
        </Button>
        <Button type="submit" disabled={pending}>
          {pending ? "Verbinding testen…" : submitLabel}
        </Button>
      </div>
    </form>
  );
}
