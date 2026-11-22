"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { useState } from "react";
import { Tabs } from "@/components/Tabs";
import { AttentionCard } from "@/components/AttentionCard";
import { AuditList } from "@/components/audit/AuditList";
import { OpenIncidentsCard } from "@/components/incidents/Incidents";
import { ClusterHealth } from "@/components/monitoring/ClusterHealth";
import { SocStrip } from "@/components/soc/SocStrip";
import { FleetRiskCard } from "@/components/risk/Risk";
import { Alert, Card, PageHeader } from "@/components/ui";
import { api } from "@/lib/api/client";
import { useAuditRecent } from "@/lib/audit";
import { useMe } from "@/lib/auth";
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

  const [tab, setTab] = useState<"status" | "incidenten" | "gezondheid" | "activiteit">("status");

  // Overzicht (fase 47): bovenaan één lijst "Wat moet ik doen", per oorzaak
  // en met de acties erbij. Wat vroeger allemaal onder elkaar stond (status,
  // incidenten, gezondheid, activiteit), staat eronder in tabbladen.
  return (
    <div className="space-y-6">
      <PageHeader title="Overzicht" description="Wat je nu moet doen, per oorzaak, met de knop die het oplost. Eronder de status van alles." />

      <AttentionCard />

      <div>
        <Tabs
          className="mb-4"
          value={tab}
          onChange={setTab}
          label="Meer op het overzicht"
          tabs={[
            { key: "status", label: "Status" },
            { key: "incidenten", label: "Incidenten" },
            { key: "gezondheid", label: "Gezondheid en verbeterpunten" },
            ...(isAdmin ? [{ key: "activiteit" as const, label: "Activiteit" }] : []),
          ]}
        />

        {tab === "status" && (
          <div className="space-y-6">
            <SocStrip />
            <div className="grid grid-cols-2 gap-3 sm:gap-4 lg:grid-cols-4">
              <Link href="/clusters">
                <Stat
                  label="Clusters"
                  value={
                    clusters.data
                      ? `${clusters.data.length}` +
                        (clusters.data.some((c) => c.status !== "unknown")
                          ? ` · ${clusters.data.filter((c) => c.status === "healthy" || c.status === "degraded").length} werken`
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
            <p className="text-sm text-slate-500">
              Nieuw hier, of weet je niet waar te beginnen?{" "}
              <Link href="/aan-de-slag" className="text-brand-700 hover:underline dark:text-sky-300">
                Aan de slag
              </Link>{" "}
              zet de gewone taken stap voor stap op een rij.
            </p>
          </div>
        )}

        {tab === "incidenten" && <OpenIncidentsCard />}

        {tab === "gezondheid" && <FleetRiskCard isAdmin={isAdmin} />}

        {tab === "activiteit" && isAdmin && (
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
