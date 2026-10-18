"use client";

import { useState } from "react";
import { GroupCard } from "@/components/cron/Cron";
import { upgradeCommand } from "@/components/drift/Drift";
import { Empty, QueryState } from "@/components/inventory/bits";
import { CopyBlock } from "@/components/inventory/InstallAgent";
import { Button, Card, PageHeader, Select } from "@/components/ui";
import { useCronjobs, useRefreshCronjobs } from "@/lib/cronjobs";

export default function CronjobsPage() {
  const groups = useCronjobs();
  const refresh = useRefreshCronjobs();
  const [cluster, setCluster] = useState("");
  const [showSystem, setShowSystem] = useState(false);
  const oldAgents = [
    ...new Set((groups.data ?? []).flatMap((g) => g.nodes.filter((n) => n.error_kind === "old_agent").map((n) => n.hostname))),
  ];
  const shown = (groups.data ?? []).filter((g) => cluster === "" || g.key === cluster);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Cronjobs"
        description="Elke cronjob en systemd-timer op je nodes, per cluster: wanneer hij draait, wat hij doet, en hoe de laatste run ging."
        actions={
          <Button variant="secondary" onClick={() => refresh.mutate()} disabled={refresh.isPending}>
            {refresh.isPending || refresh.isSuccess ? "Wordt gelezen…" : "Nu opnieuw lezen"}
          </Button>
        }
      />

      {oldAgents.length > 0 && (
        <Card title="Agent bijwerken">
          <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
            Op {oldAgents.join(", ")} draait een agent die de cronjobs nog niet kan lezen. Werk hem bij met deze regel op elke node;
            de cronjobs verschijnen binnen vijf minuten.
          </p>
          <CopyBlock text={upgradeCommand()} />
        </Card>
      )}

      <div className="flex flex-wrap items-center gap-4 text-sm">
        <label className="flex items-center gap-2">
          Cluster
          <Select value={cluster} onChange={(e) => setCluster(e.target.value)} className="w-auto">
            <option value="">Alle</option>
            {(groups.data ?? []).map((g) => (
              <option key={g.key} value={g.key}>
                {g.cluster_id ? g.cluster_name : "Nodes zonder cluster"}
              </option>
            ))}
          </Select>
        </label>
        <label className="flex items-center gap-2">
          <input type="checkbox" checked={showSystem} onChange={(e) => setShowSystem(e.target.checked)} />
          Ook de jobs van Debian-pakketten tonen
        </label>
      </div>

      <QueryState q={groups}>
        {shown.length === 0 ? (
          <Empty>
            Nog geen cronjobs gelezen. ClusterForge leest ze op elke node met cf-agent 16 of nieuwer, elke vijf minuten.
          </Empty>
        ) : (
          <div className="space-y-6">
            {shown.map((g) => (
              <GroupCard key={g.key} g={g} showSystem={showSystem} />
            ))}
          </div>
        )}
      </QueryState>
    </div>
  );
}
