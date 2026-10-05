"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { Alert, Card } from "@/components/ui";
import { api, unwrap, type ApiEvent } from "@/lib/api/client";
import { useMe } from "@/lib/auth";

const actionLabels: Record<string, string> = {
  "auth.login": "Ingelogd",
  "auth.login_failed": "Mislukte inlogpoging",
  "auth.logout": "Uitgelogd",
  "auth.password_changed": "Wachtwoord gewijzigd",
  "auth.totp_enabled": "Tweestapsverificatie aangezet",
  "auth.totp_disabled": "Tweestapsverificatie uitgezet",
  "user.created": "Gebruiker aangemaakt",
};

export default function OverviewPage() {
  const me = useMe();
  const isAdmin = me.data?.user.role === "admin";
  const health = useQuery({
    queryKey: ["health"],
    queryFn: async () => (await api.GET("/health")).data,
    refetchInterval: 30_000,
  });
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
          Clusters en nodes verschijnen hier zodra de inventory er is.
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

      <div className="grid gap-4 sm:grid-cols-3">
        <Stat label="Server" value={health.data?.status === "ok" ? "Gezond" : health.isLoading ? "…" : "Probleem"} />
        <Stat label="Database" value={health.data?.database === "ok" ? "Bereikbaar" : health.isLoading ? "…" : "Onbereikbaar"} />
        <Stat label="Versie" value={health.data?.version ?? "…"} />
      </div>

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
