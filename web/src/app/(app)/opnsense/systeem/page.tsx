"use client";

import { Suspense, useState } from "react";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Modal } from "@/components/Modal";
import { ChangeDialog, ClusterStrip, JobBanner, OpnGate } from "@/components/opnsense/bits";
import { Tile as Win, TileGrid } from "@/components/Tiles";
import { Alert, Badge, Button, Card, Field, Input, PageHeader, Select } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { readNode, roleLabel, useOpnSystem, type OpnCluster, type OpnLegacyEdit, type OpnSysIface, type OpnSysNode } from "@/lib/opnsense";
import type { Job } from "@/lib/proxmox";

export default function OpnSystemPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Interfaces en systeem"
        description="Interfaces toewijzen en hun adres, en de systeeminstellingen (hostnaam, domein, DNS-servers, tijdservers, tijdzone) van elke firewall. Die gelden per firewall en gaan niet mee met de sync naar de backup. Elke wijziging gaat met herstelpunt en terugdraaitimer: verlies je de verbinding, dan zet de firewall zelf de oude configuratie terug."
      />
      <Suspense>
        <OpnGate>{(c) => <System key={c.key} c={c} />}</OpnGate>
      </Suspense>
    </div>
  );
}

type Result = { kind: "success"; job: Job } | { kind: "error"; message: string } | null;
type Pending = { node: string; edits: OpnLegacyEdit[] } | null;

function System({ c }: { c: OpnCluster }) {
  const node = readNode(c);
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const q = useOpnSystem(admin ? node?.node_id : undefined);
  const [pick, setPick] = useState<string>("");
  const [pending, setPending] = useState<Pending>(null);
  const [result, setResult] = useState<Result>(null);
  if (!admin) return <Alert kind="info">Alleen een beheerder ziet en wijzigt interfaces en systeem.</Alert>;
  const nodes = q.data?.nodes ?? [];
  const n = nodes.find((x) => x.node_id === pick) ?? nodes[0];
  return (
    <div className="space-y-4">
      <ClusterStrip c={c} />
      <JobBanner result={result} />
      {q.isError && <Alert>{(q.error as Error).message}</Alert>}
      {nodes.length > 1 && (
        <div className="flex items-center gap-2 text-sm">
          <span className="text-slate-500">Firewall</span>
          <Select aria-label="Firewall" value={n?.node_id ?? ""} onChange={(e) => setPick(e.target.value)} className="w-auto">
            {nodes.map((x) => (
              <option key={x.node_id} value={x.node_id}>
                {x.hostname} ({roleLabel[x.role] ?? x.role})
              </option>
            ))}
          </Select>
        </div>
      )}
      {!q.data ? (
        <p className="text-sm text-slate-500">Laden…</p>
      ) : !n ? null : n.error ? (
        <Alert>{n.error}</Alert>
      ) : (
        <>
          {!n.legacy && (
            <Alert kind="info">De agent op {n.hostname} is te oud om dit te wijzigen (protocol 56 nodig): Nodes → Agents bijwerken.</Alert>
          )}
          <TileGrid>
            <Win>
              <SystemCard key={n.node_id} n={n} onChange={(edits) => setPending({ node: n.node_id, edits })} />
            </Win>
            <Win wide>
              <Interfaces key={n.node_id} n={n} onChange={(edits) => setPending({ node: n.node_id, edits })} />
            </Win>
          </TileGrid>
        </>
      )}
      {pending && node && (
        <ChangeDialog
          nodeId={node.node_id}
          calls={[]}
          legacy={pending.edits}
          node={pending.node}
          before={
            <ul className="list-disc pl-5 text-xs">
              {pending.edits.map((e, i) => (
                <li key={i}>
                  {e.section === "interfaces" ? `${e.key}: ` : ""}
                  {e.field} {e.delete ? "weghalen" : `= ${e.value || "(leeg)"}`}
                </li>
              ))}
            </ul>
          }
          onClose={() => setPending(null)}
          onDone={(j) => {
            setPending(null);
            setResult({ kind: "success", job: j });
          }}
        />
      )}
    </div>
  );
}

function SystemCard({ n, onChange }: { n: OpnSysNode; onChange: (e: OpnLegacyEdit[]) => void }) {
  const start = { hostname: n.host, domain: n.domain, dnsserver: n.dns.join(", "), timeservers: n.ntp ?? "", timezone: n.timezone ?? "" };
  const [v, setV] = useState(start);
  const edits: OpnLegacyEdit[] = (Object.keys(start) as (keyof typeof start)[])
    .filter((k) => v[k].trim() !== start[k])
    .map((k) => ({ section: "system", field: k, value: k === "dnsserver" ? v[k].replace(/\s+/g, "") : v[k].trim() }));
  const row = (k: keyof typeof start, label: string, hint?: string) => (
    <Field label={label} htmlFor={`sys-${k}`} hint={hint}>
      <Input id={`sys-${k}`} value={v[k]} disabled={!n.legacy} onChange={(e) => setV({ ...v, [k]: e.target.value })} />
    </Field>
  );
  return (
    <Card title="Systeem">
      <form
        className="space-y-3 text-sm"
        onSubmit={(e) => {
          e.preventDefault();
          onChange(edits);
        }}
      >
        {row("hostname", "Hostnaam")}
        {row("domain", "Domein")}
        {row("dnsserver", "DNS-servers", "Met komma's, zoals 9.9.9.9, 1.1.1.1")}
        {row("timeservers", "Tijdservers", "Met spaties, zoals 0.be.pool.ntp.org 1.be.pool.ntp.org")}
        {row("timezone", "Tijdzone", "Zoals Europe/Brussels")}
        <div className="flex justify-end">
          <Button type="submit" disabled={!n.legacy || edits.length === 0}>
            Wijzigen ({edits.length})
          </Button>
        </div>
      </form>
    </Card>
  );
}

type IfaceForm = { descr: string; enable: boolean; ipaddr: string; subnet: string; gateway: string; ipaddrv6: string; subnetv6: string };

function Interfaces({ n, onChange }: { n: OpnSysNode; onChange: (e: OpnLegacyEdit[]) => void }) {
  const [edit, setEdit] = useState<OpnSysIface | null>(null);
  const [assign, setAssign] = useState("");
  return (
    <Card title={`Interfaces · ${n.interfaces.length}`}>
      <div className="space-y-3">
        <div className="overflow-auto">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Interface</th>
                <th className={thClass}>Apparaat</th>
                <th className={thClass}>IPv4</th>
                <th className={thClass}>IPv6</th>
                <th className={thClass}>Gateway</th>
                <th className={thClass} />
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {n.interfaces.map((i) => (
                <tr key={i.key}>
                  <td className={tdClass}>
                    {i.descr || i.key.toUpperCase()} <span className="font-mono text-xs text-slate-500">{i.key}</span>{" "}
                    {!i.enabled && <Badge tone="slate">uit</Badge>}
                  </td>
                  <td className={`${tdClass} font-mono text-xs`}>{i.device}</td>
                  <td className={`${tdClass} font-mono text-xs`}>{i.ipaddr ? `${i.ipaddr}${i.subnet ? "/" + i.subnet : ""}` : "–"}</td>
                  <td className={`${tdClass} font-mono text-xs`}>
                    {i.ipaddrv6 ? `${i.ipaddrv6}${i.subnetv6 ? "/" + i.subnetv6 : ""}` : "–"}
                  </td>
                  <td className={`${tdClass} text-xs`}>{i.gateway || "–"}</td>
                  <td className={`${tdClass} text-right`}>
                    {n.legacy && (
                      <button type="button" className="text-xs text-slate-500 underline" onClick={() => setEdit(i)}>
                        wijzigen
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {n.legacy && n.free.length > 0 && (
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="text-slate-500">Vrij apparaat toewijzen als {n.next_key}:</span>
            <Select aria-label="Apparaat" value={assign} onChange={(e) => setAssign(e.target.value)} className="w-auto">
              <option value="">Kies…</option>
              {n.free.map((d) => (
                <option key={d} value={d}>
                  {d}
                </option>
              ))}
            </Select>
            <Button
              variant="secondary"
              disabled={!assign}
              onClick={() =>
                onChange([
                  { section: "interfaces", key: n.next_key, field: "if", value: assign },
                  { section: "interfaces", key: n.next_key, field: "descr", value: n.next_key.toUpperCase() },
                ])
              }
            >
              Toewijzen
            </Button>
            <span className="text-xs text-slate-500">Daarna geef je het een naam, adres en zet je het aan.</span>
          </div>
        )}
      </div>
      {edit && (
        <IfaceDialog
          i={edit}
          onClose={() => setEdit(null)}
          onSave={(edits) => {
            setEdit(null);
            onChange(edits);
          }}
        />
      )}
    </Card>
  );
}

function IfaceDialog({ i, onClose, onSave }: { i: OpnSysIface; onClose: () => void; onSave: (e: OpnLegacyEdit[]) => void }) {
  const start: IfaceForm = {
    descr: i.descr ?? "",
    enable: i.enabled,
    ipaddr: i.ipaddr ?? "",
    subnet: i.subnet ?? "",
    gateway: i.gateway ?? "",
    ipaddrv6: i.ipaddrv6 ?? "",
    subnetv6: i.subnetv6 ?? "",
  };
  const [v, setV] = useState(start);
  const edits: OpnLegacyEdit[] = [];
  for (const k of ["descr", "ipaddr", "subnet", "gateway", "ipaddrv6", "subnetv6"] as const) {
    const val = v[k].trim();
    if (val === start[k]) continue;
    edits.push(
      val === ""
        ? { section: "interfaces", key: i.key, field: k, delete: true }
        : { section: "interfaces", key: i.key, field: k, value: val },
    );
  }
  if (v.enable !== start.enable) {
    edits.push(
      v.enable
        ? { section: "interfaces", key: i.key, field: "enable", value: "1" }
        : { section: "interfaces", key: i.key, field: "enable", delete: true },
    );
  }
  const txt = (k: Exclude<keyof IfaceForm, "enable">, label: string, hint?: string) => (
    <Field label={label} htmlFor={`if-${k}`} hint={hint}>
      <Input id={`if-${k}`} value={v[k]} onChange={(e) => setV({ ...v, [k]: e.target.value })} />
    </Field>
  );
  const wan = i.key === "wan";
  return (
    <Modal title={`Interface ${i.descr || i.key} wijzigen`} onClose={onClose}>
      <div className="space-y-3 text-sm">
        {wan && <Alert kind="info">Dit is de WAN. Een fout adres neemt internet weg; de terugdraaitimer zet het dan terug.</Alert>}
        {txt("descr", "Naam")}
        <label className="flex items-center gap-2">
          <input type="checkbox" checked={v.enable} onChange={(e) => setV({ ...v, enable: e.target.checked })} /> Aan
        </label>
        <div className="grid grid-cols-2 gap-3">
          {txt("ipaddr", "IPv4-adres", "Een adres, dhcp of leeg")}
          {txt("subnet", "Prefix", "Zoals 24")}
          {txt("ipaddrv6", "IPv6-adres", "Een adres, dhcp6, slaac of leeg")}
          {txt("subnetv6", "IPv6-prefix", "Zoals 64")}
        </div>
        {txt("gateway", "Gateway (naam)", "Zoals WAN_DHCP; leeg voor geen")}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button disabled={edits.length === 0} onClick={() => onSave(edits)}>
            Verder ({edits.length})
          </Button>
        </div>
      </div>
    </Modal>
  );
}
