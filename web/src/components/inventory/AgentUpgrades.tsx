"use client";

import Link from "next/link";
import { useState } from "react";
import { Modal } from "@/components/Modal";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { CopyBlock, scriptCommand } from "@/components/inventory/InstallAgent";
import { Alert, Badge, Button, Card } from "@/components/ui";
import {
  upgradable,
  upgradeStates,
  useAgentUpgrades,
  useSetAgentUpgradeAuto,
  useSetClusterAgentUpgradeAuto,
  useStartAgentUpgrades,
  type AgentUpgradeAgent,
  type AgentUpgradeInput,
} from "@/lib/agentUpgrades";
import { useClusters } from "@/lib/inventory";

function StateBadge({ a }: { a: AgentUpgradeAgent }) {
  const s = upgradeStates[a.state];
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

function Started({ result }: { result: { jobs: { id: string; title: string }[]; skipped: string[] } }) {
  return (
    <Alert kind="success">
      {result.jobs.length === 1 ? "Taak gestart: " : `${result.jobs.length} taken gestart: `}
      {result.jobs.map((j, i) => (
        <span key={j.id}>
          {i > 0 && ", "}
          <Link href={`/taken/detail?id=${j.id}`} className="underline">
            {j.title}
          </Link>
        </span>
      ))}
      . Per cluster één node tegelijk.
      {result.skipped.length > 0 && <span className="block">Overgeslagen: {result.skipped.join("; ")}</span>}
    </Alert>
  );
}

// ProdUpgrade vraagt de slug en tweestapsverificatie voor een prodcluster.
function ProdUpgrade({
  title,
  slug,
  body,
  onClose,
}: {
  title: string;
  slug: string;
  body: AgentUpgradeInput;
  onClose: () => void;
}) {
  const start = useStartAgentUpgrades();
  const totp = useProdTotp();
  const [typed, setTyped] = useState("");
  return (
    <Modal title={title} onClose={onClose}>
      <div className="space-y-4 text-sm">
        {start.data ? (
          <Started result={start.data} />
        ) : (
          <>
            <p>
              ClusterForge werkt de agents één node tegelijk bij. De agent herstart, de services op de node niet. Praat een nieuwe
              agent niet binnen 3 minuten met de server, dan zet de node de oude terug en stopt de taak.
            </p>
            <ProdConfirm slug={slug} value={typed} onChange={setTyped} />
          </>
        )}
        {start.isError && <Alert>{start.error.message}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            {start.data ? "Sluiten" : "Annuleren"}
          </Button>
          {!start.data && (
            <Button
              disabled={!prodConfirmed(slug, typed, totp) || start.isPending}
              onClick={() => start.mutate({ ...body, confirm: typed })}
            >
              Bijwerken
            </Button>
          )}
        </div>
      </div>
    </Modal>
  );
}

// AgentUpgradeCard staat bij Nodes: welke agents verouderd zijn, met de knop
// om ze bij te werken. Hij verschijnt alleen als er iets te doen of te
// weten is.
export function AgentUpgradeCard({ isAdmin }: { isAdmin: boolean }) {
  const q = useAgentUpgrades();
  const start = useStartAgentUpgrades();
  const [prod, setProd] = useState<{ id: string; name: string; slug: string } | null>(null);
  const ov = q.data;
  if (!ov) return null;
  const open = ov.agents.filter((a) => a.state !== "current" && a.state !== "unknown");
  if (open.length === 0 && ov.running.length === 0) return null;

  const outside = ov.agents.filter((a) => upgradable(a) && a.cluster_environment !== "prod");
  const prodClusters = new Map<string, { id: string; name: string; slug: string; n: number }>();
  for (const a of ov.agents) {
    if (!upgradable(a) || a.cluster_environment !== "prod" || !a.cluster_id) continue;
    const c = prodClusters.get(a.cluster_id) ?? { id: a.cluster_id, name: a.cluster_name, slug: a.cluster_slug, n: 0 };
    c.n++;
    prodClusters.set(a.cluster_id, c);
  }
  const manual = open.some((a) => a.state === "manual");

  return (
    <Card title="Agents bijwerken">
      <div className="space-y-4 text-sm">
        <p className="text-slate-600 dark:text-slate-400">
          Deze server biedt agent {ov.version} aan (protocol {ov.protocol}). Een agent vanaf protocol 23 werkt zichzelf bij: hij
          haalt de nieuwe van deze server, controleert de sha256 en zet de oude terug als de nieuwe niet binnen 3 minuten met de
          server praat. Per cluster één node tegelijk.{" "}
          {ov.auto ? (
            <>
              Vanzelf bijwerken staat aan, behalve op prod (
              <Link href="/instellingen#agents-bijwerken" className="underline">
                Instellingen
              </Link>
              ).
            </>
          ) : (
            <>
              Vanzelf bijwerken staat uit (
              <Link href="/instellingen#agents-bijwerken" className="underline">
                Instellingen
              </Link>
              ).
            </>
          )}
        </p>
        {ov.running.length > 0 && (
          <Alert kind="info">
            Bezig:{" "}
            {ov.running.map((r, i) => (
              <span key={r.job_id}>
                {i > 0 && ", "}
                <Link href={`/taken/detail?id=${r.job_id}`} className="underline">
                  bekijk de taak
                </Link>
              </span>
            ))}
            .
          </Alert>
        )}
        {open.length > 0 && (
          <ul className="divide-y divide-slate-100 dark:divide-slate-800">
            {open.map((a) => (
              <li key={a.node_id} className="flex flex-wrap items-baseline gap-x-3 gap-y-1 py-2">
                <Link href={`/nodes/detail?id=${a.node_id}`} className="min-w-32 font-medium text-brand-700 hover:underline dark:text-sky-300">
                  {a.hostname}
                </Link>
                <span className="text-slate-500">{a.cluster_name || "zonder cluster"}</span>
                <span className="font-mono text-xs text-slate-600 dark:text-slate-400">
                  {a.version || "?"} · protocol {a.protocol}
                </span>
                <StateBadge a={a} />
                {!a.online && <Badge>Niet verbonden</Badge>}
                {a.auto && <span className="text-xs text-slate-500">gaat vanzelf</span>}
                {a.why && <span className="basis-full text-xs text-slate-500">{a.why}</span>}
              </li>
            ))}
          </ul>
        )}
        {manual && (
          <div className="space-y-2">
            <p>
              Agents onder protocol 23 werk je één keer met de hand bij. Daarna doen ze het zelf. Op elke node met &quot;Met de hand
              bijwerken&quot;:
            </p>
            <CopyBlock text={scriptCommand("--upgrade")} />
          </div>
        )}
        {start.data && <Started result={start.data} />}
        {start.isError && <Alert>{start.error.message}</Alert>}
        {isAdmin && (outside.length > 0 || prodClusters.size > 0) && (
          <div className="flex flex-wrap gap-2">
            {outside.length > 0 && (
              <Button disabled={start.isPending} onClick={() => start.mutate({ all: true })}>
                Agents bijwerken ({outside.length})
              </Button>
            )}
            {[...prodClusters.values()].map((c) => (
              <Button key={c.id} variant="secondary" onClick={() => setProd(c)}>
                Bijwerken in {c.name} (prod, {c.n})
              </Button>
            ))}
          </div>
        )}
      </div>
      {prod && (
        <ProdUpgrade
          title={`Agents bijwerken in ${prod.name}`}
          slug={prod.slug}
          body={{ cluster_id: prod.id }}
          onClose={() => setProd(null)}
        />
      )}
    </Card>
  );
}

// AgentUpgradeRow staat in de kaart Agent van een node.
export function AgentUpgradeRow({ nodeId, isAdmin }: { nodeId: string; isAdmin: boolean }) {
  const q = useAgentUpgrades();
  const start = useStartAgentUpgrades();
  const [confirming, setConfirming] = useState(false);
  const a = q.data?.agents.find((x) => x.node_id === nodeId);
  if (!a) return null;
  const busy = q.data!.running.some((r) => (r.cluster_id ?? null) === (a.cluster_id ?? null));
  return (
    <div className="mt-4 space-y-2 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <StateBadge a={a} />
        {a.auto && <span className="text-xs text-slate-500">wordt vanzelf bijgewerkt</span>}
        {busy && <span className="text-xs text-slate-500">er loopt een taak die agents bijwerkt</span>}
      </div>
      {a.why && <p className="text-slate-600 dark:text-slate-400">{a.why}</p>}
      {a.state === "manual" && <CopyBlock text={scriptCommand("--upgrade")} />}
      {isAdmin && upgradable(a) && (
        <Button
          variant="secondary"
          disabled={start.isPending || busy}
          onClick={() => (a.cluster_environment === "prod" ? setConfirming(true) : start.mutate({ node_ids: [a.node_id] }))}
        >
          Agent bijwerken
        </Button>
      )}
      {start.data && <Started result={start.data} />}
      {start.isError && <Alert>{start.error.message}</Alert>}
      {confirming && (
        <ProdUpgrade
          title={`Agent op ${a.hostname} bijwerken`}
          slug={a.cluster_slug}
          body={{ node_ids: [a.node_id] }}
          onClose={() => setConfirming(false)}
        />
      )}
    </div>
  );
}

// AgentUpgradeSettingsCard: vanzelf bijwerken, voor alles en per cluster.
export function AgentUpgradeSettingsCard({ isAdmin }: { isAdmin: boolean }) {
  const q = useAgentUpgrades();
  const clusters = useClusters();
  const setAuto = useSetAgentUpgradeAuto();
  const setCluster = useSetClusterAgentUpgradeAuto();
  const ov = q.data;
  if (!ov) return null;
  return (
    <Card title="Agents bijwerken">
      <div className="space-y-4 text-sm">
        <label className="flex items-start gap-2">
          <input
            type="checkbox"
            className="mt-0.5"
            checked={ov.auto}
            disabled={!isAdmin || setAuto.isPending}
            onChange={(e) => setAuto.mutate(e.target.checked)}
          />
          <span>
            Agents vanzelf bijwerken
            <span className="block text-slate-500">
              Na een update van ClusterForge werkt de server verouderde agents vanzelf bij, één node tegelijk per cluster. Nooit op
              prod: daar gebruik je de knop bij Nodes. Een update die een node terugzette, probeert hij niet nog eens vanzelf.
            </span>
          </span>
        </label>
        {ov.auto && (clusters.data?.length ?? 0) > 0 && (
          <div className="space-y-1">
            <p className="font-medium">Per cluster</p>
            {clusters.data!.map((c) => {
              const prod = c.environment === "prod";
              return (
                <label key={c.id} className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={!prod && !ov.off_cluster_ids.includes(c.id)}
                    disabled={!isAdmin || prod || setCluster.isPending}
                    onChange={(e) => setCluster.mutate({ clusterId: c.id, enabled: e.target.checked })}
                  />
                  <span>
                    {c.name}
                    {prod && <span className="text-slate-500"> · prod, nooit vanzelf</span>}
                  </span>
                </label>
              );
            })}
          </div>
        )}
        {(setAuto.isError || setCluster.isError) && <Alert>{(setAuto.error ?? setCluster.error)!.message}</Alert>}
      </div>
    </Card>
  );
}
