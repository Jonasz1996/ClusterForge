"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { QueryState } from "@/components/inventory/bits";
import { Alert, Card, PageHeader, Select } from "@/components/ui";
import { ClassFilter, TimelineList } from "@/components/whatbroke/WhatBroke";
import { useCluster, useNode } from "@/lib/inventory";
import { timelinePeriods, useTimeline, type TimelineClass } from "@/lib/timeline";

export default function TimelinePage() {
  return (
    <Suspense>
      <TimelineInner />
    </Suspense>
  );
}

function TimelineInner() {
  const params = useSearchParams();
  const nodeId = params.get("node") ?? "";
  const clusterId = params.get("cluster") ?? "";
  if (nodeId === "" && clusterId === "") return <Alert>Geen cluster of node opgegeven.</Alert>;
  return nodeId !== "" ? <NodeTimeline id={nodeId} /> : <ClusterTimeline id={clusterId} />;
}

function NodeTimeline({ id }: { id: string }) {
  const node = useNode(id);
  return (
    <TimelineView
      kind="node"
      id={id}
      name={node.data?.hostname ?? ""}
      back={{ href: `/nodes/detail?id=${id}`, label: node.data?.hostname ?? "Node" }}
    />
  );
}

function ClusterTimeline({ id }: { id: string }) {
  const cluster = useCluster(id);
  return (
    <TimelineView
      kind="cluster"
      id={id}
      name={cluster.data?.name ?? ""}
      back={{ href: `/clusters/detail?id=${id}`, label: cluster.data?.name ?? "Cluster" }}
    />
  );
}

function TimelineView({
  kind,
  id,
  name,
  back,
}: {
  kind: "node" | "cluster";
  id: string;
  name: string;
  back: { href: string; label: string };
}) {
  const [period, setPeriod] = useState("24h");
  const [shown, setShown] = useState<TimelineClass[]>(["change", "symptom", "info"]);
  const hours = timelinePeriods.find((p) => p.value === period)?.hours ?? 24;
  const q = useTimeline(kind, id, { hours, classes: shown, limit: 500 });
  return (
    <div className="space-y-6">
      <div className="text-sm">
        <Link href={back.href} className="text-slate-500 hover:underline">
          ← {back.label}
        </Link>
      </div>
      <PageHeader
        title={`Tijdlijn${name ? ` van ${name}` : ""}`}
        description={
          kind === "cluster"
            ? "Alles wat er met het cluster en zijn nodes gebeurde: het logboek, het journaal van de nodes en bij Kubernetes de waarschuwingen samen, nieuwste eerst. Veranderingen kunnen oorzaak zijn, symptomen zijn wat er misging."
            : "Alles wat er met de node gebeurde: het logboek en zijn journaal samen, nieuwste eerst. Veranderingen kunnen oorzaak zijn, symptomen zijn wat er misging."
        }
      />
      <div className="flex flex-wrap items-center gap-3">
        <Select aria-label="Periode" className="w-32" value={period} onChange={(e) => setPeriod(e.target.value)}>
          {timelinePeriods.map((p) => (
            <option key={p.value} value={p.value}>
              {p.label}
            </option>
          ))}
        </Select>
        <ClassFilter value={shown} onChange={setShown} />
      </div>
      <Card>
        {shown.length === 0 ? (
          <p className="text-sm text-slate-500">Kies minstens één klasse.</p>
        ) : (
          <QueryState q={q}>
            {q.data && (
              <>
                <TimelineList entries={q.data.entries} newestFirst empty="Niets in deze periode." />
                {q.data.has_more && (
                  <p className="mt-3 text-sm text-slate-500">
                    Alleen de laatste 500 regels; kies een kortere periode of minder klassen voor de rest.
                  </p>
                )}
              </>
            )}
          </QueryState>
        )}
      </Card>
    </div>
  );
}
