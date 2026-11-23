"use client";

import Link from "next/link";
import { useState } from "react";
import { Modal } from "@/components/Modal";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { Alert, Button } from "@/components/ui";
import { useClusters, useNodes } from "@/lib/inventory";
import { useRunOnce } from "@/lib/updates";

// RestartService herstart een dienst op één of meer nodes met een eenmalig
// commando (fase 47): de knop "Herstarten" bij een oorzaak in Aandacht. Eén
// node tegelijk, met live uitvoer in de taak; niets komt in de lijst met
// eigen commando's.
export function RestartService({ unit, nodeIds, onClose }: { unit: string; nodeIds: string[]; onClose: () => void }) {
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
  // Op prod: één cluster tegelijk, zoals elk commando.
  const blocked = prod.length > 1 || (prod.length === 1 && !prodConfirmed(prod[0], confirm, totp));
  return (
    <Modal title={`${unit} herstarten`} onClose={onClose}>
      {run.data ? (
        <div className="space-y-3 text-sm">
          <Alert kind="success">Het herstarten is gestart.</Alert>
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
          <p>
            ClusterForge voert <code className="font-mono">systemctl restart {unit}</code> uit op{" "}
            {picked.length > 0 ? picked.map((n) => n.hostname).join(", ") : `${nodeIds.length} nodes`}, één node tegelijk. Lukt het op een
            node niet, dan stopt het daar.
          </p>
          {prod.length > 1 && <Alert>Deze nodes horen bij meer dan één prodcluster. Herstart per cluster vanaf de pagina van de node.</Alert>}
          {prod.length === 1 && <ProdConfirm slug={prod[0]} value={confirm} onChange={setConfirm} />}
          {run.error && <Alert>{run.error instanceof Error ? run.error.message : "Herstarten mislukte"}</Alert>}
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={onClose}>
              Annuleren
            </Button>
            <Button
              disabled={blocked || run.isPending}
              onClick={() =>
                run.mutate({
                  name: `${unit} herstarten`,
                  script: `systemctl restart -- '${unit.replace(/'/g, "")}'\nsystemctl is-active -- '${unit.replace(/'/g, "")}'`,
                  node_ids: nodeIds,
                  ...(prod.length === 1 ? { confirm } : {}),
                })
              }
            >
              Herstarten
            </Button>
          </div>
        </div>
      )}
    </Modal>
  );
}
