"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { Modal } from "@/components/Modal";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card } from "@/components/ui";
import {
  roleLabel,
  stateInfo,
  useClusterComponents,
  useReapplyComponent,
  useRemoveComponent,
  type ClusterComponent,
} from "@/lib/components";
import type { ClusterDetail } from "@/lib/inventory";

// ClusterDatabaseCard toont de database die ClusterForge op de nodes van
// dit cluster zette, of de knop om er een toe te voegen.
export function ClusterDatabaseCard({ c, isAdmin }: { c: ClusterDetail; isAdmin: boolean }) {
  const q = useClusterComponents(c.id);
  const list = q.data ?? [];
  const add = `/clusters/database?id=${c.id}`;
  return (
    <Card
      title={
        <span className="flex flex-wrap items-start justify-between gap-2">
          <span>Database</span>
          {isAdmin && q.data && list.length === 0 && (
            <Link href={add}>
              <Button variant="secondary">Database toevoegen</Button>
            </Link>
          )}
        </span>
      }
    >
      <QueryState q={q}>
        {q.data && list.length === 0 && (
          <p className="text-sm text-slate-600 dark:text-slate-400">
            ClusterForge heeft hier geen database gezet. Met Database toevoegen komt MariaDB Galera op de nodes van dit cluster,
            zodat de app zijn database naast zich heeft. Wat er al aan databases draait, staat bij{" "}
            <Link href="/databases" className="underline">
              Databases
            </Link>
            .
          </p>
        )}
        <div className="space-y-6">
          {list.map((comp) => (
            <Component key={comp.id} c={c} comp={comp} isAdmin={isAdmin} />
          ))}
        </div>
      </QueryState>
    </Card>
  );
}

function Component({ c, comp, isAdmin }: { c: ClusterDetail; comp: ClusterComponent; isAdmin: boolean }) {
  const st = stateInfo[comp.state] ?? { label: comp.state, tone: "slate" as const };
  const busy = comp.state === "installing" || comp.state === "applying";
  const [reapply, setReapply] = useState(false);
  const remove = useRemoveComponent(c.id);
  const settings = Object.entries(comp.params);
  return (
    <div className="space-y-3 text-sm">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="flex flex-wrap items-center gap-2">
          <span className="font-medium">{comp.title}</span>
          <Badge tone={st.tone}>{st.label}</Badge>
          <span className="text-slate-500">
            revisie {comp.revision}
            {comp.applied_revision !== null && comp.applied_revision !== comp.revision && `, toegepast ${comp.applied_revision}`}
          </span>
        </span>
        {isAdmin && (
          <span className="flex flex-wrap gap-2">
            {comp.applied_revision === null && !busy && (
              <Button variant="secondary-danger" disabled={remove.isPending} onClick={() => remove.mutate(comp.id)}>
                Weghalen
              </Button>
            )}
            <Button variant="secondary" disabled={busy} onClick={() => setReapply(true)}>
              Opnieuw toepassen
            </Button>
          </span>
        )}
      </div>
      {comp.job_id && (
        <p className="text-slate-500">
          {busy ? "Bezig: " : "Laatste taak: "}
          <Link href={`/taken/detail?id=${comp.job_id}`} className="underline">
            bekijk het logboek
          </Link>
          {busy && ". ClusterForge zet de nodes een voor een op en wacht na elke node tot hij Synced is."}
        </p>
      )}
      {comp.state === "failed" && (
        <Alert>
          De laatste taak mislukte{comp.job_error && `: ${comp.job_error}`}. Los het op en kies Opnieuw toepassen; wat al klaar
          was, blijft staan.
          {comp.applied_revision === null && " Weghalen haalt de database uit ClusterForge; de nodes zelf blijven zoals ze zijn."}
        </Alert>
      )}
      {remove.isError && <Alert>{remove.error.message}</Alert>}
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Node</th>
              <th className={thClass}>Rol</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {comp.nodes.map((n) => (
              <tr key={n.node_id}>
                <td className={tdClass}>
                  <Link href={`/nodes/detail?id=${n.node_id}`} className="font-medium hover:underline">
                    {n.hostname}
                  </Link>
                </td>
                <td className={tdClass}>{roleLabel[n.role] ?? n.role}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {settings.length > 0 && (
        <p className="text-slate-600 dark:text-slate-400">
          Instellingen:{" "}
          {settings.map(([k, v], i) => (
            <span key={k}>
              {i > 0 && ", "}
              <span className="font-mono">{k}</span> {String(v)}
            </span>
          ))}
          .
        </p>
      )}
      {reapply && <ReapplyDialog c={c} comp={comp} onClose={() => setReapply(false)} />}
    </div>
  );
}

function ReapplyDialog({ c, comp, onClose }: { c: ClusterDetail; comp: ClusterComponent; onClose: () => void }) {
  const reapply = useReapplyComponent(c.id);
  const router = useRouter();
  const totp = useProdTotp();
  const [confirm, setConfirm] = useState("");
  const prod = c.environment === "prod";
  return (
    <Modal title={`${comp.title} opnieuw toepassen`} onClose={onClose}>
      <div className="space-y-4 text-sm">
        <p>
          ClusterForge zet de configuratie van revisie {comp.revision} terug op elke node, een voor een. Een node waarop iets
          veranderde, herstart MariaDB en ClusterForge wacht tot hij weer Synced is voor hij verder gaat. De andere nodes blijven
          draaien, dus de database blijft bereikbaar.
        </p>
        {prod && <ProdConfirm slug={c.slug} value={confirm} onChange={setConfirm} />}
        {reapply.isError && <Alert>{reapply.error.message}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            disabled={reapply.isPending || (prod && !prodConfirmed(c.slug, confirm, totp))}
            onClick={async () => {
              const j = await reapply.mutateAsync({ id: comp.id, confirm: prod ? confirm : undefined }).catch(() => null);
              if (j) router.push(`/taken/detail?id=${j.id}`);
            }}
          >
            {reapply.isPending ? "Bezig…" : "Opnieuw toepassen"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
