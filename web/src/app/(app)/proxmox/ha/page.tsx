"use client";

import Link from "next/link";
import { Suspense, useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ActionMenu, DatacenterGate, useCallRunner } from "@/components/pve/bits";
import { Check, FormModal } from "@/components/pve/guest/common";
import { Alert, Badge, Button, Card, cx, Field, Input, Select } from "@/components/ui";
import { PageHeader } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import {
  guestHref,
  guestLabel,
  usePveGet,
  type Datacenter,
  type HaGroup,
  type HaResource,
  type HaRule,
  type HaStatusEntry,
  type PveResource,
} from "@/lib/pve";

// High availability van Proxmox (fase 26 M4): status, welke gasten onder HA
// staan, en de regels (PVE 9) of groepen (PVE 8) die zeggen waar ze mogen
// draaien.

export default function HaPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="HA"
        description="Welke gasten Proxmox vanzelf herstart of verhuist als een host uitvalt, en op welke hosts ze mogen draaien."
      />
      <Suspense>
        <DatacenterGate>{(dc) => (dc.standalone ? <Alert kind="info">HA kan alleen in een Proxmox-cluster met minstens twee hosts (en quorum).</Alert> : <HaView key={dc.id} dc={dc} />)}</DatacenterGate>
      </Suspense>
    </div>
  );
}

const stateTone = (s?: string) =>
  s === "started" ? ("green" as const) : s === "error" || s === "fence" ? ("red" as const) : s === "stopped" || s === "disabled" || s === "ignored" ? ("slate" as const) : ("amber" as const);

const stateWord: Record<string, string> = {
  started: "draait",
  stopped: "gestopt",
  disabled: "uit",
  ignored: "genegeerd",
  error: "fout",
  fence: "fencing",
  migrate: "verhuist",
  relocate: "verplaatst",
  freeze: "bevroren",
  recovery: "herstelt",
  request_stop: "stopt",
  starting: "start",
};

function HaView({ dc }: { dc: Datacenter }) {
  const isAdmin = useIsAdmin();
  const status = usePveGet<HaStatusEntry[]>(dc.id, "/cluster/ha/status/current", { refetch: 10_000 });
  const resources = usePveGet<HaResource[]>(dc.id, "/cluster/ha/resources", { refetch: 15_000 });
  const guests = usePveGet<PveResource[]>(dc.id, "/cluster/resources", { params: { type: "vm" }, refetch: 15_000 });
  const rules = usePveGet<HaRule[]>(dc.id, "/cluster/ha/rules", { refetch: 60_000 });
  const groups = usePveGet<HaGroup[]>(dc.id, "/cluster/ha/groups", { refetch: 60_000, enabled: rules.isError });
  const { run, pending, banner } = useCallRunner(dc.id);
  const [dialog, setDialog] = useState<{ kind: "add" } | { kind: "edit"; r: HaResource } | { kind: "move"; r: HaResource; cmd: "migrate" | "relocate" } | null>(null);
  const close = () => setDialog(null);

  const st = status.data ?? [];
  const quorum = st.find((x) => x.type === "quorum");
  const master = st.find((x) => x.type === "master");
  const lrms = st.filter((x) => x.type === "lrm");
  const svc = new Map(st.filter((x) => x.type === "service").map((x) => [x.sid, x]));
  const byID = new Map((guests.data ?? []).map((g) => [`${g.type === "lxc" ? "ct" : "vm"}:${g.vmid}`, g]));
  const nodes = dc.hosts.map((h) => h.pve_node);

  const setState = (r: HaResource, state: string) => void run({ method: "set", path: `/cluster/ha/resources/${r.sid}`, params: { state } });
  const remove = async (r: HaResource) => {
    if (!(await askConfirm(`${r.sid} uit HA halen? De gast blijft draaien waar hij nu staat, maar Proxmox herstart of verhuist hem niet meer vanzelf.`))) return;
    void run({ method: "delete", path: `/cluster/ha/resources/${r.sid}` });
  };

  return (
    <div className="space-y-6">
      {banner}
      <QueryState q={status}>
        <div className="grid gap-4 sm:grid-cols-3">
          <Card title="Quorum">
            <p className={cx("text-lg font-semibold", quorum?.status === "OK" ? "text-emerald-700 dark:text-emerald-300" : "text-red-700 dark:text-red-300")}>
              {quorum?.status === "OK" ? "In orde" : (quorum?.status ?? "onbekend")}
            </p>
          </Card>
          <Card title="Manager">
            <p className="text-lg font-semibold">{master?.node ?? "–"}</p>
            <p className="text-xs text-slate-500">{master?.status}</p>
          </Card>
          <Card title="Hosts">
            <ul className="space-y-1 text-sm">
              {lrms.map((l) => (
                <li key={l.id} className="flex justify-between gap-2">
                  <span>{l.node}</span>
                  <Badge tone={/maintenance/.test(l.status ?? "") ? "amber" : /active|idle/.test(l.status ?? "") ? "green" : "red"}>
                    {/maintenance/.test(l.status ?? "") ? "onderhoud" : /active/.test(l.status ?? "") ? "actief" : /idle/.test(l.status ?? "") ? "rust" : (l.status ?? "?")}
                  </Badge>
                </li>
              ))}
            </ul>
          </Card>
        </div>
      </QueryState>

      <Card
        title={
          <span className="flex items-center justify-between gap-2">
            <span>Gasten onder HA</span>
            {isAdmin && (
              <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "add" })}>
                Gast toevoegen
              </Button>
            )}
          </span>
        }
      >
        <QueryState q={resources}>
          {(resources.data ?? []).length === 0 ? (
            <Empty>Nog geen gasten onder HA.</Empty>
          ) : (
            <div className="relative -mx-4 overflow-x-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Gast</th>
                    <th className={thClass}>Gevraagd</th>
                    <th className={thClass}>Nu</th>
                    <th className={thClass}>Host</th>
                    <th className={thClass}>Herstarts / verhuizen</th>
                    <th className={thClass}>
                      <span className="sr-only">Acties</span>
                    </th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {resources.data!.map((r) => {
                    const g = byID.get(r.sid);
                    const s = svc.get(r.sid);
                    return (
                      <tr key={r.sid}>
                        <td className={tdClass}>
                          {g ? (
                            <Link href={guestHref(dc.id, { node: g.node, type: g.type, vmid: g.vmid })} className="font-medium hover:underline">
                              {guestLabel(g.type)} {g.vmid} {g.name}
                            </Link>
                          ) : (
                            <span className="font-mono">{r.sid}</span>
                          )}
                          {r.comment && <span className="block text-xs text-slate-500">{r.comment}</span>}
                        </td>
                        <td className={tdClass}>
                          <Badge tone={stateTone(r.state ?? "started")}>{stateWord[r.state ?? "started"] ?? r.state}</Badge>
                        </td>
                        <td className={tdClass}>{s ? <Badge tone={stateTone(s.state)}>{stateWord[s.state ?? ""] ?? s.state}</Badge> : "–"}</td>
                        <td className={tdClass}>{s?.node ?? g?.node ?? "–"}</td>
                        <td className={tdClass + " tabular-nums"}>
                          {r.max_restart ?? 1} / {r.max_relocate ?? 1}
                        </td>
                        <td className={tdClass + " text-right"}>
                          {isAdmin && (
                            <ActionMenu
                              disabled={pending}
                              items={[
                                ...(r.state === "started" || !r.state
                                  ? [
                                      { label: "Live verhuizen…", onClick: () => setDialog({ kind: "move", r, cmd: "migrate" }) },
                                      { label: "Verplaatsen (herstart)…", onClick: () => setDialog({ kind: "move", r, cmd: "relocate" }) },
                                      { label: "Stoppen", onClick: () => setState(r, "stopped") },
                                    ]
                                  : [{ label: "Starten", onClick: () => setState(r, "started") }]),
                                ...(r.state === "error" ? [{ label: "Fout wissen (uit)", onClick: () => setState(r, "disabled") }] : []),
                                { label: "Wijzigen", onClick: () => setDialog({ kind: "edit", r }) },
                                { label: "Uit HA halen", onClick: () => void remove(r), danger: true },
                              ]}
                            />
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </QueryState>
      </Card>

      {rules.isError ? (
        <GroupsCard dc={dc} groups={groups.data ?? []} nodes={nodes} isAdmin={isAdmin} run={run} pending={pending} />
      ) : (
        <RulesCard rules={rules.data ?? []} sids={(resources.data ?? []).map((r) => r.sid)} nodes={nodes} isAdmin={isAdmin} run={run} pending={pending} byID={byID} />
      )}

      {dialog?.kind === "add" && (
        <ResourceDialog guests={(guests.data ?? []).filter((g) => !g.template && !(resources.data ?? []).some((r) => r.sid === `${g.type === "lxc" ? "ct" : "vm"}:${g.vmid}`))} groups={groups.data} run={run} pending={pending} onClose={close} />
      )}
      {dialog?.kind === "edit" && <ResourceDialog edit={dialog.r} guests={[]} groups={groups.data} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "move" && (
        <MoveDialog r={dialog.r} cmd={dialog.cmd} nodes={nodes.filter((n) => n !== svc.get(dialog.r.sid)?.node)} run={run} pending={pending} onClose={close} />
      )}
    </div>
  );
}

type Runner = ReturnType<typeof useCallRunner>["run"];

function ResourceDialog({ edit, guests, groups, run, pending, onClose }: { edit?: HaResource; guests: PveResource[]; groups?: HaGroup[]; run: Runner; pending: boolean; onClose: () => void }) {
  const [sid, setSid] = useState(edit?.sid ?? (guests[0] ? `${guests[0].type === "lxc" ? "ct" : "vm"}:${guests[0].vmid}` : ""));
  const [state, setState] = useState(edit?.state ?? "started");
  const [restart, setRestart] = useState(String(edit?.max_restart ?? 1));
  const [relocate, setRelocate] = useState(String(edit?.max_relocate ?? 1));
  const [group, setGroup] = useState(edit?.group ?? "");
  const [comment, setComment] = useState(edit?.comment ?? "");
  const error = !sid ? "Alle gasten staan al onder HA." : !/^\d+$/.test(restart) || !/^\d+$/.test(relocate) ? "Herstarts en verhuizingen zijn aantallen." : null;
  const submit = () => {
    const p: Record<string, string> = { state, max_restart: restart, max_relocate: relocate };
    if (comment) p.comment = comment;
    if (groups && group) p.group = group;
    const call = edit
      ? { method: "set" as const, path: `/cluster/ha/resources/${edit.sid}`, params: { ...p, ...(groups && !group && edit.group ? { delete: "group" } : {}) } }
      : { method: "create" as const, path: "/cluster/ha/resources", params: { ...p, sid } };
    void run(call).then((ok) => ok && onClose());
  };
  return (
    <FormModal title={edit ? `HA van ${edit.sid}` : "Gast onder HA zetten"} onClose={onClose} onSubmit={submit} submitLabel="Opslaan" disabled={pending} error={error}>
      {!edit && (
        <Field label="Gast" htmlFor="ha-sid">
          <Select id="ha-sid" value={sid} onChange={(e) => setSid(e.target.value)}>
            {guests.map((g) => (
              <option key={g.id} value={`${g.type === "lxc" ? "ct" : "vm"}:${g.vmid}`}>
                {guestLabel(g.type)} {g.vmid} {g.name} (op {g.node})
              </option>
            ))}
          </Select>
        </Field>
      )}
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Gevraagde toestand" htmlFor="ha-state">
          <Select id="ha-state" value={state} onChange={(e) => setState(e.target.value)}>
            <option value="started">Draaien</option>
            <option value="stopped">Gestopt (blijft onder HA)</option>
            <option value="disabled">Uit</option>
            <option value="ignored">Negeren (HA laat hem met rust)</option>
          </Select>
        </Field>
        {groups && (
          <Field label="Groep" htmlFor="ha-group">
            <Select id="ha-group" value={group} onChange={(e) => setGroup(e.target.value)}>
              <option value="">geen (alle hosts)</option>
              {groups.map((g) => (
                <option key={g.group}>{g.group}</option>
              ))}
            </Select>
          </Field>
        )}
        <Field label="Herstarts op dezelfde host" htmlFor="ha-rs" hint="Voor HA hem verhuist">
          <Input id="ha-rs" inputMode="numeric" value={restart} onChange={(e) => setRestart(e.target.value)} />
        </Field>
        <Field label="Verhuizingen" htmlFor="ha-rl" hint="Voor HA opgeeft">
          <Input id="ha-rl" inputMode="numeric" value={relocate} onChange={(e) => setRelocate(e.target.value)} />
        </Field>
      </div>
      <Field label="Opmerking" htmlFor="ha-c">
        <Input id="ha-c" value={comment} onChange={(e) => setComment(e.target.value)} />
      </Field>
    </FormModal>
  );
}

function MoveDialog({ r, cmd, nodes, run, pending, onClose }: { r: HaResource; cmd: "migrate" | "relocate"; nodes: string[]; run: Runner; pending: boolean; onClose: () => void }) {
  const [node, setNode] = useState(nodes[0] ?? "");
  return (
    <FormModal
      title={cmd === "migrate" ? `${r.sid} live verhuizen` : `${r.sid} verplaatsen`}
      onClose={onClose}
      submitLabel={cmd === "migrate" ? "Verhuizen" : "Verplaatsen"}
      disabled={pending || !node}
      onSubmit={() => void run({ method: "create", path: `/cluster/ha/resources/${r.sid}/${cmd}`, params: { node } }).then((ok) => ok && onClose())}
    >
      <p className="text-sm">{cmd === "migrate" ? "De gast blijft draaien terwijl hij verhuist (een CT herstart altijd even)." : "De gast stopt, verhuist en start op de nieuwe host."}</p>
      <Field label="Naar" htmlFor="ha-mv">
        <Select id="ha-mv" value={node} onChange={(e) => setNode(e.target.value)}>
          {nodes.map((n) => (
            <option key={n}>{n}</option>
          ))}
        </Select>
      </Field>
    </FormModal>
  );
}

// nodes "a:2,b" → [{node:a, prio:2}, {node:b, prio:""}]
function parseNodes(s?: string) {
  return (s ?? "")
    .split(",")
    .filter(Boolean)
    .map((x) => {
      const [node, prio] = x.split(":");
      return { node, prio: prio ?? "" };
    });
}

function NodesPicker({ nodes, value, onChange }: { nodes: string[]; value: Record<string, string | null>; onChange: (v: Record<string, string | null>) => void }) {
  return (
    <div className="grid gap-2 sm:grid-cols-2">
      {nodes.map((n) => (
        <div key={n} className="flex items-center gap-2 text-sm">
          <input type="checkbox" aria-label={n} checked={value[n] !== undefined && value[n] !== null} onChange={(e) => onChange({ ...value, [n]: e.target.checked ? "" : null })} />
          <span className="w-28 truncate">{n}</span>
          <Input
            aria-label={`Voorkeur ${n}`}
            inputMode="numeric"
            placeholder="voorkeur"
            className="w-24"
            disabled={value[n] === undefined || value[n] === null}
            value={value[n] ?? ""}
            onChange={(e) => onChange({ ...value, [n]: e.target.value.replace(/\D/g, "") })}
          />
        </div>
      ))}
    </div>
  );
}

const joinNodes = (v: Record<string, string | null>) =>
  Object.entries(v)
    .filter(([, p]) => p !== null && p !== undefined)
    .map(([n, p]) => (p ? `${n}:${p}` : n))
    .join(",");

function RulesCard({ rules, sids, nodes, isAdmin, run, pending, byID }: { rules: HaRule[]; sids: string[]; nodes: string[]; isAdmin: boolean; run: Runner; pending: boolean; byID: Map<string, PveResource> }) {
  const [edit, setEdit] = useState<HaRule | "new" | null>(null);
  const name = (sid: string) => byID.get(sid)?.name ?? sid;
  const remove = async (r: HaRule) => {
    if (await askConfirm(`Regel ${r.rule} verwijderen?`)) void run({ method: "delete", path: `/cluster/ha/rules/${r.rule}` });
  };
  return (
    <Card
      title={
        <span className="flex items-center justify-between gap-2">
          <span>Regels</span>
          {isAdmin && (
            <Button variant="secondary" disabled={pending || sids.length === 0} onClick={() => setEdit("new")}>
              Regel toevoegen
            </Button>
          )}
        </span>
      }
    >
      {rules.length === 0 ? (
        <Empty>Geen regels: HA mag elke gast op elke host zetten.</Empty>
      ) : (
        <ul className="divide-y divide-slate-100 dark:divide-slate-800">
          {rules.map((r) => (
            <li key={r.rule} className="flex items-start justify-between gap-3 py-2 text-sm">
              <div className="min-w-0">
                <span className="font-medium">{r.rule}</span> {r.disable ? <Badge tone="amber">uit</Badge> : null}{" "}
                {r.type === "node-affinity" ? (
                  <span>
                    {r.resources.split(",").map(name).join(", ")} {r.strict ? "alleen" : "liefst"} op {parseNodes(r.nodes).map((n) => n.node + (n.prio ? ` (${n.prio})` : "")).join(", ")}
                  </span>
                ) : (
                  <span>
                    {r.resources.split(",").map(name).join(", ")} {r.affinity === "negative" ? "nooit samen op één host" : "altijd samen"}
                  </span>
                )}
                {r.comment && <span className="block text-xs text-slate-500">{r.comment}</span>}
              </div>
              {isAdmin && (
                <ActionMenu
                  disabled={pending}
                  items={[
                    { label: "Wijzigen", onClick: () => setEdit(r) },
                    { label: "Verwijderen", onClick: () => void remove(r), danger: true },
                  ]}
                />
              )}
            </li>
          ))}
        </ul>
      )}
      {edit && <RuleDialog rule={edit === "new" ? null : edit} sids={sids} nodes={nodes} name={name} run={run} pending={pending} onClose={() => setEdit(null)} />}
    </Card>
  );
}

function RuleDialog({ rule, sids, nodes, name, run, pending, onClose }: { rule: HaRule | null; sids: string[]; nodes: string[]; name: (s: string) => string; run: Runner; pending: boolean; onClose: () => void }) {
  const [id, setId] = useState(rule?.rule ?? "");
  const [type, setType] = useState(rule?.type ?? "node-affinity");
  const [res, setRes] = useState<Set<string>>(new Set(rule?.resources.split(",").filter(Boolean) ?? []));
  const [nv, setNv] = useState<Record<string, string | null>>(() => Object.fromEntries(parseNodes(rule?.nodes).map((n) => [n.node, n.prio])));
  const [strict, setStrict] = useState(!!rule?.strict);
  const [affinity, setAffinity] = useState(rule?.affinity ?? "negative");
  const [comment, setComment] = useState(rule?.comment ?? "");
  const [enabled, setEnabled] = useState(!rule?.disable);
  const error = !/^[A-Za-z][A-Za-z0-9_-]{1,63}$/.test(id)
    ? "Geef de regel een naam (letters, cijfers, - en _)."
    : res.size === 0
      ? "Kies minstens één gast."
      : type === "node-affinity" && !joinNodes(nv)
        ? "Kies minstens één host."
        : type === "resource-affinity" && res.size < 2
          ? "Kies minstens twee gasten."
          : null;
  const submit = () => {
    const p: Record<string, string> = { resources: [...res].join(","), disable: enabled ? "0" : "1" };
    if (comment) p.comment = comment;
    if (type === "node-affinity") Object.assign(p, { nodes: joinNodes(nv), strict: strict ? "1" : "0" });
    else p.affinity = affinity;
    const call = rule ? { method: "set" as const, path: `/cluster/ha/rules/${rule.rule}`, params: { ...p, type } } : { method: "create" as const, path: "/cluster/ha/rules", params: { ...p, rule: id, type } };
    void run(call).then((ok) => ok && onClose());
  };
  return (
    <FormModal title={rule ? `Regel ${rule.rule}` : "Nieuwe HA-regel"} onClose={onClose} onSubmit={submit} submitLabel="Opslaan" disabled={pending} error={error} wide>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Naam" htmlFor="hr-id">
          <Input id="hr-id" value={id} onChange={(e) => setId(e.target.value)} disabled={!!rule} />
        </Field>
        <Field label="Soort" htmlFor="hr-type">
          <Select id="hr-type" value={type} onChange={(e) => setType(e.target.value)} disabled={!!rule}>
            <option value="node-affinity">Op welke hosts</option>
            <option value="resource-affinity">Samen of apart</option>
          </Select>
        </Field>
      </div>
      <fieldset>
        <legend className="mb-1 text-sm font-medium">Gasten</legend>
        <div className="grid max-h-40 gap-1 overflow-y-auto rounded border border-slate-200 p-2 text-sm sm:grid-cols-2 dark:border-slate-800">
          {sids.map((s) => (
            <label key={s} className="flex items-center gap-2">
              <input
                type="checkbox"
                checked={res.has(s)}
                onChange={() =>
                  setRes((x) => {
                    const n = new Set(x);
                    if (n.has(s)) n.delete(s);
                    else n.add(s);
                    return n;
                  })
                }
              />
              <span className="truncate">
                {s} {name(s) !== s ? name(s) : ""}
              </span>
            </label>
          ))}
        </div>
      </fieldset>
      {type === "node-affinity" ? (
        <>
          <fieldset>
            <legend className="mb-1 text-sm font-medium">Hosts (hoger getal = liever)</legend>
            <NodesPicker nodes={nodes} value={nv} onChange={setNv} />
          </fieldset>
          <Check id="hr-strict" checked={strict} onChange={setStrict}>
            Strikt: nooit op een andere host, ook niet als deze allemaal uitvallen
          </Check>
        </>
      ) : (
        <Field label="Regel" htmlFor="hr-aff">
          <Select id="hr-aff" value={affinity} onChange={(e) => setAffinity(e.target.value)}>
            <option value="negative">Nooit samen op één host (bijvoorbeeld de twee firewalls)</option>
            <option value="positive">Altijd samen op één host</option>
          </Select>
        </Field>
      )}
      <Field label="Opmerking" htmlFor="hr-c">
        <Input id="hr-c" value={comment} onChange={(e) => setComment(e.target.value)} />
      </Field>
      <Check id="hr-en" checked={enabled} onChange={setEnabled}>
        Aan
      </Check>
    </FormModal>
  );
}

function GroupsCard({ groups, nodes, isAdmin, run, pending }: { dc: Datacenter; groups: HaGroup[]; nodes: string[]; isAdmin: boolean; run: Runner; pending: boolean }) {
  const [edit, setEdit] = useState<HaGroup | "new" | null>(null);
  const remove = async (g: HaGroup) => {
    if (await askConfirm(`Groep ${g.group} verwijderen? Gasten in deze groep mogen daarna overal draaien.`)) void run({ method: "delete", path: `/cluster/ha/groups/${g.group}` });
  };
  return (
    <Card
      title={
        <span className="flex items-center justify-between gap-2">
          <span>HA-groepen</span>
          {isAdmin && (
            <Button variant="secondary" disabled={pending} onClick={() => setEdit("new")}>
              Groep toevoegen
            </Button>
          )}
        </span>
      }
    >
      {groups.length === 0 ? (
        <Empty>Geen groepen: HA mag elke gast op elke host zetten.</Empty>
      ) : (
        <ul className="divide-y divide-slate-100 dark:divide-slate-800">
          {groups.map((g) => (
            <li key={g.group} className="flex items-start justify-between gap-3 py-2 text-sm">
              <div>
                <span className="font-medium">{g.group}</span>: {parseNodes(g.nodes).map((n) => n.node + (n.prio ? ` (${n.prio})` : "")).join(", ")}{" "}
                {g.restricted ? <Badge>alleen deze hosts</Badge> : null} {g.nofailback ? <Badge>niet terug</Badge> : null}
              </div>
              {isAdmin && (
                <ActionMenu
                  disabled={pending}
                  items={[
                    { label: "Wijzigen", onClick: () => setEdit(g) },
                    { label: "Verwijderen", onClick: () => void remove(g), danger: true },
                  ]}
                />
              )}
            </li>
          ))}
        </ul>
      )}
      {edit && <GroupDialog group={edit === "new" ? null : edit} nodes={nodes} run={run} pending={pending} onClose={() => setEdit(null)} />}
    </Card>
  );
}

function GroupDialog({ group, nodes, run, pending, onClose }: { group: HaGroup | null; nodes: string[]; run: Runner; pending: boolean; onClose: () => void }) {
  const [id, setId] = useState(group?.group ?? "");
  const [nv, setNv] = useState<Record<string, string | null>>(() => Object.fromEntries(parseNodes(group?.nodes).map((n) => [n.node, n.prio])));
  const [restricted, setRestricted] = useState(!!group?.restricted);
  const [nofailback, setNofailback] = useState(!!group?.nofailback);
  const error = !/^[A-Za-z][A-Za-z0-9_-]{1,63}$/.test(id) ? "Geef de groep een naam." : !joinNodes(nv) ? "Kies minstens één host." : null;
  const submit = () => {
    const p = { nodes: joinNodes(nv), restricted: restricted ? "1" : "0", nofailback: nofailback ? "1" : "0" };
    const call = group ? { method: "set" as const, path: `/cluster/ha/groups/${group.group}`, params: p } : { method: "create" as const, path: "/cluster/ha/groups", params: { ...p, group: id } };
    void run(call).then((ok) => ok && onClose());
  };
  return (
    <FormModal title={group ? `Groep ${group.group}` : "Nieuwe HA-groep"} onClose={onClose} onSubmit={submit} submitLabel="Opslaan" disabled={pending} error={error}>
      <Field label="Naam" htmlFor="hg-id">
        <Input id="hg-id" value={id} onChange={(e) => setId(e.target.value)} disabled={!!group} />
      </Field>
      <fieldset>
        <legend className="mb-1 text-sm font-medium">Hosts (hoger getal = liever)</legend>
        <NodesPicker nodes={nodes} value={nv} onChange={setNv} />
      </fieldset>
      <Check id="hg-r" checked={restricted} onChange={setRestricted}>
        Alleen op deze hosts
      </Check>
      <Check id="hg-n" checked={nofailback} onChange={setNofailback}>
        Niet vanzelf terug naar de voorkeurshost
      </Check>
    </FormModal>
  );
}
