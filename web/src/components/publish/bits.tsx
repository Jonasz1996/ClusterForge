"use client";

import Link from "next/link";
import type { ReactNode } from "react";
import { Alert, Badge, Button } from "@/components/ui";
import { akLabel, akTone, useRefreshPublish, type AKState, type PublishOverview, type PublishReadState } from "@/lib/publish";

export function ago(iso?: string) {
  if (!iso) return "nog nooit";
  const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
  if (s < 60) return `${s} s geleden`;
  if (s < 3600) return `${Math.round(s / 60)} min geleden`;
  if (s < 86400) return `${Math.round(s / 3600)} u geleden`;
  return `${Math.round(s / 86400)} d geleden`;
}

export function AKBadge({ state }: { state: AKState }) {
  if (!state) return null;
  return <Badge tone={akTone(state)}>{akLabel[state]}</Badge>;
}

function LinkState({ name, r }: { name: string; r: PublishReadState }) {
  if (!r.linked) {
    return (
      <span className="flex items-center gap-2">
        <Badge tone="slate">{name} niet gekoppeld</Badge>
      </span>
    );
  }
  return (
    <span className="flex flex-wrap items-center gap-2">
      <Badge tone={r.error ? "red" : "green"}>
        {name}
        {r.version ? ` ${r.version}` : ""}
      </Badge>
      <span className="text-xs text-slate-500">
        {r.hostname ? `via ${r.hostname}, ` : ""}gelezen {ago(r.read_at)}
      </span>
    </span>
  );
}

// StatusBar toont de koppeling met NPM en Authentik, de aantallen en een
// knop om alles nu opnieuw te lezen.
export function StatusBar({ ov, admin }: { ov: PublishOverview; admin: boolean }) {
  const refresh = useRefreshPublish();
  const c = ov.counts;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm">
        <LinkState name="NPM" r={ov.npm} />
        <LinkState name="Authentik" r={ov.authentik} />
        <span className="text-slate-600 dark:text-slate-300">
          Hoofddomein <span className="font-medium">{ov.settings.domain}</span>
        </span>
        <span className="text-slate-600 dark:text-slate-300">
          {c.web} webdiensten, {c.unpublished} niet gepubliceerd{c.internal ? ` (${c.internal} intern verborgen)` : ""} · {c.hosts} proxy hosts,{" "}
          {c.managed} beheerd, {c.protected} achter Authentik
        </span>
        {admin && (
          <Button variant="secondary" className="ml-auto" disabled={refresh.isPending} onClick={() => refresh.mutate()}>
            {refresh.isPending ? "Lezen…" : "Nu opnieuw lezen"}
          </Button>
        )}
      </div>
      {ov.npm.error && (
        <Alert>
          NPM: {ov.npm.error} (fout van {ago(ov.npm.read_at)}; ClusterForge probeert het elke 30 seconden opnieuw)
        </Alert>
      )}
      {ov.authentik.error && (
        <Alert>
          Authentik: {ov.authentik.error} (fout van {ago(ov.authentik.read_at)}; ClusterForge probeert het elke 30 seconden opnieuw)
        </Alert>
      )}
      {!ov.npm.linked && (
        <Alert kind="info">
          NPM is nog niet gekoppeld. Kies de NPM-node of het NPM-cluster en een NPM-gebruiker in{" "}
          <Link href="/instellingen#publicatie" className="underline">
            Instellingen → Domeinen en publicatie
          </Link>
          .
        </Alert>
      )}
      {refresh.isError && <Alert>{(refresh.error as Error).message}</Alert>}
    </div>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <p className="text-sm text-slate-500">{children}</p>;
}
