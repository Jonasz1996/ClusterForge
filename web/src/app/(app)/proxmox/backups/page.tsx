"use client";

import Link from "next/link";
import { Suspense, useState } from "react";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { BackupJobs } from "@/components/pve/BackupJobs";
import { BackupList } from "@/components/pve/Backups";
import { DatacenterGate } from "@/components/pve/bits";
import { Tabs } from "@/components/Tabs";
import { Badge, Card, PageHeader } from "@/components/ui";
import { usePveGet, type Datacenter, type PveResource } from "@/lib/pve";
import { jobStatuses, useJobs } from "@/lib/proxmox";

export default function BackupsPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Back-ups"
        description="Alle back-ups van je Proxmox-gasten, back-upjobs, terugzetten, en terugzettests die bewijzen dat een back-up echt werkt."
      />
      <Suspense>
        <DatacenterGate>{(dc) => <BackupsView key={dc.id} dc={dc} />}</DatacenterGate>
      </Suspense>
    </div>
  );
}

type Tab = "list" | "jobs" | "tests";

function BackupsView({ dc }: { dc: Datacenter }) {
  const res = usePveGet<PveResource[]>(dc.id, "/cluster/resources", { refetch: 30_000 });
  const [tab, setTab] = useState<Tab>("list");
  return (
    <QueryState q={res}>
      <div className="space-y-4">
        <Tabs
          value={tab}
          onChange={setTab}
          label="Back-ups"
          tabs={[
            { key: "list", label: "Back-ups" },
            { key: "jobs", label: "Jobs" },
            { key: "tests", label: "Terugzettests" },
          ]}
        />
        {tab === "list" && <BackupList dc={dc} res={res.data ?? []} />}
        {tab === "jobs" && <BackupJobs dc={dc} res={res.data ?? []} />}
        {tab === "tests" && <RestoreTests />}
      </div>
    </QueryState>
  );
}

function RestoreTests() {
  const jobs = useJobs({ limit: 200 });
  const tests = (jobs.data ?? []).filter((j) => j.title.startsWith("Terugzettest"));
  return (
    <Card title="Terugzettests">
      <p className="mb-3 text-sm text-slate-500">
        Start een test bij een back-up (Acties → Terugzettest). ClusterForge zet hem terug als losse gast zonder netwerk, start hem, controleert of hij opstart en
        ruimt hem weer op.
      </p>
      <QueryState q={jobs}>
        {tests.length === 0 ? (
          <Empty>Nog geen terugzettests.</Empty>
        ) : (
          <div className="relative -mx-4 overflow-x-auto sm:mx-0">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Test</th>
                  <th className={thClass}>Wanneer</th>
                  <th className={thClass}>Uitkomst</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {tests.map((j) => (
                  <tr key={j.id}>
                    <td className={tdClass}>
                      <Link href={`/taken/detail?id=${j.id}`} className="hover:underline">
                        {j.title.replace(/^Terugzettest van /, "")}
                      </Link>
                      {j.error && <span className="block text-xs text-red-700 dark:text-red-300">{j.error}</span>}
                    </td>
                    <td className={tdClass + " whitespace-nowrap"}>{new Date(j.created_at).toLocaleString("nl-BE", { dateStyle: "short", timeStyle: "short" })}</td>
                    <td className={tdClass}>
                      <Badge tone={jobStatuses[j.status].tone}>{j.status === "succeeded" ? "Terug te zetten" : jobStatuses[j.status].label}</Badge>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </QueryState>
    </Card>
  );
}
