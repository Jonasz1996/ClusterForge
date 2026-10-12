"use client";

import Link from "next/link";
import { useState } from "react";
import { Modal } from "@/components/Modal";
import { ago, formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, Field, Input } from "@/components/ui";
import { backupTimeFmt } from "@/lib/backups";
import {
  dumpFreshness,
  dumpStatus,
  useComponentDumps,
  useDatabaseBackups,
  useDumpNow,
  useRestoreDump,
  useSetBackup,
  type ClusterComponent,
  type ComponentBackupSettings,
  type ComponentDump,
} from "@/lib/components";

const linkClass = "text-brand-600 hover:underline dark:text-brand-500";
const none = <span className="text-slate-400">–</span>;

// DumpsCard toont de dumps van de databases van een onderdeel: per
// database de nieuwste goede dump, 'Dump nu', alle dumps en terugzetten.
export function DumpsCard({ clusterId, comp, isAdmin }: { clusterId: string; comp: ClusterComponent; isAdmin: boolean }) {
  const q = useComponentDumps(clusterId, comp.id);
  const now = useDumpNow(clusterId, comp.id);
  const [dialog, setDialog] = useState<{ kind: "settings" } | { kind: "restore"; dump: ComponentDump } | null>(null);
  const [all, setAll] = useState(false);
  const d = q.data;
  const busy = d?.active ?? null;
  const dumps = d ? (all ? d.dumps : d.dumps.slice(0, 15)) : [];
  return (
    <div id="dumps">
      <Card
        title={
          <span className="flex flex-wrap items-start justify-between gap-2">
            <span>Dumps</span>
            {isAdmin && d && (
              <Button variant="secondary" onClick={() => setDialog({ kind: "settings" })}>
                Instellen
              </Button>
            )}
          </span>
        }
      >
        {q.isLoading ? (
          <p className="text-sm text-slate-500">Laden…</p>
        ) : q.isError ? (
          <Alert>{q.error.message}</Alert>
        ) : (
          d && (
            <div className="space-y-5 text-sm">
              <p className="text-slate-600 dark:text-slate-400">
                <Schedule s={d.settings} /> De dumps staan op de nodes in <code>/var/backups/clusterforge-db</code>, alleen
                leesbaar voor root, en gaan mee in de Proxmox-back-up van de node.
              </p>
              {busy && (
                <Alert kind="info">
                  Bezig: {busy.title}.{" "}
                  <Link href={`/taken/detail?id=${busy.job_id}`} className="underline">
                    Bekijk de taak
                  </Link>
                </Alert>
              )}
              {now.isError && <Alert>{now.error.message}</Alert>}
              {d.databases.length === 0 ? (
                <p className="text-slate-500">Er staat nog geen database in, dus ook geen dump.</p>
              ) : (
                <div className="overflow-x-auto">
                  <table className={tableClass}>
                    <thead>
                      <tr>
                        <th className={thClass}>Database</th>
                        <th className={thClass}>Nieuwste goede dump</th>
                        <th className={thClass}>Grootte</th>
                        <th className={thClass}>Node</th>
                        <th className={thClass}>Stand</th>
                        {isAdmin && <th className={thClass} />}
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                      {d.databases.map((db) => {
                        const f = dumpFreshness[db.freshness];
                        return (
                          <tr key={db.database}>
                            <td className={tdClass + " font-medium"}>{db.database}</td>
                            <td className={tdClass}>
                              {db.latest ? ago(db.latest.finished_at ?? db.latest.started_at) : "nog geen"}
                              {db.failed && (
                                <span className="block min-w-56 text-xs text-red-700 dark:text-red-400">
                                  Laatste poging {ago(db.failed.started_at)} {dumpStatus[db.failed.status].label}
                                  {db.failed.message && `: ${db.failed.message}`}
                                </span>
                              )}
                            </td>
                            <td className={tdClass}>{db.latest ? formatBytes(db.latest.size_bytes) : none}</td>
                            <td className={tdClass}>{db.latest ? db.latest.node : none}</td>
                            <td className={tdClass}>
                              <Badge tone={f.tone}>{f.label}</Badge>
                            </td>
                            {isAdmin && (
                              <td className={tdClass + " text-right whitespace-nowrap"}>
                                <Button
                                  variant="ghost"
                                  disabled={busy !== null || now.isPending}
                                  onClick={() => now.mutate(db.database)}
                                >
                                  Dump nu
                                </Button>
                              </td>
                            )}
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              )}
              <div>
                <h3 className="mb-1 font-medium">Alle dumps</h3>
                {d.dumps.length === 0 ? (
                  <p className="text-slate-500">Nog geen dump gemaakt.</p>
                ) : (
                  <div className="overflow-x-auto">
                    <table className={tableClass}>
                      <thead>
                        <tr>
                          <th className={thClass}>Gemaakt</th>
                          <th className={thClass}>Database</th>
                          <th className={thClass}>Node</th>
                          <th className={thClass}>Grootte</th>
                          <th className={thClass}>Tabellen</th>
                          <th className={thClass}>Controle</th>
                          {isAdmin && <th className={thClass} />}
                        </tr>
                      </thead>
                      <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                        {dumps.map((x) => (
                          <DumpRow
                            key={x.id}
                            x={x}
                            isAdmin={isAdmin}
                            busy={busy !== null}
                            onRestore={() => setDialog({ kind: "restore", dump: x })}
                          />
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
                {d.dumps.length > 15 && (
                  <Button variant="ghost" className="mt-1" onClick={() => setAll(!all)}>
                    {all ? "Minder tonen" : `Alle ${d.dumps.length} tonen`}
                  </Button>
                )}
              </div>
            </div>
          )
        )}
      </Card>
      {d && dialog?.kind === "settings" && (
        <SettingsDialog clusterId={clusterId} comp={comp} s={d.settings} onClose={() => setDialog(null)} />
      )}
      {dialog?.kind === "restore" && (
        <RestoreDialog clusterId={clusterId} comp={comp} dump={dialog.dump} onClose={() => setDialog(null)} />
      )}
    </div>
  );
}

function Schedule({ s }: { s: ComponentBackupSettings }) {
  if (!s.on) return <>De nachtelijke dump staat uit; Dump nu werkt wel.</>;
  return (
    <>
      Elke dag om {s.at} een dump van elke database, {s.keep_days} {s.keep_days === 1 ? "dag" : "dagen"} bewaard
      {s.next_at ? `; de volgende ${backupTimeFmt.format(new Date(s.next_at))}` : ""}. De nieuwste goede dump van een database
      blijft altijd staan.
    </>
  );
}

function DumpRow({ x, isAdmin, busy, onRestore }: { x: ComponentDump; isAdmin: boolean; busy: boolean; onRestore: () => void }) {
  const st = dumpStatus[x.status];
  const usable = x.status === "ok" || x.status === "suspect";
  return (
    <tr>
      <td className={tdClass + " whitespace-nowrap"}>
        {backupTimeFmt.format(new Date(x.started_at))}
        <span className="block text-xs text-slate-500">{x.trigger === "manual" ? "met Dump nu" : "nachtelijk"}</span>
      </td>
      <td className={tdClass}>{x.database}</td>
      <td className={tdClass}>{x.node}</td>
      <td className={tdClass + " whitespace-nowrap"}>
        {x.status === "running" || x.status === "failed" ? none : formatBytes(x.size_bytes)}
        {usable && x.raw_bytes > 0 && <span className="block text-xs text-slate-500">{formatBytes(x.raw_bytes)} SQL</span>}
      </td>
      <td className={tdClass}>
        {!usable ? none : x.tables >= x.tables_expected ? x.tables : `${x.tables} van ${x.tables_expected}`}
      </td>
      <td className={tdClass}>
        <Badge tone={st.tone}>{st.label}</Badge>
        {x.message && <span className="block max-w-xs min-w-56 text-xs text-slate-500">{x.message}</span>}
      </td>
      {isAdmin && (
        <td className={tdClass + " text-right whitespace-nowrap"}>
          {usable && (
            <Button variant="ghost" disabled={busy} onClick={onRestore}>
              Terugzetten
            </Button>
          )}
        </td>
      )}
    </tr>
  );
}

function SettingsDialog({
  clusterId,
  comp,
  s,
  onClose,
}: {
  clusterId: string;
  comp: ClusterComponent;
  s: ComponentBackupSettings;
  onClose: () => void;
}) {
  const set = useSetBackup(clusterId, comp.id);
  const [on, setOn] = useState(s.on);
  const [at, setAt] = useState(s.at);
  const [keep, setKeep] = useState(String(s.keep_days));
  return (
    <Modal title="Nachtelijke dump instellen" onClose={onClose}>
      <form
        className="space-y-4 text-sm"
        onSubmit={(e) => {
          e.preventDefault();
          set.mutate({ on, at, keep_days: Number(keep) }, { onSuccess: onClose });
        }}
      >
        <label className="flex items-start gap-2">
          <input type="checkbox" className="mt-0.5" checked={on} onChange={(e) => setOn(e.target.checked)} />
          <span>
            Elke dag een dump van elke database
            <span className="block text-xs text-slate-500">
              {comp.engine === "postgresql"
                ? "Op de primary: op een replica breekt PostgreSQL een lange dump af zodra de primary iets opruimt dat de dump nog leest."
                : "Op een node zonder het VIP, zodat de node waar de app naar schrijft geen extra werk krijgt."}
            </span>
          </span>
        </label>
        <Field
          label="Om"
          htmlFor="dump-at"
          hint="Uu:mm in de tijdzone van ClusterForge. Standaard 02:00, buiten het testvenster."
        >
          <Input id="dump-at" inputMode="numeric" placeholder="02:00" value={at} onChange={(e) => setAt(e.target.value)} />
        </Field>
        <Field label="Bewaren" htmlFor="dump-keep" hint="Dagen, van 1 tot 90. De nieuwste goede dump blijft altijd staan.">
          <Input id="dump-keep" type="number" min={1} max={90} value={keep} onChange={(e) => setKeep(e.target.value)} />
        </Field>
        {set.isError && <Alert>{set.error.message}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" disabled={set.isPending || at === "" || keep === ""}>
            {set.isPending ? "Bezig…" : "Bewaren"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function RestoreDialog({
  clusterId,
  comp,
  dump,
  onClose,
}: {
  clusterId: string;
  comp: ClusterComponent;
  dump: ComponentDump;
  onClose: () => void;
}) {
  const restore = useRestoreDump(clusterId, comp.id);
  const [target, setTarget] = useState("");
  const done = restore.data;
  return (
    <Modal title={`Dump van ${dump.database} terugzetten`} onClose={onClose}>
      {done ? (
        <div className="space-y-4 text-sm">
          <p>
            {dump.node} zet de dump terug in <strong>{done.target}</strong>. Als de taak klaar is, staat {done.target} bij
            Databases op deze pagina.
          </p>
          <div className="flex justify-end gap-2">
            <Link href={`/taken/detail?id=${done.job.id}`} className={linkClass + " self-center"}>
              Bekijk de taak
            </Link>
            <Button onClick={onClose}>Klaar</Button>
          </div>
        </div>
      ) : (
        <form
          className="space-y-4 text-sm"
          onSubmit={(e) => {
            e.preventDefault();
            restore.mutate({ dumpId: dump.id, target });
          }}
        >
          <p>
            De dump van {backupTimeFmt.format(new Date(dump.started_at))} op {dump.node} ({formatBytes(dump.size_bytes)}) komt in
            een nieuwe database naast {dump.database}. {dump.database} zelf blijft zoals ze is, en de apps merken er niets van.
          </p>
          {dump.status === "suspect" && <Alert>Deze dump is verdacht: {dump.message}</Alert>}
          <Field label="Nieuwe database" htmlFor="restore-target" hint="Kleine letters, cijfers en _. Ze mag nog niet bestaan.">
            <Input
              id="restore-target"
              value={target}
              placeholder={dump.restore_name}
              onChange={(e) => setTarget(e.target.value)}
              autoFocus
            />
          </Field>
          {comp.engine === "postgresql" ? (
            <p className="text-slate-600 dark:text-slate-400">
              PostgreSQL schrijft alleen op de primary. ClusterForge maakt daar een tijdelijke beheerder, de agent op {dump.node}{" "}
              zet de dump ermee over het netwerk terug, en daarna is de beheerder weer weg. De nieuwe database krijgt dezelfde
              eigenaar als {dump.database}. Daarna staat ze bij Databases, met haar grootte en tabellen, zodat je ze kunt
              vergelijken; als root op de primary kun je erin kijken met{" "}
              <code>runuser -u postgres -- psql {target || dump.restore_name}</code>. Gebruikers zitten niet in een dump. Weghalen
              kan bij Databases, met de naam.
            </p>
          ) : (
            <p className="text-slate-600 dark:text-slate-400">
              Daarna staat ze bij Databases, met haar grootte en tabellen, zodat je ze kunt vergelijken; als root op een node kun
              je erin kijken met <code>mariadb {target || dump.restore_name}</code>. Gebruikers zitten niet in een dump. Weghalen
              kan bij Databases, met de naam.
            </p>
          )}
          {restore.isError && <Alert>{restore.error.message}</Alert>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={onClose}>
              Annuleren
            </Button>
            <Button type="submit" disabled={restore.isPending}>
              {restore.isPending ? "Bezig…" : "Terugzetten"}
            </Button>
          </div>
        </form>
      )}
    </Modal>
  );
}

// DatabaseBackupsCard toont bij Back-ups per database van elk cluster de
// leeftijd van de nieuwste goede dump.
export function DatabaseBackupsCard() {
  const q = useDatabaseBackups();
  const items = q.data?.items ?? [];
  if (q.isLoading || (q.isSuccess && items.length === 0)) return null;
  return (
    <Card title="Databases">
      {q.isError ? (
        <Alert>{q.error.message}</Alert>
      ) : (
        <div className="space-y-3 text-sm">
          <p className="text-slate-600 dark:text-slate-400">
            De nachtelijke dump van elke database van een cluster met een database van ClusterForge. Zo&apos;n dump zet je terug
            naast de bestaande database, zonder de hele VM.
          </p>
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Cluster</th>
                  <th className={thClass}>Database</th>
                  <th className={thClass}>Nieuwste goede dump</th>
                  <th className={thClass}>Grootte</th>
                  <th className={thClass}>Stand</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {items.map((it) => {
                  const f = dumpFreshness[it.freshness];
                  return (
                    <tr key={`${it.component_id}/${it.database}`}>
                      <td className={tdClass}>
                        <Link
                          href={`/clusters/databases?id=${it.cluster_id}&component=${it.component_id}#dumps`}
                          className={linkClass}
                        >
                          {it.cluster_name}
                        </Link>
                      </td>
                      <td className={tdClass + " font-medium"}>{it.database}</td>
                      <td className={tdClass}>
                        {it.latest ? `${ago(it.latest.finished_at ?? it.latest.started_at)} op ${it.latest.node}` : "nog geen"}
                        {it.failed && (
                          <span className="block min-w-56 text-xs text-red-700 dark:text-red-400">
                            Laatste poging {dumpStatus[it.failed.status].label}
                            {it.failed.message && `: ${it.failed.message}`}
                          </span>
                        )}
                      </td>
                      <td className={tdClass}>{it.latest ? formatBytes(it.latest.size_bytes) : none}</td>
                      <td className={tdClass}>
                        <Badge tone={f.tone}>{f.label}</Badge>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </Card>
  );
}
