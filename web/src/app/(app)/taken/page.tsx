"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { Suspense } from "react";
import { JobList } from "@/components/jobs/JobList";
import { QueryState } from "@/components/inventory/bits";
import { Tabs } from "@/components/Tabs";
import { Card, PageHeader } from "@/components/ui";
import { useJobs, type JobFilter } from "@/lib/proxmox";

// De filters van de takenlijst (fase 47). Standaard wat jij startte: de
// honderden automatische taken (zoals Firewall toepassen) staan onder Alles.
const filters = {
  mij: { label: "Door mij", query: { who: "me" }, empty: "Je startte nog geen taken." },
  mislukt: { label: "Mislukt", query: { status: "failed" }, empty: "Geen mislukte taken." },
  mensen: { label: "Door gebruikers", query: { who: "people" }, empty: "Nog geen taken van gebruikers." },
  alles: { label: "Alles, ook automatisch", query: {}, empty: "Nog geen taken. Start er een vanuit Proxmox of een node." },
} satisfies Record<string, { label: string; query: JobFilter; empty: string }>;

type Key = keyof typeof filters;

function Jobs() {
  const params = useSearchParams();
  const router = useRouter();
  const raw = params.get("filter");
  const key: Key = raw && raw in filters ? (raw as Key) : "mij";
  const f = filters[key];
  const jobs = useJobs({ limit: 100, ...f.query });
  return (
    <div className="space-y-6">
      <PageHeader
        title="Taken"
        description="Alles wat ClusterForge voor je uitvoert, zoals updates, commando's en VM's starten. Nieuwste eerst."
      />
      <Tabs
        value={key}
        onChange={(k) => router.replace(k === "mij" ? "/taken" : `/taken?filter=${k}`)}
        label="Welke taken"
        tabs={(Object.keys(filters) as Key[]).map((k) => ({ key: k, label: filters[k].label }))}
      />
      <Card>
        <QueryState q={jobs}>{jobs.data && <JobList jobs={jobs.data} empty={f.empty} />}</QueryState>
      </Card>
    </div>
  );
}

export default function JobsPage() {
  return (
    <Suspense>
      <Jobs />
    </Suspense>
  );
}
