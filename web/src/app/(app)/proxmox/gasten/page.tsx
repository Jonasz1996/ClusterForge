"use client";

import Link from "next/link";
import { Suspense, useMemo, useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { Empty, formatBytes, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Modal } from "@/components/Modal";
import { DatacenterGate, Meter, uptime, useCallRunner } from "@/components/pve/bits";
import { AgentInstallDialog } from "@/components/pve/AgentInstall";
import { CreateCTDialog, CreateVMDialog } from "@/components/pve/CreateGuest";
import { TagChips, useTagColors } from "@/components/pve/TagChips";
import { Badge, Button, Card, cx, Field, Input, PageHeader, Select } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { guestHref, guestLabel, guestPath, pct, splitTags, usePveGet, type Datacenter, type PveResource } from "@/lib/pve";
import { vmStatusInfo } from "@/lib/proxmox";

export default function GuestsPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Gasten"
        description="Alle VM's en CT's van het Proxmox-cluster: zoeken op naam, tag of host, starten en stoppen, tags in één keer zetten, en nieuwe gasten maken."
      />
      <Suspense>
        <DatacenterGate>{(dc) => <GuestsView key={dc.id} dc={dc} />}</DatacenterGate>
      </Suspense>
    </div>
  );
}

function GuestsView({ dc }: { dc: Datacenter }) {
  const isAdmin = useIsAdmin();
  const res = usePveGet<PveResource[]>(dc.id, "/cluster/resources", { refetch: 10_000 });
  const colors = useTagColors(dc.id);
  const { run, pending, banner } = useCallRunner(dc.id);
  const [search, setSearch] = useState("");
  const [node, setNode] = useState("");
  const [state, setState] = useState("");
  const [tag, setTag] = useState("");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [dialog, setDialog] = useState<"vm" | "ct" | "tags" | "agent" | null>(null);

  const all = useMemo(() => (res.data ?? []).filter((r) => r.type === "qemu" || r.type === "lxc"), [res.data]);
  const tags = useMemo(() => [...new Set(all.flatMap((r) => splitTags(r.tags)))].sort(), [all]);
  const nodes = useMemo(() => [...new Set((res.data ?? []).filter((r) => r.type === "node").map((r) => r.node))].sort(), [res.data]);
  const storages = useMemo(() => (res.data ?? []).filter((r) => r.type === "storage"), [res.data]);
  const list = useMemo(() => {
    const f = search.trim().toLowerCase();
    return all
      .filter((r) => !node || r.node === node)
      .filter((r) => !state || (state === "template" ? r.template === 1 : r.template !== 1 && r.status === state))
      .filter((r) => !tag || splitTags(r.tags).includes(tag))
      .filter((r) => !f || (r.name ?? "").toLowerCase().includes(f) || String(r.vmid).includes(f) || (r.tags ?? "").includes(f))
      .sort((a, b) => (a.vmid ?? 0) - (b.vmid ?? 0));
  }, [all, node, state, tag, search]);
  const selected = list.filter((r) => picked.has(r.id));
  const toggle = (id: string) =>
    setPicked((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });

  const bulk = async (cmd: "start" | "shutdown") => {
    const targets = selected.filter((r) => r.template !== 1 && (cmd === "start" ? r.status !== "running" : r.status === "running"));
    if (targets.length === 0) return;
    if (!(await askConfirm(`${targets.length} gast(en) ${cmd === "start" ? "starten" : "afsluiten"}: ${targets.map((r) => r.name ?? r.vmid).join(", ")}?`))) return;
    for (const r of targets) await run({ method: "create", path: `${guestPath(r.node, r.type as "qemu", r.vmid!)}/status/${cmd}` });
    setPicked(new Set());
  };

  return (
    <div className="space-y-4">
      {banner}
      <Card>
        <div className="space-y-4">
          <div className="flex flex-wrap items-end justify-between gap-3">
            <div className="grid flex-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
              <Field label="Zoeken" htmlFor="g-search">
                <Input id="g-search" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Naam, VMID of tag" />
              </Field>
              <Field label="Host" htmlFor="g-node">
                <Select id="g-node" value={node} onChange={(e) => setNode(e.target.value)}>
                  <option value="">Alle</option>
                  {nodes.map((n) => (
                    <option key={n}>{n}</option>
                  ))}
                </Select>
              </Field>
              <Field label="Toestand" htmlFor="g-state">
                <Select id="g-state" value={state} onChange={(e) => setState(e.target.value)}>
                  <option value="">Alle</option>
                  <option value="running">Draait</option>
                  <option value="stopped">Uit</option>
                  <option value="template">Templates</option>
                </Select>
              </Field>
              <Field label="Tag" htmlFor="g-tag">
                <Select id="g-tag" value={tag} onChange={(e) => setTag(e.target.value)}>
                  <option value="">Alle</option>
                  {tags.map((t) => (
                    <option key={t}>{t}</option>
                  ))}
                </Select>
              </Field>
            </div>
            {isAdmin && (
              <div className="flex gap-2">
                <Button onClick={() => setDialog("vm")}>VM maken</Button>
                <Button variant="secondary" onClick={() => setDialog("ct")}>
                  CT maken
                </Button>
              </div>
            )}
          </div>
          {isAdmin && selected.length > 0 && (
            <div className="flex flex-wrap items-center gap-2 rounded-md bg-slate-50 p-2 text-sm dark:bg-slate-800/60">
              <span className="font-medium">{selected.length} gekozen</span>
              <Button variant="secondary" disabled={pending} onClick={() => setDialog("tags")}>
                Tags…
              </Button>
              <Button variant="secondary" disabled={pending} onClick={() => void bulk("start")}>
                Starten
              </Button>
              <Button variant="secondary" disabled={pending} onClick={() => void bulk("shutdown")}>
                Afsluiten
              </Button>
              {selected.some((r) => r.type === "lxc" && r.template !== 1) && (
                <Button variant="secondary" onClick={() => setDialog("agent")}>
                  Agent installeren…
                </Button>
              )}
              <Button variant="ghost" onClick={() => setPicked(new Set())}>
                Wissen
              </Button>
            </div>
          )}
          <QueryState q={res}>
            {list.length === 0 ? (
              <Empty>Geen gasten die passen.</Empty>
            ) : (
              <div className="relative -mx-4 overflow-x-auto sm:mx-0">
                <table className={tableClass}>
                  <thead>
                    <tr>
                      {isAdmin && (
                        <th className={thClass}>
                          <input
                            type="checkbox"
                            aria-label="Alles kiezen"
                            checked={selected.length === list.length}
                            onChange={(e) => setPicked(e.target.checked ? new Set(list.map((r) => r.id)) : new Set())}
                          />
                        </th>
                      )}
                      <th className={thClass}>ID</th>
                      <th className={thClass}>Naam</th>
                      <th className={thClass}>Host</th>
                      <th className={thClass}>Toestand</th>
                      <th className={cx(thClass, "hidden md:table-cell")}>CPU</th>
                      <th className={cx(thClass, "hidden md:table-cell")}>Geheugen</th>
                      <th className={cx(thClass, "hidden lg:table-cell")}>Schijf</th>
                      <th className={cx(thClass, "hidden lg:table-cell")}>Tags</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                    {list.map((r) => {
                      const st = vmStatusInfo(r.status);
                      return (
                        <tr key={r.id} className={cx(picked.has(r.id) && "bg-brand-50/60 dark:bg-brand-950/20")}>
                          {isAdmin && (
                            <td className={tdClass}>
                              <input type="checkbox" aria-label={`Kies ${r.name ?? r.vmid}`} checked={picked.has(r.id)} onChange={() => toggle(r.id)} />
                            </td>
                          )}
                          <td className={cx(tdClass, "font-mono")}>{r.vmid}</td>
                          <td className={tdClass}>
                            <Link href={guestHref(dc.id, r)} className="font-medium hover:underline">
                              {r.name || `${guestLabel(r.type)} ${r.vmid}`}
                            </Link>
                            <div className="text-xs text-slate-500">
                              {guestLabel(r.type)}
                              {r.hastate ? ` · HA ${r.hastate}` : ""}
                              {r.lock ? ` · slot ${r.lock}` : ""}
                              {r.pool ? ` · pool ${r.pool}` : ""}
                            </div>
                            <div className="mt-1 lg:hidden">
                              <TagChips tags={splitTags(r.tags)} colors={colors} />
                            </div>
                          </td>
                          <td className={tdClass}>{r.node}</td>
                          <td className={tdClass}>
                            {r.template === 1 ? <Badge tone="blue">Template</Badge> : <Badge tone={st.tone}>{st.label}</Badge>}
                            {r.status === "running" && <div className="mt-0.5 text-xs text-slate-500">{uptime(r.uptime ?? 0)}</div>}
                          </td>
                          <td className={cx(tdClass, "hidden min-w-28 md:table-cell")}>
                            {r.status === "running" ? <Meter value={(r.cpu ?? 0) * 100} warn={80} crit={95} /> : null}
                            <div className="text-xs text-slate-500">{r.maxcpu} vCPU</div>
                          </td>
                          <td className={cx(tdClass, "hidden min-w-32 md:table-cell")}>
                            {r.status === "running" ? <Meter value={pct(r.mem ?? 0, r.maxmem ?? 0)} /> : null}
                            <div className="text-xs text-slate-500">{formatBytes(r.maxmem ?? 0)}</div>
                          </td>
                          <td className={cx(tdClass, "hidden whitespace-nowrap text-slate-500 lg:table-cell")}>{formatBytes(r.maxdisk ?? 0)}</td>
                          <td className={cx(tdClass, "hidden lg:table-cell")}>
                            <TagChips tags={splitTags(r.tags)} colors={colors} />
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </QueryState>
        </div>
      </Card>
      {dialog === "vm" && <CreateVMDialog dc={dc} nodes={nodes} storages={storages} used={all} onClose={() => setDialog(null)} />}
      {dialog === "ct" && <CreateCTDialog dc={dc} nodes={nodes} storages={storages} used={all} onClose={() => setDialog(null)} />}
      {dialog === "agent" && <AgentInstallDialog dc={dc} guests={selected} onClose={() => setDialog(null)} />}
      {dialog === "tags" && (
        <BulkTags
          guests={selected}
          known={tags}
          pending={pending}
          onClose={() => setDialog(null)}
          apply={async (add, remove) => {
            for (const r of selected) {
              const cur = splitTags(r.tags);
              const next = [...new Set([...cur.filter((t) => !remove.includes(t)), ...add])].sort();
              if (next.join(";") === cur.join(";")) continue;
              const params: Record<string, string> = next.length ? { tags: next.join(";") } : { delete: "tags" };
              await run({ method: "set", path: `${guestPath(r.node, r.type as "qemu", r.vmid!)}/config`, params });
            }
            setDialog(null);
            setPicked(new Set());
          }}
        />
      )}
    </div>
  );
}

const tagRe = /^[a-z0-9_][a-z0-9_.+-]{0,63}$/;

function BulkTags({
  guests,
  known,
  pending,
  onClose,
  apply,
}: {
  guests: PveResource[];
  known: string[];
  pending: boolean;
  onClose: () => void;
  apply: (add: string[], remove: string[]) => Promise<void>;
}) {
  const [add, setAdd] = useState("");
  const [remove, setRemove] = useState<string[]>([]);
  const addList = add
    .split(/[\s,;]+/)
    .map((t) => t.trim().toLowerCase())
    .filter(Boolean);
  const bad = addList.filter((t) => !tagRe.test(t));
  const present = [...new Set(guests.flatMap((g) => splitTags(g.tags)))].sort();
  return (
    <Modal title={`Tags van ${guests.length} gast(en)`} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (bad.length === 0) void apply(addList, remove);
        }}
      >
        <Field label="Toevoegen" htmlFor="bt-add" hint={`Kleine letters, cijfers en - _ . +; bekend: ${known.slice(0, 12).join(", ") || "nog geen"}`}>
          <Input id="bt-add" value={add} onChange={(e) => setAdd(e.target.value)} placeholder="prod web" list="bt-known" />
          <datalist id="bt-known">
            {known.map((t) => (
              <option key={t} value={t} />
            ))}
          </datalist>
        </Field>
        {bad.length > 0 && <p className="text-sm text-red-600">Ongeldige tag: {bad.join(", ")}</p>}
        {present.length > 0 && (
          <fieldset className="space-y-1 text-sm">
            <legend className="mb-1 font-medium">Weghalen</legend>
            {present.map((t) => (
              <label key={t} className="mr-3 inline-flex items-center gap-1.5">
                <input type="checkbox" checked={remove.includes(t)} onChange={(e) => setRemove((r) => (e.target.checked ? [...r, t] : r.filter((x) => x !== t)))} />
                {t}
              </label>
            ))}
          </fieldset>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" disabled={pending || bad.length > 0 || (addList.length === 0 && remove.length === 0)}>
            Toepassen
          </Button>
        </div>
      </form>
    </Modal>
  );
}
