"use client";

import Link from "next/link";
import { useState } from "react";
import { Modal } from "@/components/Modal";
import { ago, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, Select, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import {
  classLabel,
  firewallState,
  modeLabel,
  originLabel,
  ruleText,
  useApplyFirewallNode,
  useDisableFirewallNode,
  useFirewallNode,
  useRefreshFirewallNode,
  useRollbackFirewallNode,
  useSetFirewallNodes,
  type FirewallEntry,
  type FirewallNode,
} from "@/lib/firewall";

export const timeFmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "short", timeStyle: "short" });
export const hourFmt = new Intl.DateTimeFormat("nl-BE", { hour: "2-digit", minute: "2-digit" });

export function errText(e: unknown) {
  return e instanceof ApiError ? e.message : "Er ging iets mis.";
}

function fromText(e: FirewallEntry) {
  if (e.state === "docker") return "Docker zet ze zelf open";
  if (e.state !== "open") return e.note || "dicht";
  if (e.from.length === 1 && e.from[0] === "any") return "iedereen";
  return e.from.join(", ");
}

export function EntryState({ e }: { e: FirewallEntry }) {
  switch (e.state) {
    case "open":
      return <Badge tone={e.class === "any" ? "amber" : "green"}>open · {classLabel[e.class] ?? e.class}</Badge>;
    case "docker":
      return <Badge tone="blue">via Docker</Badge>;
    case "ignored":
      return <Badge>ufw laat toe</Badge>;
  }
  return <Badge>dicht</Badge>;
}

/** De poorten van het plan en het verschil met de node. */
export function PlanView({ n }: { n: FirewallNode }) {
  const p = n.plan;
  if (!p) return <p className="text-sm text-slate-500">Nog geen plan: de stand van de node is nog niet opgehaald.</p>;
  const d = n.diff;
  return (
    <div className="space-y-4">
      {p.warnings.length > 0 && (
        <Alert kind="info">
          <ul className="list-disc pl-4">
            {p.warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        </Alert>
      )}
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Poort</th>
              <th className={thClass}>Software</th>
              <th className={thClass}>Stand</th>
              <th className={thClass}>Voor wie</th>
              <th className={thClass}>Waarom</th>
            </tr>
          </thead>
          <tbody>
            {p.entries
              .filter((e) => e.state !== "ignored")
              .map((e) => (
                <tr key={`${e.proto}/${e.port}/${e.class}`}>
                  <td className={cx(tdClass, "font-mono text-xs")}>{e.port ? `${e.port}/${e.proto}` : e.proto}</td>
                  <td className={tdClass}>
                    {e.software || <span className="text-slate-500">{e.process || "?"}</span>}
                    {e.label && <span className="text-slate-500"> · {e.label}</span>}
                    {e.unknown && (
                      <span className="ml-1">
                        <Badge tone="amber">onbekend</Badge>
                      </span>
                    )}
                  </td>
                  <td className={tdClass}>
                    <EntryState e={e} />
                  </td>
                  <td className={cx(tdClass, "max-w-xs break-words font-mono text-xs")}>{fromText(e)}</td>
                  <td className={cx(tdClass, "text-xs text-slate-500")}>
                    {originLabel[e.origin] ?? e.origin}
                    {e.note && e.state === "open" ? ` · ${e.note}` : ""}
                  </td>
                </tr>
              ))}
          </tbody>
        </table>
      </div>
      {d && (
        <div>
          <h3 className="mb-1 text-sm font-semibold">Verschil met de node</h3>
          {d.add.length + d.adopt.length + d.remove.length === 0 ? (
            <p className="text-sm text-slate-500">
              De regels op de node kloppen{n.status?.active ? "" : " (maar ufw staat uit)"}. {d.keep.length} regels van ClusterForge
              {d.foreign.length > 0 && `, ${d.foreign.length} andere blijven staan`}.
            </p>
          ) : (
            <ul className="space-y-0.5 font-mono text-xs">
              {d.add.map((r) => (
                <li key={"a" + ruleText(r)} className="text-emerald-700 dark:text-emerald-400">
                  + {ruleText(r)}
                </li>
              ))}
              {d.adopt.map((r) => (
                <li key={"o" + ruleText(r)} className="text-sky-700 dark:text-sky-300">
                  ~ {ruleText(r)} (overnemen)
                </li>
              ))}
              {d.remove.map((r) => (
                <li key={"r" + ruleText(r)} className="text-red-700 dark:text-red-400">
                  − {ruleText(r)}
                  {!r.comment?.startsWith("cf:") && " (niet van ClusterForge; Strikt staat aan)"}
                </li>
              ))}
            </ul>
          )}
          {n.status && !n.status.active && (
            <p className="mt-1 text-xs text-slate-500">
              Bij toepassen gaat ufw aan: inkomend dicht behalve bovenstaande poorten, uitgaand open.
              {!n.status.installed && " ufw wordt eerst geïnstalleerd."}
            </p>
          )}
          {d.foreign.length > 0 && (
            <p className="mt-1 text-xs text-slate-500">Blijven staan (niet van ClusterForge): {d.foreign.map(ruleText).join("; ")}</p>
          )}
        </div>
      )}
    </div>
  );
}

/** Knoppen voor een node; gedeeld door de pagina en de kaart. */
export function NodeActions({ n, onMsg, compact }: { n: FirewallNode; onMsg: (m: string) => void; compact?: boolean }) {
  const set = useSetFirewallNodes();
  const refresh = useRefreshFirewallNode();
  const apply = useApplyFirewallNode();
  const rollback = useRollbackFirewallNode();
  const disable = useDisableFirewallNode();
  const [confirmDisable, setConfirmDisable] = useState(false);
  const onErr = (e: unknown) => onMsg(errText(e));
  const busy = !n.online || n.apply_running;
  return (
    <span className="flex flex-wrap items-center gap-2">
      <Select
        aria-label="Stand"
        value={n.mode}
        disabled={!n.supported || set.isPending}
        onChange={(e) =>
          set.mutate(
            { node_ids: [n.node_id], mode: e.target.value as "off" | "preview" | "managed" },
            {
              onSuccess: (r) =>
                onMsg(
                  (r as { skipped: string[] }).skipped.join("; ") ||
                    (e.target.value === "managed" ? "Beheerd: het plan wordt zo toegepast, met terugdraaitimer." : "Stand gewijzigd."),
                ),
              onError: onErr,
            },
          )
        }
        className="w-auto"
      >
        <option value="off">uit</option>
        <option value="preview">voorbeeld</option>
        <option value="managed">beheerd</option>
      </Select>
      {n.mode !== "off" && (
        <>
          <Button variant="secondary" disabled={refresh.isPending || busy} onClick={() => refresh.mutate(n.node_id, { onError: onErr })}>
            {refresh.isPending ? "Ophalen…" : "Voorbeeld vernieuwen"}
          </Button>
          <Button
            disabled={apply.isPending || busy || !n.plan}
            onClick={() => apply.mutate(n.node_id, { onSuccess: () => onMsg("Toepassen gestart; volg het in de taak."), onError: onErr })}
          >
            Toepassen
          </Button>
          {n.status?.pending && (
            <Button variant="danger" disabled={rollback.isPending} onClick={() => rollback.mutate(n.node_id, { onSuccess: () => onMsg("Teruggedraaid."), onError: onErr })}>
              Terugdraaien
            </Button>
          )}
          {!compact && n.status?.active && (
            <Button variant="secondary" disabled={disable.isPending || busy} onClick={() => setConfirmDisable(true)}>
              ufw uitschakelen
            </Button>
          )}
        </>
      )}
      {n.apply_job_id && (
        <Link href={`/taken/detail?id=${n.apply_job_id}`} className="text-sm text-brand-700 hover:underline dark:text-sky-300">
          Laatste taak
        </Link>
      )}
      {confirmDisable && (
        <Modal title={`ufw uitschakelen op ${n.hostname}?`} onClose={() => setConfirmDisable(false)}>
          <p className="text-sm">
            Alle poorten staan dan open. De regels blijven bewaard; een beheerde node gaat naar de stand voorbeeld, zodat ClusterForge ufw niet meteen weer aanzet.
          </p>
          <div className="mt-4 flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setConfirmDisable(false)}>
              Annuleren
            </Button>
            <Button
              variant="danger"
              onClick={() =>
                disable.mutate(n.node_id, {
                  onSuccess: () => {
                    setConfirmDisable(false);
                    onMsg("ufw is uitgeschakeld.");
                  },
                  onError: (e) => {
                    setConfirmDisable(false);
                    onErr(e);
                  },
                })
              }
            >
              Uitschakelen
            </Button>
          </div>
        </Modal>
      )}
    </span>
  );
}

/** Kaart Firewall op de pagina van een node (fase 16). */
export function FirewallNodeCard({ nodeId, isAdmin }: { nodeId: string; isAdmin: boolean }) {
  const d = useFirewallNode(nodeId);
  const [msg, setMsg] = useState<string | null>(null);
  const [show, setShow] = useState(false);
  if (!d.data) return null;
  const n = d.data.node;
  const st = firewallState(n);
  const open = n.plan?.entries.filter((e) => e.state === "open") ?? [];
  const blocked = d.data.traffic;
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span className="flex items-center gap-2">
            Firewall <Badge tone={st.tone}>{st.label}</Badge>
            {n.status?.version && <span className="text-xs font-normal text-slate-500">ufw {n.status.version}</span>}
          </span>
          <span className="flex flex-wrap items-center gap-2">
            {isAdmin && <NodeActions n={n} onMsg={setMsg} compact />}
            <Link href={`/firewall?tab=verkeer&node_id=${n.node_id}`} className="text-sm text-brand-700 hover:underline dark:text-sky-300">
              Verkeer
            </Link>
          </span>
        </span>
      }
    >
      <div className="space-y-3 text-sm">
        {msg && <Alert kind="info">{msg}</Alert>}
        {n.mode === "off" ? (
          <p className="text-slate-500">ClusterForge beheert de firewall van deze node niet. Kies de stand voorbeeld om te zien wat er open en dicht zou gaan.</p>
        ) : (
          <>
            <p>
              <span className="font-medium">{open.length}</span> poorten open
              {n.plan && (
                <>
                  : <span className="font-mono text-xs">{open.map((e) => (e.port ? `${e.port}/${e.proto}` : e.proto)).join(", ")}</span>
                </>
              )}
              . Stand: {modeLabel[n.mode]}
              {n.applied_at && `, laatst toegepast ${ago(n.applied_at)}`}.
            </p>
            {n.apply_error && <Alert>{n.apply_error}</Alert>}
            {n.diff && n.diff.add.length + n.diff.adopt.length + n.diff.remove.length > 0 && (
              <p className="text-amber-700 dark:text-amber-400">
                Verschil met de node: {n.diff.add.length} erbij, {n.diff.remove.length} weg{n.diff.adopt.length > 0 && `, ${n.diff.adopt.length} over te nemen`}.
              </p>
            )}
            <p>
              Pogingen naar dichte poorten (24 u): <span className="font-medium tabular-nums">{n.blocked_24h}</span>
              {blocked.length > 0 &&
                ` — meest: ${blocked
                  .slice(0, 3)
                  .map((t) => `${t.src} → ${t.port || t.proto}`)
                  .join(", ")}`}
            </p>
            <button className="text-brand-700 hover:underline dark:text-sky-300" onClick={() => setShow(true)}>
              Plan en verschil bekijken
            </button>
          </>
        )}
      </div>
      {show && (
        <Modal title={`Firewall van ${n.hostname}`} onClose={() => setShow(false)} wide>
          <PlanView n={n} />
        </Modal>
      )}
    </Card>
  );
}
