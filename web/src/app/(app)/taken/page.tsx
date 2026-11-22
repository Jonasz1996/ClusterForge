"use client";

import { JobList } from "@/components/jobs/JobList";
import { QueryState } from "@/components/inventory/bits";
import { Card, PageHeader } from "@/components/ui";
import { useJobs } from "@/lib/proxmox";

export default function JobsPage() {
  const jobs = useJobs({ limit: 100 });
  return (
    <div className="space-y-6">
      <PageHeader
        title="Taken"
        description="Alles wat ClusterForge voor je uitvoert, zoals VM's starten, snapshotten en migreren. Nieuwste eerst."
      />
      <Card>
        <QueryState q={jobs}>{jobs.data && <JobList jobs={jobs.data} empty="Nog geen taken. Start er een vanuit Proxmox of een node." />}</QueryState>
      </Card>
    </div>
  );
}
