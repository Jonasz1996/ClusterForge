"use client";

import { useRouter } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { Findings } from "@/components/databases/Findings";
import { formatBytes, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Modal } from "@/components/Modal";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { Alert, Badge, Button, Input, Select } from "@/components/ui";
import {
  useChangeSettings,
  useJoinComponent,
  useJoinPlan,
  useSettingsPlan,
  useUpgradeComponent,
  useUpgradePlan,
  type ClusterComponent,
  type ComponentSettingParam,
} from "@/lib/components";
import type { ClusterDetail } from "@/lib/inventory";

const pct = (n: number) => `${Math.round(n)}%`;

function useDebounced<T>(value: T, ms: number) {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return v;
}

// changed zijn de instellingen die de gebruiker veranderde, zoals de API ze
// wil: een getal als getal, een grootte als tekst.
function changed(params: ComponentSettingParam[], values: Record<string, string>) {
  const out: Record<string, unknown> = {};
  for (const p of params) {
    const v = (values[p.name] ?? p.value).trim();
    if (v === p.value) continue;
    out[p.name] = p.type === "int" && /^\d+$/.test(v) ? Number(v) : v;
  }
  return out;
}

// SettingsDialog wijzigt de instellingen van een database: het plan zegt
// bij elke wijziging wat er gebeurt, in welke volgorde en of het geheugen
// van de nodes het aankan.
export function SettingsDialog({ c, comp, onClose }: { c: ClusterDetail; comp: ClusterComponent; onClose: () => void }) {
  const router = useRouter();
  const totp = useProdTotp();
  const [confirm, setConfirm] = useState("");
  const [values, setValues] = useState<Record<string, string>>({});
  const base = useSettingsPlan(c.id, comp.id, {});
  const params = useMemo(() => base.data?.params ?? [], [base.data]);
  const settings = useDebounced(
    useMemo(() => changed(params, values), [params, values]),
    400,
  );
  const plan = useSettingsPlan(c.id, comp.id, settings);
  const change = useChangeSettings(c.id);
  const prod = c.environment === "prod";
  const pg = comp.engine === "postgresql";
  const p = plan.data;
  // Het plan hoort bij wat er nu in de velden staat.
  const current = JSON.stringify(settings) === JSON.stringify(changed(params, values));
  return (
    <Modal title={`Instellingen van ${comp.title}`} onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        <p>
          {pg
            ? "Nieuwe instellingen worden een nieuwe revisie. max_connections komt in etcd, waar Patroni het voor het hele cluster bewaart; daarna herstart PostgreSQL node voor node, de replica's eerst en de primary als laatste, zonder failover."
            : "Nieuwe instellingen worden een nieuwe revisie. ClusterForge schrijft ze op elke node en herstart MariaDB node voor node, de node met het VIP als laatste; na elke node wacht het tot hij weer Synced is."}
        </p>
        <QueryState q={base}>
          <div className="grid gap-4 sm:grid-cols-2">
            {params.map((x) => (
              <div key={x.name}>
                <label htmlFor={`s-${x.name}`} className="mb-1 block font-medium text-slate-700 dark:text-slate-300">
                  {x.label}
                </label>
                <Input
                  id={`s-${x.name}`}
                  type={x.type === "int" ? "number" : "text"}
                  min={x.type === "int" ? x.min || undefined : undefined}
                  max={x.type === "int" ? x.max || undefined : undefined}
                  value={values[x.name] ?? x.value}
                  onChange={(e) => setValues((v) => ({ ...v, [x.name]: e.target.value }))}
                />
                <span className="mt-1 block text-xs text-slate-500">
                  Nu {x.value}
                  {x.min && x.max && `; van ${x.min} tot ${x.max}`}. {x.help}
                </span>
              </div>
            ))}
          </div>
        </QueryState>
        {plan.isError && Object.keys(settings).length > 0 && <Alert>{plan.error.message}</Alert>}
        {p && !plan.isError && p.changes.length > 0 && (
          <>
            <div>
              <p className="font-medium">Wat verandert</p>
              <ul className="list-disc pl-5">
                {p.changes.map((ch) => (
                  <li key={ch.name}>
                    {ch.label}: {ch.from} wordt {ch.to}
                  </li>
                ))}
              </ul>
            </div>
            <div className="overflow-x-auto">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Volgorde</th>
                    <th className={thClass}>Nu</th>
                    <th className={thClass}>Geheugen</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {p.nodes.map((n, i) => (
                    <tr key={n.node_id}>
                      <td className={tdClass}>
                        <span className="font-medium">
                          {i + 1}. {n.hostname}
                        </span>{" "}
                        {n.writer && <Badge tone="blue">{pg ? "primary" : "VIP"}</Badge>}
                      </td>
                      <td className={tdClass}>
                        <span className={n.healthy ? "" : "text-amber-700 dark:text-amber-300"}>{n.state}</span>
                      </td>
                      <td className={tdClass}>
                        {n.memory_bytes > 0 ? (
                          <>
                            {formatBytes(n.memory_bytes)}
                            {n.memory_used_pct !== null && `, tot ${pct(n.memory_used_pct)} gebruikt`}
                            {n.free_after_pct !== null && (
                              <span
                                className={`block ${n.free_after_pct < 15 ? "text-amber-700 dark:text-amber-300" : "text-slate-500"}`}
                              >
                                daarna {pct(Math.max(0, n.free_after_pct))} vrij
                              </span>
                            )}
                          </>
                        ) : (
                          <span className="text-slate-500">onbekend</span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <ul className="list-disc space-y-1 pl-5">
              {p.effects.map((e) => (
                <li key={e}>{e}</li>
              ))}
            </ul>
          </>
        )}
        {p && !plan.isError && p.findings.length > 0 && Object.keys(settings).length > 0 && (
          <Findings list={p.findings.filter((f) => f.code !== "no_changes")} />
        )}
        {prod && <ProdConfirm slug={c.slug} value={confirm} onChange={setConfirm} />}
        {change.isError && <Alert>{change.error.message}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            disabled={
              !p ||
              !current ||
              plan.isFetching ||
              plan.isError ||
              p.blocked ||
              change.isPending ||
              (prod && !prodConfirmed(c.slug, confirm, totp))
            }
            onClick={async () => {
              if (!p) return;
              const j = await change
                .mutateAsync({ id: comp.id, settings, hash: p.hash, confirm: prod ? confirm : undefined })
                .catch(() => null);
              if (j) router.push(`/taken/detail?id=${j.id}`);
            }}
          >
            {change.isPending ? "Bezig…" : "Wijzigen"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

// UpgradeDialog werkt een database bij naar een kleinere versie, node voor
// node met de schrijver als laatste.
export function UpgradeDialog({ c, comp, onClose }: { c: ClusterDetail; comp: ClusterComponent; onClose: () => void }) {
  const plan = useUpgradePlan(c.id, comp.id);
  const upgrade = useUpgradeComponent(c.id);
  const router = useRouter();
  const totp = useProdTotp();
  const [confirm, setConfirm] = useState("");
  const prod = c.environment === "prod";
  const pg = comp.engine === "postgresql";
  const p = plan.data;
  return (
    <Modal title={`${comp.title} bijwerken`} onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        <QueryState q={plan}>
          {p && (
            <>
              <ul className="list-disc space-y-1 pl-5">
                {p.effects.map((e) => (
                  <li key={e}>{e}</li>
                ))}
              </ul>
              <div className="overflow-x-auto">
                <table className={tableClass}>
                  <thead>
                    <tr>
                      <th className={thClass}>Volgorde</th>
                      <th className={thClass}>Versie</th>
                      <th className={thClass}>Nieuwere pakketten</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                    {p.nodes.map((n, i) => (
                      <tr key={n.node_id}>
                        <td className={tdClass}>
                          <span className="font-medium">
                            {n.skip ? "–" : `${i + 1}.`} {n.hostname}
                          </span>{" "}
                          {n.writer && <Badge tone="blue">{pg ? "primary" : "VIP"}</Badge>}
                          {n.role === "arbiter" && <Badge tone="slate">scheidsrechter</Badge>}
                          <span className={`block ${n.healthy ? "text-slate-500" : "text-amber-700 dark:text-amber-300"}`}>
                            {n.state}
                          </span>
                        </td>
                        <td className={tdClass}>{n.version || <span className="text-slate-500">–</span>}</td>
                        <td className={tdClass}>
                          {n.skip ? (
                            <span className="text-slate-500">niets bij te werken</span>
                          ) : n.packages === null ? (
                            <span className="text-slate-500">onbekend</span>
                          ) : n.packages.length === 0 ? (
                            <span className="text-slate-500">geen, volgens de laatste apt update</span>
                          ) : (
                            <span className="font-mono text-xs">{n.packages.join(", ")}</span>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {p.findings.length > 0 && <Findings list={p.findings} />}
              {prod && <ProdConfirm slug={c.slug} value={confirm} onChange={setConfirm} />}
            </>
          )}
        </QueryState>
        {upgrade.isError && <Alert>{upgrade.error.message}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            disabled={!p || p.blocked || upgrade.isPending || (prod && !prodConfirmed(c.slug, confirm, totp))}
            onClick={async () => {
              if (!p) return;
              const j = await upgrade
                .mutateAsync({ id: comp.id, hash: p.hash, confirm: prod ? confirm : undefined })
                .catch(() => null);
              if (j) router.push(`/taken/detail?id=${j.id}`);
            }}
          >
            {upgrade.isPending ? "Bezig…" : "Bijwerken"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

// JoinDialog voegt een node van het cluster toe aan de database, ook een CT
// die Jonas zelf maakte. De andere nodes herstarten daarbij niet.
export function JoinDialog({ c, comp, onClose }: { c: ClusterDetail; comp: ClusterComponent; onClose: () => void }) {
  const router = useRouter();
  const totp = useProdTotp();
  const [confirm, setConfirm] = useState("");
  const [nodeId, setNodeId] = useState("");
  const plan = useJoinPlan(c.id, comp.id, nodeId);
  const join = useJoinComponent(c.id);
  const prod = c.environment === "prod";
  const pg = comp.engine === "postgresql";
  const p = plan.data;
  const n = p?.node ?? null;
  const findings = p?.findings.filter((f) => f.code !== "choose") ?? [];
  return (
    <Modal title={`Node toevoegen aan ${comp.title}`} onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        <p>
          {pg
            ? "De nieuwe node wordt eerst lid van etcd en daarna een replica die alle data van de primary kopieert. Patroni op de andere nodes herlaadt alleen; PostgreSQL herstart daar niet."
            : "De nieuwe node krijgt MariaDB en haalt alle data bij een node die meedoet. De andere nodes krijgen hem in hun configuratie, maar MariaDB herstart daar niet."}{" "}
          Een node die je zelf maakte, zoals een CT, staat hier zodra de agent erop draait.
        </p>
        <QueryState q={plan}>
          {p && (
            <>
              {p.candidates.length > 0 && (
                <div>
                  <label htmlFor="join-node" className="mb-1 block font-medium text-slate-700 dark:text-slate-300">
                    Node
                  </label>
                  <Select id="join-node" value={nodeId} onChange={(e) => setNodeId(e.target.value)}>
                    <option value="">Kies een node</option>
                    {p.candidates.map((x) => (
                      <option key={x.node_id} value={x.node_id}>
                        {x.hostname}
                      </option>
                    ))}
                  </Select>
                </div>
              )}
              {n && (
                <div className="rounded border border-slate-200 p-3 dark:border-slate-700">
                  <p className="font-medium">
                    {n.hostname} <span className="font-normal text-slate-500">{n.address}</span>
                  </p>
                  <p className="text-slate-600 dark:text-slate-400">
                    {[
                      n.os,
                      n.pve_type === "lxc" ? `CT ${n.vmid}` : n.pve_type === "qemu" ? `VM ${n.vmid}` : "",
                      n.memory_bytes > 0
                        ? `${formatBytes(n.memory_bytes)} geheugen, de database vraagt ${formatBytes(n.need_bytes)}`
                        : "",
                      n.disk_free_bytes !== null ? `${formatBytes(n.disk_free_bytes)} vrij op ${n.disk_mount}` : "",
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </p>
                  <details className="mt-2">
                    <summary className="cursor-pointer text-slate-600 dark:text-slate-400">
                      Stappen op {n.hostname} ({n.steps.length})
                    </summary>
                    <ol className="mt-1 list-decimal pl-5">
                      {n.steps.map((s, i) => (
                        <li key={i}>{s}</li>
                      ))}
                    </ol>
                  </details>
                </div>
              )}
              {n && (
                <div>
                  <p className="font-medium text-slate-700 dark:text-slate-300">Wat er op de andere nodes verandert</p>
                  <ul className="divide-y divide-slate-100 dark:divide-slate-800">
                    {p.members.map((m) => (
                      <li key={m.node_id} className="py-2 sm:grid sm:grid-cols-[12rem_1fr] sm:gap-4">
                        <div>
                          <span className="font-medium">{m.hostname}</span>{" "}
                          {m.writer && <Badge tone="blue">{pg ? "primary" : "VIP"}</Badge>}
                          {m.role === "arbiter" && <Badge tone="slate">scheidsrechter</Badge>}
                          <span className={`block ${m.healthy ? "text-slate-500" : "text-amber-700 dark:text-amber-300"}`}>
                            {m.state}
                          </span>
                        </div>
                        {m.changes.length === 0 ? (
                          <p className="text-slate-500">niets</p>
                        ) : (
                          <ul className="mt-1 [overflow-wrap:anywhere] sm:mt-0">
                            {m.changes.map((x) => (
                              <li key={x}>{x}</li>
                            ))}
                          </ul>
                        )}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
              {n && (
                <ul className="list-disc space-y-1 pl-5">
                  {p.effects.map((e) => (
                    <li key={e}>{e}</li>
                  ))}
                </ul>
              )}
              {findings.length > 0 && <Findings list={findings} />}
              {n && prod && <ProdConfirm slug={c.slug} value={confirm} onChange={setConfirm} />}
            </>
          )}
        </QueryState>
        {join.isError && <Alert>{join.error.message}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            disabled={
              !p || !n || plan.isFetching || p.blocked || join.isPending || (prod && !prodConfirmed(c.slug, confirm, totp))
            }
            onClick={async () => {
              if (!p || !n) return;
              const j = await join
                .mutateAsync({ id: comp.id, nodeId: n.node_id, hash: p.hash, confirm: prod ? confirm : undefined })
                .catch(() => null);
              if (j) router.push(`/taken/detail?id=${j.id}`);
            }}
          >
            {join.isPending ? "Bezig…" : "Toevoegen"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
