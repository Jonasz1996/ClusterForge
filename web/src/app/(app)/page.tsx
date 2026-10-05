"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { ClusterHealth } from "@/components/monitoring/ClusterHealth";
import { Alert, Card } from "@/components/ui";
import { api, unwrap, type ApiEvent } from "@/lib/api/client";
import { useMe } from "@/lib/auth";
import { statuses, useClusters, useNodes, type Status } from "@/lib/inventory";
import { vmStatusInfo } from "@/lib/proxmox";

const actionLabels: Record<string, string> = {
  "auth.login": "Ingelogd",
  "auth.login_failed": "Mislukte inlogpoging",
  "auth.logout": "Uitgelogd",
  "auth.password_changed": "Wachtwoord gewijzigd",
  "auth.totp_enabled": "Tweestapsverificatie aangezet",
  "auth.totp_disabled": "Tweestapsverificatie uitgezet",
  "user.created": "Gebruiker aangemaakt",
  "cluster.created": "Cluster aangemaakt",
  "cluster.updated": "Cluster gewijzigd",
  "cluster.deleted": "Cluster verwijderd",
  "node.created": "Node aangemaakt",
  "node.updated": "Node gewijzigd",
  "node.deleted": "Node verwijderd",
  "vip.created": "VIP toegevoegd",
  "vip.updated": "VIP gewijzigd",
  "vip.deleted": "VIP verwijderd",
  "vip.owner_changed": "VIP verhuisd",
  "node.status_changed": "Status van node",
  "cluster.status_changed": "Status van cluster",
  "agent.enrolled": "Agent aangemeld",
  "agent.revoked": "Agent ingetrokken",
  "node.facts_changed": "Facts gewijzigd",
  "enrollment_token.created": "Enrollmenttoken gemaakt",
  "enrollment_token.deleted": "Enrollmenttoken ingetrokken",
  "proxmox.created": "Proxmox gekoppeld",
  "proxmox.updated": "Proxmox-koppeling gewijzigd",
  "proxmox.deleted": "Proxmox-koppeling verwijderd",
  "proxmox.sync_failed": "Proxmox niet bereikbaar",
  "proxmox.sync_recovered": "Proxmox weer bereikbaar",
  "vm.status_changed": "VM-status",
  "vm.moved": "VM verhuisd",
  "vm.missing": "VM verdwenen uit Proxmox",
  "job.queued": "Taak gestart",
  "job.succeeded": "Taak gelukt",
  "job.failed": "Taak mislukt",
  "job.canceled": "Taak geannuleerd",
  "job.cancel_requested": "Taak annuleren gevraagd",
};

// subject geeft een leesbare naam voor het onderwerp van een event, voor zover
// de payload die bevat.
function subject(e: ApiEvent): string | null {
  const p = e.payload;
  for (const k of ["name", "hostname", "address", "title"]) {
    const v = p[k];
    if (typeof v === "string") return v;
    if (v && typeof v === "object" && "to" in v && typeof v.to === "string") return v.to;
  }
  return null;
}

// detail vult een event aan met de nieuwe status of eigenaar.
function detail(e: ApiEvent): string | null {
  const p = e.payload;
  if ((e.action === "vm.status_changed" || e.action === "vm.moved") && typeof p.to === "string") {
    return `→ ${e.action === "vm.moved" ? p.to : vmStatusInfo(p.to).label.toLowerCase()}`;
  }
  if (e.action === "job.failed" && typeof p.error === "string") return p.error;
  if (e.action.endsWith(".status_changed") && typeof p.to === "string") {
    return `→ ${statuses[p.to as Status]?.label ?? p.to}`;
  }
  if (e.action === "vip.owner_changed" && "owner_hostname" in p) {
    return typeof p.owner_hostname === "string" ? `→ ${p.owner_hostname}` : "→ geen eigenaar";
  }
  return null;
}

export default function OverviewPage() {
  const me = useMe();
  const isAdmin = me.data?.user.role === "admin";
  const health = useQuery({
    queryKey: ["health"],
    queryFn: async () => (await api.GET("/health")).data,
    refetchInterval: 30_000,
  });
  const clusters = useClusters();
  const nodes = useNodes(true);
  const events = useQuery({
    queryKey: ["events", 20],
    queryFn: async () => unwrap(await api.GET("/events", { params: { query: { limit: 20 } } })).items,
    enabled: isAdmin,
  });

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Overzicht</h1>
        <p className="mt-1 text-sm text-slate-600 dark:text-slate-400">
          De status van je clusters en wie elk VIP heeft, live bijgewerkt.
        </p>
      </div>

      {me.data && !me.data.user.totp_enabled && (
        <Alert kind="info">
          Tweestapsverificatie staat nog uit.{" "}
          <Link href="/instellingen" className="font-medium underline">
            Zet het aan bij Instellingen
          </Link>
          .
        </Alert>
      )}

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Link href="/clusters">
          <Stat
            label="Clusters"
            value={
              clusters.data
                ? `${clusters.data.length}` +
                  (clusters.data.some((c) => c.status !== "unknown")
                    ? ` · ${clusters.data.filter((c) => c.status === "healthy").length} gezond`
                    : "")
                : "…"
            }
          />
        </Link>
        <Link href="/nodes">
          <Stat
            label="Nodes"
            value={
              nodes.data
                ? `${nodes.data.length}` +
                  (nodes.data.some((n) => n.agent)
                    ? ` · ${nodes.data.filter((n) => n.agent?.connection === "online").length} online`
                    : "")
                : "…"
            }
          />
        </Link>
        <Stat label="Server" value={health.data?.status === "ok" ? "Gezond" : health.isLoading ? "…" : "Probleem"} />
        <Stat label="Database" value={health.data?.database === "ok" ? "Bereikbaar" : health.isLoading ? "…" : "Onbereikbaar"} />
      </div>

      {clusters.data && clusters.data.length > 0 && (
        <section className="space-y-3">
          <h2 className="text-lg font-semibold">Clusterstatus</h2>
          <ClusterHealth clusters={clusters.data} />
        </section>
      )}

      {isAdmin && (
        <Card title="Recente activiteit">
          {events.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
          {events.isError && <Alert>Activiteit kon niet geladen worden.</Alert>}
          {events.data && <EventList items={events.data} />}
        </Card>
      )}
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border border-slate-200 bg-white px-4 py-3 dark:border-slate-800 dark:bg-slate-900">
      <div className="text-xs font-medium uppercase tracking-wide text-slate-500">{label}</div>
      <div className="mt-1 text-lg font-semibold">{value}</div>
    </div>
  );
}

function EventList({ items }: { items: ApiEvent[] }) {
  if (items.length === 0) return <p className="text-sm text-slate-500">Nog geen activiteit.</p>;
  const fmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "short", timeStyle: "medium" });
  return (
    <ul className="divide-y divide-slate-100 dark:divide-slate-800">
      {items.map((e) => (
        <li key={e.id} className="flex items-baseline justify-between gap-4 py-2 text-sm">
          <span>
            {actionLabels[e.action] ?? e.action}
            {subject(e) && <span className="font-medium"> {subject(e)}</span>}
            {detail(e) && <span> {detail(e)}</span>}
            {typeof e.payload.ip === "string" && <span className="text-slate-500"> vanaf {e.payload.ip}</span>}
          </span>
          <time className="shrink-0 text-xs text-slate-500" dateTime={e.ts}>
            {fmt.format(new Date(e.ts))}
          </time>
        </li>
      ))}
    </ul>
  );
}
