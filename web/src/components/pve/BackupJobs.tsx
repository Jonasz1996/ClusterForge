"use client";

import Link from "next/link";
import { useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { when } from "@/components/pve/Backups";
import { ActionMenu, useCallRunner } from "@/components/pve/bits";
import { Check, FormModal } from "@/components/pve/guest/common";
import { Alert, Badge, Button, Card, Field, Input, Select } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { backupStores, guestHref, guestLabel, parseProps, usePveGet, type Datacenter, type PveBackupJob, type PveResource } from "@/lib/pve";

// Back-upjobs van het datacenter (/cluster/backup) en welke gasten geen
// back-up krijgen (fase 26 M3).

const keepKeys = [
  { key: "keep-last", label: "Laatste" },
  { key: "keep-daily", label: "Dagen" },
  { key: "keep-weekly", label: "Weken" },
  { key: "keep-monthly", label: "Maanden" },
] as const;

function retention(v: PveBackupJob["prune-backups"]): Record<string, string> {
  if (!v) return {};
  if (typeof v === "string") return parseProps(v);
  return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, String(x)]));
}

function retentionText(v: PveBackupJob["prune-backups"]) {
  const r = retention(v);
  if (r["keep-all"] === "1") return "alles bewaren";
  const parts = keepKeys.filter((k) => r[k.key]).map((k) => `${r[k.key]} ${k.label.toLowerCase()}`);
  return parts.length ? parts.join(", ") : "zoals de storage";
}

const scheduleHelp: Record<string, string> = {
  daily: "elke dag om 00:00",
  weekly: "elke maandag om 00:00",
  monthly: "de eerste van de maand",
  hourly: "elk uur",
};

function scheduleText(s?: string) {
  if (!s) return "–";
  if (scheduleHelp[s]) return scheduleHelp[s];
  if (/^\d{1,2}:\d{2}$/.test(s)) return `elke dag om ${s}`;
  return s;
}

export function BackupJobs({ dc, res }: { dc: Datacenter; res: PveResource[] }) {
  const isAdmin = useIsAdmin();
  const jobs = usePveGet<PveBackupJob[]>(dc.id, "/cluster/backup", { refetch: 30_000 });
  const missing = usePveGet<{ vmid: number; name?: string; type: string }[]>(dc.id, "/cluster/backup-info/not-backed-up", { refetch: 60_000 });
  const { run, pending, banner } = useCallRunner(dc.id);
  const [edit, setEdit] = useState<PveBackupJob | "new" | null>(null);
  const guests = res.filter((r) => r.type === "qemu" || r.type === "lxc");

  const runNow = async (j: PveBackupJob) => {
    if (!(await askConfirm(`Back-upjob ${j.comment || j.id} nu uitvoeren?`))) return;
    const ids = (j.vmid ?? "").split(",").filter(Boolean);
    for (const h of dc.hosts.filter((x) => x.online)) {
      if (j.node && j.node !== h.pve_node) continue;
      const p: Record<string, string> = {};
      for (const k of ["storage", "mode", "compress", "notes-template", "pool"] as const) if (j[k]) p[k] = String(j[k]);
      const r = retention(j["prune-backups"]);
      if (Object.keys(r).length) p["prune-backups"] = Object.entries(r).map(([k, v]) => `${k}=${v}`).join(",");
      if (j.all) {
        p.all = "1";
        if (j.exclude) p.exclude = j.exclude;
      } else if (!j.pool) {
        const here = ids.filter((id) => guests.some((g) => String(g.vmid) === id && g.node === h.pve_node));
        if (here.length === 0) continue;
        p.vmid = here.join(",");
      }
      await run({ method: "create", path: `/nodes/${h.pve_node}/vzdump`, params: p });
    }
  };

  const toggle = (j: PveBackupJob) => void run({ method: "set", path: `/cluster/backup/${j.id}`, params: { enabled: j.enabled === 0 ? "1" : "0" } });
  const remove = async (j: PveBackupJob) => {
    if (!(await askConfirm(`Back-upjob ${j.comment || j.id} verwijderen? Bestaande back-ups blijven staan.`))) return;
    void run({ method: "delete", path: `/cluster/backup/${j.id}` });
  };

  return (
    <div className="space-y-6">
      {banner}
      {(missing.data ?? []).length > 0 && (
        <Alert kind="info">
          Geen back-upjob voor:{" "}
          {missing.data!.map((g, i) => {
            const r = guests.find((x) => x.vmid === g.vmid);
            return (
              <span key={g.vmid}>
                {i > 0 && ", "}
                {r ? (
                  <Link href={guestHref(dc.id, { node: r.node, type: r.type, vmid: r.vmid })} className="underline">
                    {g.name ?? g.vmid}
                  </Link>
                ) : (
                  (g.name ?? g.vmid)
                )}
              </span>
            );
          })}
          .
        </Alert>
      )}
      <Card
        title={
          <span className="flex items-center justify-between gap-2">
            <span>Back-upjobs</span>
            {isAdmin && (
              <Button variant="secondary" disabled={pending} onClick={() => setEdit("new")}>
                Job toevoegen
              </Button>
            )}
          </span>
        }
      >
        <QueryState q={jobs}>
          {(jobs.data ?? []).length === 0 ? (
            <Empty>Nog geen back-upjobs.</Empty>
          ) : (
            <div className="relative -mx-4 overflow-x-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Job</th>
                    <th className={thClass}>Wanneer</th>
                    <th className={thClass}>Wat</th>
                    <th className={thClass}>Naar</th>
                    <th className={thClass}>Bewaren</th>
                    <th className={thClass}>Volgende</th>
                    <th className={thClass}>
                      <span className="sr-only">Acties</span>
                    </th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {jobs.data!.map((j) => (
                    <tr key={j.id}>
                      <td className={tdClass}>
                        <span className="font-medium">{j.comment || j.id}</span>{" "}
                        {j.enabled === 0 && <Badge tone="amber">uit</Badge>}
                        <span className="block text-xs text-slate-500">{j.mode ?? "snapshot"}</span>
                      </td>
                      <td className={tdClass}>{scheduleText(j.schedule)}</td>
                      <td className={tdClass + " text-sm"}>
                        {j.all ? `alles${j.exclude ? ` behalve ${j.exclude}` : ""}` : j.pool ? `pool ${j.pool}` : (j.vmid ?? "").split(",").join(", ")}
                        {j.node && <span className="block text-xs text-slate-500">alleen op {j.node}</span>}
                      </td>
                      <td className={tdClass}>{j.storage ?? "–"}</td>
                      <td className={tdClass + " text-xs"}>{retentionText(j["prune-backups"])}</td>
                      <td className={tdClass + " whitespace-nowrap text-xs"}>{j.enabled === 0 ? "–" : when(j["next-run"])}</td>
                      <td className={tdClass + " text-right"}>
                        {isAdmin && (
                          <ActionMenu
                            disabled={pending}
                            items={[
                              { label: "Nu uitvoeren", onClick: () => void runNow(j) },
                              { label: "Wijzigen", onClick: () => setEdit(j) },
                              { label: j.enabled === 0 ? "Aanzetten" : "Uitzetten", onClick: () => toggle(j) },
                              { label: "Verwijderen", onClick: () => void remove(j), danger: true },
                            ]}
                          />
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </QueryState>
      </Card>
      {edit && <JobDialog res={res} job={edit === "new" ? null : edit} run={run} pending={pending} onClose={() => setEdit(null)} />}
    </div>
  );
}

function JobDialog({ res, job, run, pending, onClose }: { res: PveResource[]; job: PveBackupJob | null; run: ReturnType<typeof useCallRunner>["run"]; pending: boolean; onClose: () => void }) {
  const stores = backupStores(res);
  const guests = res.filter((r) => (r.type === "qemu" || r.type === "lxc") && !r.template).sort((a, b) => (a.vmid ?? 0) - (b.vmid ?? 0));
  const r = retention(job?.["prune-backups"]);
  const [comment, setComment] = useState(job?.comment ?? "");
  const [schedule, setSchedule] = useState(job?.schedule ?? "02:00");
  const [storage, setStorage] = useState(job?.storage ?? (stores.find((s) => s.shared) ?? stores[0])?.storage ?? "");
  const [sel, setSel] = useState<"all" | "some">(job && !job.all ? "some" : "all");
  const [ids, setIds] = useState<Set<string>>(new Set((job?.all ? job?.exclude : job?.vmid)?.split(",").filter(Boolean) ?? []));
  const [mode, setMode] = useState(job?.mode ?? "snapshot");
  const [keep, setKeep] = useState<Record<string, string>>(job ? r : { "keep-daily": "7", "keep-weekly": "4", "keep-monthly": "3" });
  const [notes, setNotes] = useState(job?.["notes-template"] ?? "{{guestname}}");
  const [enabled, setEnabled] = useState(job ? job.enabled !== 0 : true);
  const pbs = /pbs/.test(stores.find((s) => s.storage === storage)?.type ?? "");
  const toggle = (id: string) =>
    setIds((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  const error = !/^[\w :.,/*-]{1,64}$/.test(schedule.trim())
    ? "Geef een tijd (02:00) of een Proxmox-schema, zoals sat 03:00 of mon..fri 22:30."
    : !storage
      ? "Kies een storage."
      : sel === "some" && ids.size === 0
        ? "Kies minstens één gast."
        : Object.values(keep).some((v) => v && !/^\d+$/.test(v))
          ? "Bewaren is een aantal."
          : null;
  const submit = () => {
    const p: Record<string, string> = { schedule: schedule.trim(), storage, mode, enabled: enabled ? "1" : "0", "notes-template": notes };
    if (comment.trim()) p.comment = comment.trim();
    if (!pbs) p.compress = "zstd";
    const prune = Object.entries(keep).filter(([, v]) => v && v !== "0").map(([k, v]) => `${k}=${v}`);
    p["prune-backups"] = prune.length ? prune.join(",") : "keep-all=1";
    const del: string[] = [];
    if (sel === "all") {
      p.all = "1";
      if (ids.size) p.exclude = [...ids].join(",");
      else if (job?.exclude) del.push("exclude");
      if (job?.vmid) del.push("vmid");
    } else {
      p.vmid = [...ids].join(",");
      if (job?.all) p.all = "0";
      if (job?.exclude) del.push("exclude");
    }
    if (job && !comment.trim() && job.comment) del.push("comment");
    if (job && del.length) p.delete = del.join(",");
    const call = job ? { method: "set" as const, path: `/cluster/backup/${job.id}`, params: p } : { method: "create" as const, path: "/cluster/backup", params: p };
    void run(call).then((ok) => ok && onClose());
  };
  return (
    <FormModal title={job ? `Back-upjob ${job.comment || job.id}` : "Nieuwe back-upjob"} onClose={onClose} onSubmit={submit} submitLabel="Opslaan" disabled={pending} error={error} wide>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Naam" htmlFor="bj-c">
          <Input id="bj-c" value={comment} onChange={(e) => setComment(e.target.value)} placeholder="Nachtelijk naar PBS" />
        </Field>
        <Field label="Wanneer" htmlFor="bj-s" hint="02:00 = elke dag; sat 03:00; mon..fri 22:30">
          <Input id="bj-s" value={schedule} onChange={(e) => setSchedule(e.target.value)} />
        </Field>
        <Field label="Naar" htmlFor="bj-st">
          <Select id="bj-st" value={storage} onChange={(e) => setStorage(e.target.value)}>
            {stores.map((s) => (
              <option key={`${s.node}/${s.storage}`} value={s.storage}>
                {s.storage}
                {s.type ? ` (${s.type})` : ""}
                {s.shared ? "" : ` – alleen op ${s.node}`}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Modus" htmlFor="bj-m">
          <Select id="bj-m" value={mode} onChange={(e) => setMode(e.target.value)}>
            <option value="snapshot">Snapshot (blijft draaien)</option>
            <option value="suspend">Pauzeren</option>
            <option value="stop">Stoppen</option>
          </Select>
        </Field>
      </div>
      <fieldset className="space-y-2">
        <legend className="text-sm font-medium">Welke gasten</legend>
        <div className="flex flex-wrap gap-4 text-sm">
          <label className="flex items-center gap-2">
            <input type="radio" name="bj-sel" checked={sel === "all"} onChange={() => setSel("all")} /> Alle, ook nieuwe
          </label>
          <label className="flex items-center gap-2">
            <input type="radio" name="bj-sel" checked={sel === "some"} onChange={() => setSel("some")} /> Alleen deze
          </label>
        </div>
        <p className="text-xs text-slate-500">{sel === "all" ? "Aangevinkt = overslaan." : "Aangevinkt = meenemen."}</p>
        <div className="grid max-h-48 gap-1 overflow-y-auto rounded border border-slate-200 p-2 text-sm sm:grid-cols-2 dark:border-slate-800">
          {guests.map((g) => (
            <label key={g.vmid} className="flex items-center gap-2">
              <input type="checkbox" checked={ids.has(String(g.vmid))} onChange={() => toggle(String(g.vmid))} />
              <span className="truncate">
                {guestLabel(g.type)} {g.vmid} {g.name}
              </span>
            </label>
          ))}
        </div>
      </fieldset>
      <fieldset>
        <legend className="mb-1 text-sm font-medium">Bewaren</legend>
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          {keepKeys.map((k) => (
            <Field key={k.key} label={k.label} htmlFor={`bj-${k.key}`}>
              <Input id={`bj-${k.key}`} inputMode="numeric" value={keep[k.key] ?? ""} onChange={(e) => setKeep({ ...keep, [k.key]: e.target.value })} />
            </Field>
          ))}
        </div>
        <p className="mt-1 text-xs text-slate-500">Alles leeg: alles bewaren. Beschermde back-ups ruimt Proxmox nooit op.</p>
      </fieldset>
      <Field label="Notitie bij elke back-up" htmlFor="bj-n" hint="{{guestname}}, {{vmid}}, {{node}}, {{cluster}}">
        <Input id="bj-n" value={notes} onChange={(e) => setNotes(e.target.value)} />
      </Field>
      <Check id="bj-en" checked={enabled} onChange={setEnabled}>
        Aan
      </Check>
    </FormModal>
  );
}
