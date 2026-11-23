"use client";

import Link from "next/link";
import { useState } from "react";
import { Modal } from "@/components/Modal";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { Alert, Button } from "@/components/ui";
import { useClusters, useNodes } from "@/lib/inventory";
import { useRunOnce } from "@/lib/updates";

// RunCommandAction draait een oplossing die de server voorstelt (zoals VRRP
// toelaten in ufw) als eenmalig commando, na het script te tonen. Live
// uitvoer in de taak; niets komt in de lijst met eigen commando's.
export function RunCommandAction({
  title,
  script,
  nodeIds,
  onClose,
}: {
  title: string;
  script: string;
  nodeIds: string[];
  onClose: () => void;
}) {
  const nodes = useNodes();
  const clusters = useClusters();
  const totp = useProdTotp();
  const run = useRunOnce();
  const [confirm, setConfirm] = useState("");
  const picked = (nodes.data ?? []).filter((n) => nodeIds.includes(n.id));
  const prod = [
    ...new Set(
      picked
        .map((n) => clusters.data?.find((c) => c.id === n.cluster_id))
        .filter((c) => c?.environment === "prod")
        .map((c) => c!.slug),
    ),
  ];
  const blocked = prod.length > 1 || (prod.length === 1 && !prodConfirmed(prod[0], confirm, totp));
  return (
    <Modal title={title} onClose={onClose}>
      {run.data ? (
        <div className="space-y-3 text-sm">
          <Alert kind="success">Gestart.</Alert>
          <p>
            <Link href={`/taken/detail?id=${run.data.id}`} className="font-medium text-brand-700 underline dark:text-sky-300">
              {run.data.title}
            </Link>{" "}
            (uitvoer live in de taak)
          </p>
          <div className="flex justify-end">
            <Button onClick={onClose}>Sluiten</Button>
          </div>
        </div>
      ) : (
        <div className="space-y-4 text-sm">
          <p>ClusterForge voert dit uit op {picked.length > 0 ? picked.map((n) => n.hostname).join(", ") : `${nodeIds.length} nodes`}:</p>
          <pre className="overflow-x-auto rounded-md bg-slate-100 p-2 font-mono text-xs dark:bg-slate-800">{script}</pre>
          {prod.length > 1 && <Alert>Deze nodes horen bij meer dan één prodcluster. Doe het per cluster.</Alert>}
          {prod.length === 1 && <ProdConfirm slug={prod[0]} value={confirm} onChange={setConfirm} />}
          {run.error && <Alert>{run.error instanceof Error ? run.error.message : "Uitvoeren mislukte"}</Alert>}
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={onClose}>
              Annuleren
            </Button>
            <Button
              disabled={blocked || run.isPending}
              onClick={() =>
                run.mutate({
                  name: title,
                  script,
                  node_ids: nodeIds,
                  ...(prod.length === 1 ? { confirm } : {}),
                })
              }
            >
              Uitvoeren
            </Button>
          </div>
        </div>
      )}
    </Modal>
  );
}
