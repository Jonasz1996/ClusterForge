"use client";

import Link from "next/link";
import { useState } from "react";
import { Modal } from "@/components/Modal";
import { useProdTotp } from "@/components/ProdConfirm";
import { CopyBlock, scriptCommand } from "@/components/inventory/InstallAgent";
import { Alert, Badge, Button, Card, Field, Input } from "@/components/ui";
import {
  upgradable,
  upgradeStates,
  useAgentUpgrades,
  useClearProdQueue,
  useSetAgentUpgradeAuto,
  useSetAgentUpgradeProdAuto,
  useSetClusterAgentUpgradeAuto,
  useStartAgentUpgrades,
  type AgentUpgradeAgent,
  type AgentUpgradeInput,
  type AgentUpgradeOverview,
} from "@/lib/agentUpgrades";
import { useAgentEndpoints } from "@/lib/agentEndpoints";
import { ApiError } from "@/lib/api/client";
import { useClusters } from "@/lib/inventory";

function StateBadge({ a }: { a: AgentUpgradeAgent }) {
  const s = upgradeStates[a.state];
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

function Started({
  result,
}: {
  result: {
    jobs: { id: string; title: string }[];
    skipped: string[];
    queued_prod: string[];
  };
}) {
  return (
    <Alert kind="success">
      {result.jobs.length > 0 && (
        <>
          {result.jobs.length === 1
            ? "Taak gestart: "
            : `${result.jobs.length} taken gestart: `}
          {result.jobs.map((j, i) => (
            <span key={j.id}>
              {i > 0 && ", "}
              <Link href={`/taken/detail?id=${j.id}`} className="underline">
                {j.title}
              </Link>
            </span>
          ))}
          . Per cluster één node tegelijk.
        </>
      )}
      {result.queued_prod.length > 0 && (
        <span className="block">
          Productie, één cluster na het andere: {result.queued_prod.join(", ")}.
          Bij een fout stopt de rij.
        </span>
      )}
      {result.skipped.length > 0 && (
        <span className="block">Overgeslagen: {result.skipped.join("; ")}</span>
      )}
    </Alert>
  );
}

// TotpField is het veld voor de code uit de app.
function TotpField({
  id,
  value,
  onChange,
}: {
  id: string;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <Field label="Code uit de app" htmlFor={id}>
      <Input
        id={id}
        inputMode="numeric"
        autoComplete="one-time-code"
        className="w-40 font-mono tracking-widest"
        maxLength={6}
        value={value}
        onChange={(e) => onChange(e.target.value.replace(/\D/g, ""))}
      />
    </Field>
  );
}

function NoTotp() {
  return (
    <Alert>
      Op prod kan dit alleen met tweestapsverificatie.{" "}
      <Link href="/instellingen" className="font-medium underline">
        Zet het aan bij Instellingen
      </Link>
      .
    </Alert>
  );
}

function StartError({ error }: { error: Error }) {
  if (error instanceof ApiError && error.code === "invalid_totp")
    return <Alert>Deze code klopt niet of is al gebruikt.</Alert>;
  return <Alert>{error.message}</Alert>;
}

// ProdUpgrade vraagt één code van tweestapsverificatie voor alles wat op
// prod bijgewerkt wordt.
function ProdUpgrade({
  title,
  body,
  intro,
  onClose,
}: {
  title: string;
  body: AgentUpgradeInput;
  intro?: React.ReactNode;
  onClose: () => void;
}) {
  const start = useStartAgentUpgrades();
  const totp = useProdTotp();
  const [code, setCode] = useState("");
  return (
    <Modal title={title} onClose={onClose}>
      <div className="space-y-4 text-sm">
        {start.data ? (
          <Started result={start.data} />
        ) : (
          <>
            {intro}
            <p>
              ClusterForge werkt de agents één node tegelijk bij. De agent
              herstart, de services op de node niet. Praat een nieuwe agent niet
              binnen 3 minuten met de server, dan zet de node de oude terug en
              stopt de taak.
            </p>
            <div className="space-y-3 rounded-md border border-red-200 bg-red-50/60 p-3 dark:border-red-900 dark:bg-red-950/40">
              <p className="font-medium text-red-800 dark:text-red-200">
                Dit raakt prod. Eén code bevestigt alles hierboven.
              </p>
              {totp ? (
                <TotpField
                  id="agent-upgrade-code"
                  value={code}
                  onChange={setCode}
                />
              ) : (
                <NoTotp />
              )}
            </div>
          </>
        )}
        {start.isError && <StartError error={start.error} />}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            {start.data ? "Sluiten" : "Annuleren"}
          </Button>
          {!start.data && (
            <Button
              variant="danger"
              disabled={!totp || code.length !== 6 || start.isPending}
              onClick={() => start.mutate({ ...body, totp_code: code })}
            >
              Bijwerken
            </Button>
          )}
        </div>
      </div>
    </Modal>
  );
}

// ProdQueue toont de rij prodclusters van "ook productie" en of vanzelf
// bijwerken op prod stilstaat.
function ProdQueue({
  ov,
  isAdmin,
}: {
  ov: AgentUpgradeOverview;
  isAdmin: boolean;
}) {
  const clear = useClearProdQueue();
  return (
    <>
      {ov.prod_queue.length > 0 && (
        <Alert kind="info">
          <span className="block">
            Productie, één cluster na het andere:{" "}
            {ov.prod_queue.map((c, i) => (
              <span key={c.cluster_id}>
                {i > 0 && " → "}
                {c.job_id ? (
                  <Link
                    href={`/taken/detail?id=${c.job_id}`}
                    className="font-medium underline"
                  >
                    {c.cluster_name} (bezig)
                  </Link>
                ) : (
                  c.cluster_name
                )}
              </span>
            ))}
            . Bij een fout stopt de rij.
          </span>
          {isAdmin && (
            <Button
              variant="secondary"
              className="mt-2"
              disabled={clear.isPending}
              onClick={() => clear.mutate()}
            >
              Rest van de rij stoppen
            </Button>
          )}
        </Alert>
      )}
      {ov.prod_halted && (ov.prod_auto || ov.prod_queue.length === 0) && (
        <Alert>
          Op prod mislukte het bijwerken van de agent op {ov.halted_by}.{" "}
          {ov.prod_auto
            ? "Vanzelf bijwerken op prod staat stil tot die node met de knop bijgewerkt is."
            : "Kijk eerst waarom voor je verder gaat."}
        </Alert>
      )}
    </>
  );
}

// AgentUpgradeCard staat bij Nodes: welke agents verouderd zijn, met de knop
// om ze bij te werken. Hij verschijnt alleen als er iets te doen of te
// weten is.
export function AgentUpgradeCard({ isAdmin }: { isAdmin: boolean }) {
  const q = useAgentUpgrades();
  const start = useStartAgentUpgrades();
  const [prod, setProd] = useState<{
    id: string;
    name: string;
    slug: string;
  } | null>(null);
  const [withProd, setWithProd] = useState(false);
  const [batch, setBatch] = useState(false);
  const ov = q.data;
  if (!ov) return null;
  const open = ov.agents.filter(
    (a) => a.state !== "current" && a.state !== "unknown",
  );
  if (
    open.length === 0 &&
    ov.running.length === 0 &&
    ov.prod_queue.length === 0
  )
    return null;

  const outside = ov.agents.filter(
    (a) => upgradable(a) && a.cluster_environment !== "prod",
  );
  const prodClusters = new Map<
    string,
    { id: string; name: string; slug: string; n: number }
  >();
  for (const a of ov.agents) {
    if (!upgradable(a) || a.cluster_environment !== "prod" || !a.cluster_id)
      continue;
    const c = prodClusters.get(a.cluster_id) ?? {
      id: a.cluster_id,
      name: a.cluster_name,
      slug: a.cluster_slug,
      n: 0,
    };
    c.n++;
    prodClusters.set(a.cluster_id, c);
  }
  const manual = open.some((a) => a.state === "manual");
  const prodNodes = [...prodClusters.values()].reduce((n, c) => n + c.n, 0);
  const total = outside.length + (withProd ? prodNodes : 0);

  return (
    <Card title="Agents bijwerken">
      <div className="space-y-4 text-sm">
        <p className="text-slate-600 dark:text-slate-400">
          Deze server biedt agent {ov.version} aan (protocol {ov.protocol}). Een
          agent vanaf protocol 23 werkt zichzelf bij: hij haalt de nieuwe van
          deze server, controleert de sha256 en zet de oude terug als de nieuwe
          niet binnen 3 minuten met de server praat. Per cluster één node
          tegelijk.{" "}
          {ov.auto ? (
            <>
              Vanzelf bijwerken staat aan
              {ov.prod_auto
                ? ", ook op prod (één prodcluster tegelijk)"
                : ", behalve op prod"}{" "}
              (
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
                <Link
                  href={`/taken/detail?id=${r.job_id}`}
                  className="underline"
                >
                  bekijk de taak
                </Link>
              </span>
            ))}
            .
          </Alert>
        )}
        <ProdQueue ov={ov} isAdmin={isAdmin} />
        {open.length > 0 && (
          <ul className="divide-y divide-slate-100 dark:divide-slate-800">
            {open.map((a) => (
              <li
                key={a.node_id}
                className="flex flex-wrap items-baseline gap-x-3 gap-y-1 py-2"
              >
                <Link
                  href={`/nodes/detail?id=${a.node_id}`}
                  className="min-w-32 font-medium text-brand-700 hover:underline dark:text-sky-300"
                >
                  {a.hostname}
                </Link>
                <span className="text-slate-500">
                  {a.cluster_name || "zonder cluster"}
                </span>
                <span className="font-mono text-xs text-slate-600 dark:text-slate-400">
                  {a.version || "?"} · protocol {a.protocol}
                </span>
                <StateBadge a={a} />
                {!a.online && <Badge>Niet verbonden</Badge>}
                {a.auto && (
                  <span className="text-xs text-slate-500">gaat vanzelf</span>
                )}
                {a.why && (
                  <span className="basis-full text-xs text-slate-500">
                    {a.why}
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}
        {manual && (
          <div className="space-y-2">
            <p>
              Agents onder protocol 23 werk je één keer met de hand bij. Daarna
              doen ze het zelf. Op elke node met &quot;Met de hand
              bijwerken&quot;:
            </p>
            <CopyBlock text={scriptCommand("--upgrade")} />
          </div>
        )}
        {start.data && <Started result={start.data} />}
        {start.isError && <StartError error={start.error} />}
        {isAdmin && (outside.length > 0 || prodClusters.size > 0) && (
          <div className="space-y-3">
            {prodClusters.size > 0 && (
              <label className="flex items-start gap-2">
                <input
                  type="checkbox"
                  className="mt-0.5"
                  checked={withProd}
                  onChange={(e) => setWithProd(e.target.checked)}
                />
                <span>
                  Ook productie ({prodNodes}{" "}
                  {prodNodes === 1 ? "node" : "nodes"} in {prodClusters.size}{" "}
                  {prodClusters.size === 1 ? "cluster" : "clusters"})
                  <span className="block text-slate-500">
                    Met één code uit de app. De prodclusters gaan één na het
                    andere, per cluster één node tegelijk; bij een fout stopt de
                    rest.
                  </span>
                </span>
              </label>
            )}
            <div className="flex flex-wrap gap-2">
              {total > 0 && (
                <Button
                  disabled={
                    start.isPending || (withProd && ov.prod_queue.length > 0)
                  }
                  onClick={() =>
                    withProd && prodNodes > 0
                      ? setBatch(true)
                      : start.mutate({ all: true })
                  }
                >
                  Agents bijwerken ({total})
                </Button>
              )}
              {[...prodClusters.values()].map((c) => (
                <Button
                  key={c.id}
                  variant="secondary"
                  onClick={() => setProd(c)}
                >
                  Alleen {c.name} (prod, {c.n})
                </Button>
              ))}
            </div>
          </div>
        )}
      </div>
      {prod && (
        <ProdUpgrade
          title={`Agents bijwerken in ${prod.name}`}
          body={{ cluster_id: prod.id }}
          onClose={() => setProd(null)}
        />
      )}
      {batch && (
        <ProdUpgrade
          title={`Agents bijwerken, ook productie (${total})`}
          body={{ all: true, include_prod: true }}
          intro={
            <p>
              {outside.length > 0 && (
                <>Buiten prod: {outside.length} meteen. </>
              )}
              Prod, één cluster na het andere:{" "}
              {[...prodClusters.values()]
                .map((c) => `${c.name} (${c.n})`)
                .join(", ")}
              .
            </p>
          }
          onClose={() => {
            setBatch(false);
            setWithProd(false);
          }}
        />
      )}
    </Card>
  );
}

// AgentUpgradeRow staat in de kaart Agent van een node.
export function AgentUpgradeRow({
  nodeId,
  isAdmin,
}: {
  nodeId: string;
  isAdmin: boolean;
}) {
  const q = useAgentUpgrades();
  const start = useStartAgentUpgrades();
  const [confirming, setConfirming] = useState(false);
  const a = q.data?.agents.find((x) => x.node_id === nodeId);
  if (!a) return null;
  const busy = q.data!.running.some(
    (r) => (r.cluster_id ?? null) === (a.cluster_id ?? null),
  );
  return (
    <div className="mt-4 space-y-2 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <StateBadge a={a} />
        {a.auto && (
          <span className="text-xs text-slate-500">
            wordt vanzelf bijgewerkt
          </span>
        )}
        {busy && (
          <span className="text-xs text-slate-500">
            er loopt een taak die agents bijwerkt
          </span>
        )}
      </div>
      {a.why && <p className="text-slate-600 dark:text-slate-400">{a.why}</p>}
      {a.state === "manual" && <CopyBlock text={scriptCommand("--upgrade")} />}
      {isAdmin && upgradable(a) && (
        <Button
          variant="secondary"
          disabled={start.isPending || busy}
          onClick={() =>
            a.cluster_environment === "prod"
              ? setConfirming(true)
              : start.mutate({ node_ids: [a.node_id] })
          }
        >
          Agent bijwerken
        </Button>
      )}
      {start.data && <Started result={start.data} />}
      {start.isError && <StartError error={start.error} />}
      {confirming && (
        <ProdUpgrade
          title={`Agent op ${a.hostname} bijwerken`}
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
  useAgentEndpoints();
  const clusters = useClusters();
  const setAuto = useSetAgentUpgradeAuto();
  const setCluster = useSetClusterAgentUpgradeAuto();
  const setProdAuto = useSetAgentUpgradeProdAuto();
  const totp = useProdTotp();
  const [asking, setAsking] = useState(false);
  const [code, setCode] = useState("");
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
              Na een update van ClusterForge werkt de server verouderde agents
              vanzelf bij, één node tegelijk per cluster. Een update die
              mislukte, probeert hij niet nog eens vanzelf.
            </span>
          </span>
        </label>
        <div className="space-y-2">
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              className="mt-0.5"
              checked={ov.prod_auto || asking}
              disabled={!isAdmin || !ov.auto || setProdAuto.isPending}
              onChange={(e) => {
                if (e.target.checked) {
                  setAsking(true);
                  setCode("");
                } else if (asking) {
                  setAsking(false);
                } else {
                  setProdAuto.mutate({ enabled: false });
                }
              }}
            />
            <span>
              Ook productie vanzelf bijwerken
              <span className="block text-slate-500">
                Ook de agents op prodclusters, maar één prodcluster tegelijk en
                per cluster één node tegelijk. Mislukt er één, dan staat het op
                prod stil tot je die node met de knop bijwerkt. Aanzetten vraagt
                een code uit de app.
              </span>
            </span>
          </label>
          {asking && !ov.prod_auto && (
            <div className="ml-6 space-y-2">
              {totp ? (
                <div className="flex flex-wrap items-end gap-2">
                  <TotpField
                    id="prod-auto-code"
                    value={code}
                    onChange={setCode}
                  />
                  <Button
                    variant="danger"
                    disabled={code.length !== 6 || setProdAuto.isPending}
                    onClick={() =>
                      setProdAuto.mutate(
                        { enabled: true, totp_code: code },
                        { onSuccess: () => setAsking(false) },
                      )
                    }
                  >
                    Aanzetten
                  </Button>
                </div>
              ) : (
                <NoTotp />
              )}
            </div>
          )}
          {ov.prod_halted && ov.prod_auto && (
            <Alert>
              Staat stil: bijwerken op {ov.halted_by} mislukte. Werk die node
              bij met de knop bij Nodes.
            </Alert>
          )}
          {setProdAuto.isError && <StartError error={setProdAuto.error} />}
        </div>
        {(clusters.data?.length ?? 0) > 0 && (
          <div className="space-y-1">
            <p className="font-medium">Per cluster</p>
            {!ov.auto && (
              <p className="text-slate-500">
                Geldt zodra vanzelf bijwerken aan staat; zet clusters die
                erbuiten moeten eerst uit.
              </p>
            )}
            {clusters.data!.map((c) => {
              const prod = c.environment === "prod";
              return (
                <label key={c.id} className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={
                      (!prod || ov.prod_auto) &&
                      !ov.off_cluster_ids.includes(c.id)
                    }
                    disabled={
                      !isAdmin ||
                      (prod && !ov.prod_auto) ||
                      setCluster.isPending
                    }
                    onChange={(e) =>
                      setCluster.mutate({
                        clusterId: c.id,
                        enabled: e.target.checked,
                      })
                    }
                  />
                  <span>
                    {c.name}
                    {prod && (
                      <span className="text-slate-500">
                        {ov.prod_auto
                          ? " · prod"
                          : ' · prod, alleen vanzelf met "Ook productie vanzelf bijwerken"'}
                      </span>
                    )}
                  </span>
                </label>
              );
            })}
          </div>
        )}
        {(setAuto.isError || setCluster.isError) && (
          <Alert>{(setAuto.error ?? setCluster.error)!.message}</Alert>
        )}
      </div>
    </Card>
  );
}
