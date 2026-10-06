"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { AuditList } from "@/components/audit/AuditList";
import { ClusterHealth } from "@/components/monitoring/ClusterHealth";
import { Alert, Card } from "@/components/ui";
import { api } from "@/lib/api/client";
import { useAuditRecent } from "@/lib/audit";
import { useMe } from "@/lib/auth";
import { useGitRepo } from "@/lib/gitops";
import { useClusters, useNodes } from "@/lib/inventory";

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
  const events = useAuditRecent({}, 20, isAdmin);
  const git = useGitRepo();
  const waiting = git.data?.pending_changes ?? 0;

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

      {waiting > 0 && (
        <Alert kind="info">
          <Link href="/gitops" className="font-medium underline">
            {waiting === 1 ? "1 wijziging uit Git wacht" : `${waiting} wijzigingen uit Git wachten`} op goedkeuring
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
        <Card
          title={
            <span className="flex flex-wrap items-center justify-between gap-2">
              <span>Recente activiteit</span>
              <Link href="/logboek" className="text-sm font-normal text-brand-600 hover:underline dark:text-brand-500">
                Naar het logboek
              </Link>
            </span>
          }
        >
          {events.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
          {events.isError && <Alert>Activiteit kon niet geladen worden.</Alert>}
          {events.data && <AuditList items={events.data} />}
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
