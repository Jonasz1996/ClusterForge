"use client";

import { useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { QueryState } from "@/components/inventory/bits";
import { DatacenterGate } from "@/components/pve/bits";
import { CleanupView } from "@/components/pve/Cleanup";
import { DiskView } from "@/components/pve/Disks";
import { MediaView } from "@/components/pve/Media";
import { StorageList } from "@/components/pve/Storage";
import { Tabs } from "@/components/Tabs";
import { PageHeader } from "@/components/ui";
import { usePveGet, type Datacenter, type PveResource } from "@/lib/pve";

export default function StoragePage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Opslag"
        description="Opslag van het datacenter, de schijven in de hosts met hun SMART- en ZFS-toestand, ISO's en templates, en opruimen wat niemand meer gebruikt."
      />
      <Suspense>
        <DatacenterGate>{(dc) => <StorageView key={dc.id} dc={dc} />}</DatacenterGate>
      </Suspense>
    </div>
  );
}

type Tab = "storage" | "disks" | "media" | "cleanup";

function StorageView({ dc }: { dc: Datacenter }) {
  const res = usePveGet<PveResource[]>(dc.id, "/cluster/resources", {
    refetch: 30_000,
  });
  const t = useSearchParams().get("tab");
  const [tab, setTab] = useState<Tab>(t === "disks" || t === "media" || t === "cleanup" ? t : "storage");
  return (
    <QueryState q={res}>
      <div className="space-y-4">
        <Tabs
          value={tab}
          onChange={setTab}
          label="Opslag"
          tabs={[
            { key: "storage", label: "Opslag" },
            { key: "disks", label: "Schijven" },
            { key: "media", label: "ISO's en templates" },
            { key: "cleanup", label: "Opruimen" },
          ]}
        />
        {tab === "storage" && <StorageList dc={dc} res={res.data ?? []} />}
        {tab === "disks" && <DiskView dc={dc} />}
        {tab === "media" && <MediaView dc={dc} res={res.data ?? []} />}
        {tab === "cleanup" && <CleanupView dc={dc} res={res.data ?? []} />}
      </div>
    </QueryState>
  );
}
