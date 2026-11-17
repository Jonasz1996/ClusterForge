"use client";

import { Suspense, useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { CallForm, keepFields, PbsGate, scheduleHint, usePbsRunner, when, type FormField } from "@/components/pbs/bits";
import { ActionMenu } from "@/components/pve/bits";
import { Tile as Win, TileGrid } from "@/components/Tiles";
import { Badge, Button, Card, cx, PageHeader } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { usePbsGet, type PbsServer } from "@/lib/pbs";

export default function PbsJobsPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Jobs"
        description="Wat de Backup Server vanzelf doet: garbage collection, prune (oude back-ups weg), verificatie en sync naar of van een andere PBS. Met de laatste uitkomst, de volgende keer en Nu uitvoeren."
      />
      <Suspense>
        <PbsGate>{(s) => <JobsView key={s.node_id} srv={s} />}</PbsGate>
      </Suspense>
    </div>
  );
}

type Job = Record<string, string | number | boolean | undefined> & {
  id: string;
  store: string;
  schedule?: string;
  disable?: boolean;
  comment?: string;
  ns?: string;
  "last-run-state"?: string;
  "last-run-endtime"?: number;
  "next-run"?: number;
};

type Kind = "prune" | "verify" | "sync";

function JobsView({ srv }: { srv: PbsServer }) {
  return (
    <TileGrid>
      <Win wide>
        <GCCard srv={srv} />
      </Win>
      <Win wide>
        <JobCard srv={srv} kind="prune" />
      </Win>
      <Win wide>
        <JobCard srv={srv} kind="verify" />
      </Win>
      <Win wide>
        <JobCard srv={srv} kind="sync" />
      </Win>
      <Win>
        <RemotesCard srv={srv} />
      </Win>
      <Win>
        <TrafficCard srv={srv} />
      </Win>
    </TileGrid>
  );
}

function LastRun({ j }: { j: { "last-run-state"?: string; "last-run-endtime"?: number } }) {
  const st = j["last-run-state"];
  if (!st) return <span className="text-slate-500">nog niet</span>;
  const bad = st !== "OK" && !st.startsWith("WARNINGS");
  return (
    <span className="whitespace-nowrap">
      <Badge tone={bad ? "red" : st === "OK" ? "green" : "amber"}>{st.length > 36 ? st.slice(0, 36) + "…" : st}</Badge>
      <span className="ml-2 text-xs text-slate-500">{when(j["last-run-endtime"])}</span>
    </span>
  );
}

function GCCard({ srv }: { srv: PbsServer }) {
  const id = srv.node_id;
  const isAdmin = useIsAdmin();
  const q = usePbsGet<Job[]>(id, "/admin/gc");
  const { run, pending, banner } = usePbsRunner(id);
  const [edit, setEdit] = useState<Job | null>(null);
  return (
    <Card title="Garbage collection">
      <div className="space-y-3">
        <p className="text-sm text-slate-500">Geeft de ruimte van verwijderde of geprunede back-ups terug. Per datastore één schema.</p>
        {banner}
        <QueryState q={q}>
          {(q.data ?? []).length === 0 ? (
            <Empty>Geen datastores.</Empty>
          ) : (
            <div className="relative -mx-4 overflow-x-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Datastore</th>
                    <th className={thClass}>Schema</th>
                    <th className={thClass}>Laatste keer</th>
                    <th className={cx(thClass, "hidden md:table-cell")}>Volgende</th>
                    {isAdmin && <th className={thClass} />}
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {(q.data ?? []).map((j) => (
                    <tr key={j.store}>
                      <td className={cx(tdClass, "font-medium")}>{j.store}</td>
                      <td className={cx(tdClass, "font-mono text-xs")}>{j.schedule || <span className="text-amber-600">geen</span>}</td>
                      <td className={tdClass}>
                        <LastRun j={j} />
                      </td>
                      <td className={cx(tdClass, "hidden whitespace-nowrap text-slate-500 md:table-cell")}>{when(j["next-run"])}</td>
                      {isAdmin && (
                        <td className={cx(tdClass, "text-right")}>
                          <ActionMenu
                            disabled={pending}
                            items={[
                              { label: "Nu uitvoeren", onClick: () => void run({ method: "create", path: `/admin/datastore/${j.store}/gc` }) },
                              { label: "Schema…", onClick: () => setEdit(j) },
                            ]}
                          />
                        </td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </QueryState>
      </div>
      {edit && (
        <CallForm
          id={id}
          title={`Schema garbage collection ${edit.store}`}
          fields={[{ key: "gc-schedule", label: "Schema", placeholder: "daily", hint: scheduleHint }]}
          initial={{ "gc-schedule": edit.schedule ?? "" }}
          method="set"
          path={`/config/datastore/${edit.store}`}
          submitLabel="Opslaan"
          onClose={() => setEdit(null)}
        />
      )}
    </Card>
  );
}

const kindInfo: Record<Kind, { title: string; intro: string; word: string }> = {
  prune: { title: "Prune-jobs", intro: "Halen oude snapshots weg volgens de bewaarregels.", word: "prune-job" },
  verify: { title: "Verify-jobs", intro: "Lezen elke back-up terug en controleren de checksums, zodat een kapotte back-up opvalt voor je hem nodig hebt.", word: "verify-job" },
  sync: { title: "Sync-jobs", intro: "Halen back-ups op van een andere PBS (pull) of sturen ze erheen (push), voor een kopie buiten huis.", word: "sync-job" },
};

function fieldsFor(kind: Kind, srv: PbsServer, remotes: string[]): FormField[] {
  const stores = srv.datastores.map((d) => ({ value: d, label: d }));
  const base: FormField[] = [
    { key: "id", label: "Naam", required: true, createOnly: true, placeholder: `${kind}-1` },
    { key: "store", label: kind === "sync" ? "Lokale datastore" : "Datastore", type: "select", options: stores, createOnly: true },
    { key: "ns", label: "Namespace", placeholder: "leeg = hoofd" },
    { key: "schedule", label: "Schema", placeholder: kind === "verify" ? "sat 05:00" : "daily", hint: scheduleHint },
  ];
  if (kind === "prune") return [...base, { key: "max-depth", label: "Diepte namespaces", type: "number", hint: "Leeg = alle onderliggende" }, ...keepFields, { key: "comment", label: "Omschrijving" }];
  if (kind === "verify")
    return [
      ...base,
      { key: "ignore-verified", label: "Al geverifieerde overslaan", type: "checkbox" },
      { key: "outdated-after", label: "Toch opnieuw na (dagen)", type: "number", hint: "Een geverifieerde back-up opnieuw controleren na zoveel dagen" },
      { key: "comment", label: "Omschrijving" },
    ];
  return [
    ...base,
    { key: "remote", label: "Remote", type: "select", options: [{ value: "", label: "(lokaal: andere datastore op deze PBS)" }, ...remotes.map((r) => ({ value: r, label: r }))] },
    { key: "remote-store", label: "Datastore op de remote", required: true },
    { key: "remote-ns", label: "Namespace op de remote" },
    {
      key: "sync-direction",
      label: "Richting",
      type: "select",
      options: [
        { value: "", label: "Ophalen (pull)" },
        { value: "push", label: "Sturen (push, PBS 4)" },
      ],
    },
    { key: "remove-vanished", label: "Verdwenen back-ups ook hier weghalen", type: "checkbox" },
    { key: "transfer-last", label: "Alleen de laatste N per groep", type: "number" },
    { key: "group-filter", label: "Groepfilter", placeholder: "type:vm of group:vm/101 of regex:^vm/1" },
    { key: "rate-in", label: "Bandbreedte (zoals 50MiB)" },
    { key: "comment", label: "Omschrijving" },
  ];
}

function JobCard({ srv, kind }: { srv: PbsServer; kind: Kind }) {
  const id = srv.node_id;
  const isAdmin = useIsAdmin();
  const info = kindInfo[kind];
  const q = usePbsGet<Job[]>(id, `/admin/${kind}`);
  const remotes = usePbsGet<{ name: string }[]>(id, "/config/remote", { enabled: kind === "sync" && isAdmin, refetch: 60_000 });
  const { run, pending, banner } = usePbsRunner(id);
  const [dialog, setDialog] = useState<{ job?: Job } | null>(null);
  const fields = fieldsFor(kind, srv, (remotes.data ?? []).map((r) => r.name));
  const initialOf = (j: Job) => {
    const out: Record<string, string> = {};
    for (const f of fields) {
      const v = j[f.key];
      if (v === undefined) continue;
      out[f.key] = typeof v === "boolean" ? (v ? "1" : "0") : String(v);
    }
    return out;
  };
  return (
    <Card title={info.title}>
      <div className="space-y-3">
        <p className="text-sm text-slate-500">{info.intro}</p>
        {banner}
        {isAdmin && (
          <div className="flex justify-end">
            <Button variant="secondary" onClick={() => setDialog({})}>
              Nieuwe {info.word}
            </Button>
          </div>
        )}
        <QueryState q={q}>
          {(q.data ?? []).length === 0 ? (
            <Empty>Nog geen {info.word}.</Empty>
          ) : (
            <div className="relative -mx-4 overflow-x-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Job</th>
                    <th className={cx(thClass, "hidden sm:table-cell")}>Schema</th>
                    <th className={thClass}>Laatste keer</th>
                    <th className={cx(thClass, "hidden md:table-cell")}>Volgende</th>
                    {isAdmin && <th className={thClass} />}
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {(q.data ?? []).map((j) => (
                    <tr key={j.id}>
                      <td className={tdClass}>
                        <span className="font-medium">{j.id}</span>
                        <span className="ml-1 text-xs text-slate-500">
                          {j.store}
                          {j.ns ? ` / ${j.ns}` : ""}
                          {kind === "sync" && ` ${j["sync-direction"] === "push" ? "→" : "←"} ${j.remote || "lokaal"}:${j["remote-store"] ?? ""}`}
                        </span>
                        {j.disable && (
                          <span className="ml-2">
                            <Badge>uit</Badge>
                          </span>
                        )}
                        {j.comment && <span className="block text-xs text-slate-500">{j.comment}</span>}
                      </td>
                      <td className={cx(tdClass, "hidden font-mono text-xs sm:table-cell")}>{j.schedule || "–"}</td>
                      <td className={tdClass}>
                        <LastRun j={j} />
                      </td>
                      <td className={cx(tdClass, "hidden whitespace-nowrap text-slate-500 md:table-cell")}>{when(j["next-run"])}</td>
                      {isAdmin && (
                        <td className={cx(tdClass, "text-right")}>
                          <ActionMenu
                            disabled={pending}
                            items={[
                              { label: "Nu uitvoeren", onClick: () => void run({ method: "create", path: `/admin/${kind}/${j.id}/run` }) },
                              { label: "Wijzigen…", onClick: () => setDialog({ job: j }) },
                              {
                                label: j.disable ? "Aanzetten" : "Uitzetten",
                                onClick: () => void run({ method: "set", path: `/config/${kind}/${j.id}`, params: { disable: j.disable ? "0" : "1" } }),
                              },
                              {
                                label: "Verwijderen",
                                danger: true,
                                onClick: async () => {
                                  if (await askConfirm(`${info.word} ${j.id} verwijderen? De back-ups zelf blijven.`, { danger: true, confirmLabel: "Verwijderen" }))
                                    void run({ method: "delete", path: `/config/${kind}/${j.id}` });
                                },
                              },
                            ]}
                          />
                        </td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </QueryState>
      </div>
      {dialog && (
        <CallForm
          id={id}
          title={dialog.job ? `${info.word} ${dialog.job.id} wijzigen` : `Nieuwe ${info.word}`}
          fields={fields}
          initial={
            dialog.job
              ? initialOf(dialog.job)
              : {
                  store: srv.datastores[0] ?? "",
                  schedule: kind === "verify" ? "sat 05:00" : "daily",
                  ...(kind === "prune" ? { "keep-last": "3", "keep-daily": "7", "keep-weekly": "4", "keep-monthly": "6" } : {}),
                  ...(kind === "verify" ? { "ignore-verified": "1", "outdated-after": "30" } : {}),
                  ...(kind === "sync" ? { remote: (remotes.data ?? [])[0]?.name ?? "" } : {}),
                }
          }
          method={dialog.job ? "set" : "create"}
          path={dialog.job ? `/config/${kind}/${dialog.job.id}` : `/config/${kind}`}
          submitLabel={dialog.job ? "Opslaan" : "Aanmaken"}
          onClose={() => setDialog(null)}
        />
      )}
    </Card>
  );
}

type Remote = { name: string; host: string; port?: number; "auth-id": string; fingerprint?: string; comment?: string };

const remoteFields: FormField[] = [
  { key: "name", label: "Naam", required: true, createOnly: true, placeholder: "offsite" },
  { key: "host", label: "Adres", required: true, placeholder: "pbs2.example.org of 10.0.0.9" },
  { key: "port", label: "Poort", type: "number", placeholder: "8007" },
  { key: "auth-id", label: "Gebruiker of API-token", required: true, placeholder: "sync@pbs of sync@pbs!cf" },
  { key: "password", label: "Wachtwoord of geheim van het token", type: "password", hint: "Gaat versleuteld naar de taak en komt nooit in een log. Leeg bij wijzigen = ongewijzigd." },
  { key: "fingerprint", label: "Fingerprint van het certificaat", hint: "Nodig bij een zelfondertekend certificaat; staat op het Overzicht van die PBS." },
  { key: "comment", label: "Omschrijving" },
];

function RemotesCard({ srv }: { srv: PbsServer }) {
  const id = srv.node_id;
  const isAdmin = useIsAdmin();
  const q = usePbsGet<Remote[]>(id, "/config/remote", { enabled: isAdmin, refetch: 60_000 });
  const { run, pending, banner } = usePbsRunner(id);
  const [dialog, setDialog] = useState<{ r?: Remote } | null>(null);
  if (!isAdmin) return null;
  return (
    <Card title="Remotes">
      <div className="space-y-3">
        <p className="text-sm text-slate-500">Andere Backup Servers waarmee sync-jobs praten.</p>
        {banner}
        <div className="flex justify-end">
          <Button variant="secondary" onClick={() => setDialog({})}>
            Remote toevoegen
          </Button>
        </div>
        <QueryState q={q}>
          {(q.data ?? []).length === 0 ? (
            <Empty>Nog geen remote.</Empty>
          ) : (
            <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
              {(q.data ?? []).map((r) => (
                <li key={r.name} className="flex items-center gap-2 py-2">
                  <div className="min-w-0 flex-1">
                    <span className="font-medium">{r.name}</span>
                    <span className="ml-2 text-slate-500">
                      {r["auth-id"]} @ {r.host}
                      {r.port ? ":" + r.port : ""}
                    </span>
                  </div>
                  <ActionMenu
                    disabled={pending}
                    items={[
                      { label: "Wijzigen…", onClick: () => setDialog({ r }) },
                      {
                        label: "Verwijderen",
                        danger: true,
                        onClick: async () => {
                          if (await askConfirm(`Remote ${r.name} verwijderen? Sync-jobs die hem gebruiken werken dan niet meer.`, { danger: true, confirmLabel: "Verwijderen" }))
                            void run({ method: "delete", path: `/config/remote/${r.name}` });
                        },
                      },
                    ]}
                  />
                </li>
              ))}
            </ul>
          )}
        </QueryState>
      </div>
      {dialog && (
        <CallForm
          id={id}
          title={dialog.r ? `Remote ${dialog.r.name} wijzigen` : "Remote toevoegen"}
          fields={remoteFields.map((f) => (f.key === "password" && !dialog.r ? { ...f, required: true } : f))}
          initial={
            dialog.r
              ? { host: dialog.r.host, port: dialog.r.port ? String(dialog.r.port) : "", "auth-id": dialog.r["auth-id"], fingerprint: dialog.r.fingerprint ?? "", comment: dialog.r.comment ?? "" }
              : {}
          }
          method={dialog.r ? "set" : "create"}
          path={dialog.r ? `/config/remote/${dialog.r.name}` : "/config/remote"}
          submitLabel={dialog.r ? "Opslaan" : "Toevoegen"}
          onClose={() => setDialog(null)}
        />
      )}
    </Card>
  );
}

type Traffic = { name: string; "rate-in"?: string; "rate-out"?: string; network?: string[]; timeframe?: string[]; comment?: string };

function TrafficCard({ srv }: { srv: PbsServer }) {
  const id = srv.node_id;
  const isAdmin = useIsAdmin();
  const q = usePbsGet<Traffic[]>(id, "/config/traffic-control", { refetch: 60_000 });
  const { run, pending, banner } = usePbsRunner(id);
  const [open, setOpen] = useState(false);
  return (
    <Card title="Bandbreedte">
      <div className="space-y-3">
        <p className="text-sm text-slate-500">Beperk back-ups en sync per netwerk en tijdstip, zodat je internet overdag vrij blijft.</p>
        {banner}
        {isAdmin && (
          <div className="flex justify-end">
            <Button variant="secondary" onClick={() => setOpen(true)}>
              Regel toevoegen
            </Button>
          </div>
        )}
        <QueryState q={q}>
          {(q.data ?? []).length === 0 ? (
            <Empty>Geen regels: alles op volle snelheid.</Empty>
          ) : (
            <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
              {(q.data ?? []).map((t) => (
                <li key={t.name} className="flex items-center gap-2 py-2">
                  <div className="min-w-0 flex-1">
                    <span className="font-medium">{t.name}</span>
                    <span className="ml-2 text-slate-500">
                      in {t["rate-in"] ?? "∞"} · uit {t["rate-out"] ?? "∞"} · {(t.network ?? []).join(", ")}
                      {t.timeframe?.length ? ` · ${t.timeframe.join(", ")}` : ""}
                    </span>
                  </div>
                  {isAdmin && (
                    <Button
                      variant="secondary-danger"
                      disabled={pending}
                      onClick={async () => {
                        if (await askConfirm(`Regel ${t.name} verwijderen?`, { danger: true, confirmLabel: "Verwijderen" })) void run({ method: "delete", path: `/config/traffic-control/${t.name}` });
                      }}
                    >
                      Weg
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </QueryState>
      </div>
      {open && (
        <CallForm
          id={id}
          title="Bandbreedteregel"
          fields={[
            { key: "name", label: "Naam", required: true, placeholder: "overdag" },
            { key: "network", label: "Netwerk", required: true, placeholder: "0.0.0.0/0 of 192.168.10.0/24" },
            { key: "rate-in", label: "Inkomend", placeholder: "50MiB" },
            { key: "rate-out", label: "Uitgaand", placeholder: "20MiB" },
            { key: "timeframe", label: "Tijdvenster", placeholder: "mon..fri 08:00-18:00", hint: "Leeg = altijd" },
            { key: "comment", label: "Omschrijving" },
          ]}
          initial={{ network: "0.0.0.0/0" }}
          method="create"
          path="/config/traffic-control"
          submitLabel="Toevoegen"
          onClose={() => setOpen(false)}
        />
      )}
    </Card>
  );
}
