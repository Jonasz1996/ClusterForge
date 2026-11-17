"use client";

import { Suspense, useState } from "react";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Modal } from "@/components/Modal";
import { ago, ClusterStrip, JobBanner, OpnGate } from "@/components/opnsense/bits";
import { Tile as Win, TileGrid } from "@/components/Tiles";
import { Alert, Badge, Button, Card, Field, Input, PageHeader } from "@/components/ui";
import { useMe } from "@/lib/auth";
import {
  readNode,
  roleLabel,
  useOpnConfigctl,
  useOpnGet,
  useOpnKey,
  useOpnSync,
  type OpnCluster,
  type OpnFirewall,
  type OpnSearch,
} from "@/lib/opnsense";
import type { Job } from "@/lib/proxmox";

export default function OpnsensePage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Firewalls"
        description="Je OPNsense-firewalls in één blik: welke node MASTER is voor elke CARP-VIP, pfsync en de configsync naar de backup, interfaces, gateways en diensten. Alles via de ClusterForge-agent op elke firewall; elke wijziging gaat met een herstelpunt en een terugdraaitimer op de firewall zelf."
      />
      <Suspense>
        <OpnGate>{(c) => <Overview key={c.key} c={c} />}</OpnGate>
      </Suspense>
    </div>
  );
}

type Result = { kind: "success"; job: Job } | { kind: "error"; message: string } | null;

function Overview({ c }: { c: OpnCluster }) {
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const [result, setResult] = useState<Result>(null);
  const master = c.nodes.find((n) => n.node_id === c.master);
  const sync = useOpnSync(master?.node_id ?? c.nodes[0].node_id);
  const rn = readNode(c);
  return (
    <div className="space-y-6">
      <ClusterStrip c={c} />
      <JobBanner result={result} />
      {c.problems.length > 0 && (
        <Card title="Aandacht nodig">
          <ul className="space-y-1.5 text-sm">
            {c.problems.map((p) => (
              <li key={p.text} className="flex items-start gap-2">
                <Badge tone={p.level === "crit" ? "red" : "amber"}>{p.level === "crit" ? "Ernstig" : "Let op"}</Badge>
                <span>{p.text}</span>
              </li>
            ))}
          </ul>
        </Card>
      )}
      <TileGrid>
        <Win wide>
          <Card title="CARP">
            <CarpTable c={c} />
            {c.ha && admin && (
              <div className="mt-3 flex flex-wrap items-center gap-3 text-sm">
                <Button
                  variant="secondary"
                  disabled={!master || sync.isPending}
                  onClick={async () => {
                    try {
                      setResult({
                        kind: "success",
                        job: await sync.mutateAsync(),
                      });
                    } catch (e) {
                      setResult({
                        kind: "error",
                        message: e instanceof Error ? e.message : "Mislukt",
                      });
                    }
                  }}
                >
                  Sync nu
                </Button>
                <span className="text-slate-500">Zet de configuratie van de master op de backup en laadt daar de diensten opnieuw.</span>
              </div>
            )}
          </Card>
        </Win>
        {c.nodes.map((n) => (
          <Win key={n.node_id}>
            <NodeCard n={n} admin={admin} onJob={setResult} />
          </Win>
        ))}
        {rn && (
          <>
            <Win>
              <GatewaysCard id={rn.node_id} />
            </Win>
            <Win wide>
              <InterfacesCard n={rn} />
            </Win>
            <Win>
              <ServicesCard id={rn.node_id} />
            </Win>
          </>
        )}
      </TileGrid>
    </div>
  );
}

const stateTone = (s: string) => (s === "MASTER" ? "green" : s === "BACKUP" ? "blue" : s ? "red" : "slate");

function CarpTable({ c }: { c: OpnCluster }) {
  if (c.vips.length === 0) return <p className="text-sm text-slate-500">Geen CARP- of andere VIP&apos;s met een VHID.</p>;
  return (
    <div className="overflow-x-auto">
      <table className={tableClass}>
        <thead>
          <tr>
            <th className={thClass}>VIP</th>
            <th className={thClass}>Interface</th>
            <th className={thClass}>VHID</th>
            {c.nodes.map((n) => (
              <th key={n.node_id} className={thClass}>
                {n.hostname}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {c.vips.map((v) => (
            <tr key={`${v.address}/${v.vhid}`}>
              <td className={tdClass}>
                <span className="font-mono">{v.address}</span>
                {v.descr && <span className="block text-xs text-slate-500">{v.descr}</span>}
              </td>
              <td className={tdClass}>{v.interface}</td>
              <td className={tdClass}>{v.vhid}</td>
              {c.nodes.map((n) => (
                <td key={n.node_id} className={tdClass}>
                  <Badge tone={stateTone(v.states[n.hostname] ?? "")}>{v.states[n.hostname] ?? "–"}</Badge>
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Row({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <div className="flex gap-3 py-1">
      <dt className="w-32 shrink-0 text-slate-500">{k}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  );
}

function NodeCard({ n, admin, onJob }: { n: OpnFirewall; admin: boolean; onJob: (r: Result) => void }) {
  const [paste, setPaste] = useState(false);
  const f = n.facts;
  return (
    <Card
      title={
        <span className="inline-flex items-center gap-2">
          {n.hostname}
          <Badge tone={n.role === "master" ? "green" : n.role === "backup" ? "blue" : "slate"}>{roleLabel[n.role]}</Badge>
        </span>
      }
    >
      {n.why && (
        <div className="mb-3">
          <Alert>{n.why}</Alert>
        </div>
      )}
      <dl className="text-sm">
        <Row k="Adres">{n.address || "–"}</Row>
        <Row k="Versie">
          OPNsense {f.version}
          {f.freebsd ? ` · FreeBSD ${f.freebsd}` : ""}
        </Row>
        <Row k="Agent">
          {n.agent_version || "?"} (protocol {n.protocol}) · {n.online ? "online" : "offline"}
        </Row>
        <Row k="CARP">
          {f.carp.maintenance ? "onderhoudsmodus" : f.carp.allow ? "aan" : "uit"}
          {f.carp.demotion > 0 ? ` · demotion ${f.carp.demotion}` : ""}
        </Row>
        <Row k="pfsync">
          {f.ha.pfsync_interface ? (
            <>
              {f.ha.pfsync_interface}
              {f.ha.pfsync_peer ? ` → ${f.ha.pfsync_peer}` : ""} · {f.ha.pfsync_up ? "actief" : "geen synchronisatie"}
            </>
          ) : (
            "niet ingesteld"
          )}
        </Row>
        <Row k="Configsync">
          {f.ha.sync_to ? `naar ${f.ha.sync_to}${f.ha.sync_items?.length ? ` (${f.ha.sync_items.length} onderdelen)` : ""}` : "–"}
        </Row>
        <Row k="Laatste revisie">
          {f.config.time ? `${ago(f.config.time)}${f.config.description ? `: ${f.config.description}` : ""}` : "–"}
        </Row>
        <Row k="API">
          {f.api.ok ? (
            <>
              ok, gebruiker {f.api.user}
              {f.api.pasted ? " (geplakte sleutel)" : ""}
            </>
          ) : (
            <span className="text-red-700 dark:text-red-400">{f.api.error || "werkt niet"}</span>
          )}
        </Row>
      </dl>
      {admin && !f.api.ok && n.online && (
        <div className="mt-3 text-sm">
          <Button variant="secondary" onClick={() => setPaste(true)}>
            API-sleutel plakken
          </Button>
          <p className="mt-1 text-xs text-slate-500">
            Alleen nodig als de agent zelf geen API-gebruiker kan maken: System → Access → Users → een gebruiker met rechten → API keys.
          </p>
        </div>
      )}
      {paste && <PasteKey n={n} onClose={() => setPaste(false)} onJob={onJob} />}
    </Card>
  );
}

function PasteKey({ n, onClose, onJob }: { n: OpnFirewall; onClose: () => void; onJob: (r: Result) => void }) {
  const [key, setKey] = useState("");
  const [secret, setSecret] = useState("");
  const m = useOpnKey(n.node_id);
  return (
    <Modal title={`API-sleutel voor ${n.hostname}`} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={async (e) => {
          e.preventDefault();
          try {
            onJob({
              kind: "success",
              job: await m.mutateAsync({ key, secret }),
            });
            onClose();
          } catch (err) {
            onJob({
              kind: "error",
              message: err instanceof Error ? err.message : "Mislukt",
            });
          }
        }}
      >
        <p className="text-sm text-slate-600 dark:text-slate-300">
          De sleutel gaat versleuteld naar de agent en blijft alleen op de firewall. ClusterForge bewaart hem nergens.
        </p>
        <Field label="Key" htmlFor="opn-key">
          <Input id="opn-key" value={key} onChange={(e) => setKey(e.target.value)} autoComplete="off" />
        </Field>
        <Field label="Secret" htmlFor="opn-secret">
          <Input id="opn-secret" type="password" value={secret} onChange={(e) => setSecret(e.target.value)} autoComplete="off" />
        </Field>
        <div className="flex gap-2">
          <Button type="submit" disabled={!key || !secret || m.isPending}>
            Testen en bewaren
          </Button>
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
        </div>
      </form>
    </Modal>
  );
}

type Gateway = {
  name: string;
  address?: string;
  status?: string;
  status_translated?: string;
  delay?: string;
  loss?: string;
  stddev?: string;
};

function GatewaysCard({ id }: { id: string }) {
  const q = useOpnConfigctl<Gateway[] | Record<string, Gateway>>(id, "interface gateways status");
  const list: Gateway[] = Array.isArray(q.data)
    ? q.data
    : q.data
      ? Object.entries(q.data).map(([k, g]) => ({ ...g, name: g.name || k }))
      : [];
  const down = list.filter((g) => g.status === "down" || g.status === "force_down").length;
  return (
    <Card title={`Gateways${down ? ` · ${down} down` : ""}`}>
      {q.isError ? (
        <p className="text-sm text-red-700 dark:text-red-400">{(q.error as Error).message}</p>
      ) : (
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Gateway</th>
              <th className={thClass}>Status</th>
              <th className={thClass}>Vertraging</th>
              <th className={thClass}>Verlies</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {list.map((g) => (
              <tr key={g.name}>
                <td className={tdClass}>
                  {g.name}
                  {g.address && <span className="block font-mono text-xs text-slate-500">{g.address}</span>}
                </td>
                <td className={tdClass}>
                  <Badge tone={g.status === "none" ? "green" : g.status === "down" || g.status === "force_down" ? "red" : "amber"}>
                    {g.status_translated || (g.status === "none" ? "Online" : g.status)}
                  </Badge>
                </td>
                <td className={tdClass}>{g.delay ?? "–"}</td>
                <td className={tdClass}>{g.loss ?? "–"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  );
}

function InterfacesCard({ n }: { n: OpnFirewall }) {
  const list = n.facts.interfaces ?? [];
  return (
    <Card title="Interfaces">
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Interface</th>
              <th className={thClass}>Apparaat</th>
              <th className={thClass}>Adressen</th>
              <th className={thClass}>Link</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {list.map((i) => (
              <tr key={i.name}>
                <td className={tdClass}>
                  {i.descr || i.name}
                  {i.descr && <span className="block text-xs text-slate-500">{i.name}</span>}
                </td>
                <td className={`${tdClass} font-mono`}>{i.device}</td>
                <td className={`${tdClass} font-mono text-xs`}>{(i.addresses ?? []).join(", ") || "–"}</td>
                <td className={tdClass}>
                  {!i.enabled ? <Badge>uit</Badge> : <Badge tone={i.up ? "green" : "red"}>{i.up ? "up" : "down"}</Badge>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

type Service = {
  id: string;
  name: string;
  description: string;
  running: number;
  locked?: number;
};

function ServicesCard({ id }: { id: string }) {
  const q = useOpnGet<OpnSearch<Service>>(id, "core/service/search");
  const rows = q.data?.rows ?? [];
  const stopped = rows.filter((s) => !s.running);
  return (
    <Card title={`Diensten${stopped.length ? ` · ${stopped.length} gestopt` : ""}`}>
      {q.isError ? (
        <p className="text-sm text-red-700 dark:text-red-400">{(q.error as Error).message}</p>
      ) : (
        <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
          {rows.map((s) => (
            <li key={s.id} className="flex items-center justify-between gap-2 py-1.5">
              <span>
                {s.description || s.name}
                <span className="ml-2 text-xs text-slate-500">{s.name}</span>
              </span>
              <Badge tone={s.running ? "green" : "red"}>{s.running ? "actief" : "gestopt"}</Badge>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
