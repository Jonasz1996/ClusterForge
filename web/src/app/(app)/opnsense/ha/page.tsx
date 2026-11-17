"use client";

import Link from "next/link";
import { Suspense, useState, type ReactNode } from "react";
import { formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ActionDialog, ClusterStrip, JobBanner, OpnGate } from "@/components/opnsense/bits";
import { Modal } from "@/components/Modal";
import { Tile as Win, TileGrid } from "@/components/Tiles";
import { Alert, Badge, Button, Card, Field, Input, PageHeader, Select } from "@/components/ui";
import { useMe } from "@/lib/auth";
import {
  downloadOpnBackup,
  readNode,
  roleLabel,
  useOpnBackupNow,
  useOpnBackups,
  useOpnCarp,
  useOpnDrift,
  useOpnFirmware,
  useOpnGet,
  type OpnCluster,
  type OpnFirewall,
} from "@/lib/opnsense";
import type { Job } from "@/lib/proxmox";

export default function OpnHAPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="HA en firmware"
        description="Onderhoudsmodus en een failovertest voor CARP, firmware-updates in de juiste volgorde (eerst de backup, dan de master terwijl de backup het verkeer heeft), wat er tussen master en backup verschilt, en de back-ups van config.xml die ClusterForge elke dag en na elke wijziging versleuteld bewaart."
      />
      <Suspense>
        <OpnGate>{(c) => <HA key={c.key} c={c} />}</OpnGate>
      </Suspense>
    </div>
  );
}

type Result = { kind: "success"; job: Job } | { kind: "error"; message: string } | null;

function HA({ c }: { c: OpnCluster }) {
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const [result, setResult] = useState<Result>(null);
  const done = (job: Job) => setResult({ kind: "success", job });
  return (
    <div className="space-y-4">
      <ClusterStrip c={c} />
      <JobBanner result={result} />
      <TileGrid>
        <Win>
          <Carp c={c} admin={admin} onDone={done} />
        </Win>
        <Win>
          <Firmware c={c} admin={admin} onDone={done} />
        </Win>
        {c.ha && (
          <Win>
            <Drift c={c} />
          </Win>
        )}
        <Win wide>
          <Backups c={c} admin={admin} onDone={done} />
        </Win>
      </TileGrid>
    </div>
  );
}

type Dialog = { title: string; body: ReactNode; confirm?: string; label: string; start: (a: object) => Promise<Job> } | null;

function Carp({ c, admin, onDone }: { c: OpnCluster; admin: boolean; onDone: (j: Job) => void }) {
  const carp = useOpnCarp();
  const [dlg, setDlg] = useState<Dialog>(null);
  const master = c.nodes.find((n) => n.node_id === c.master);
  const prod = c.environment === "prod";
  const anyMaint = c.nodes.some((n) => n.facts.carp?.maintenance);
  return (
    <Card title="CARP en onderhoud">
      {dlg && (
        <ActionDialog
          title={dlg.title}
          confirm={dlg.confirm}
          prod={prod}
          label={dlg.label}
          start={dlg.start}
          onClose={() => setDlg(null)}
          onDone={(j) => {
            setDlg(null);
            onDone(j);
          }}
        >
          {dlg.body}
        </ActionDialog>
      )}
      <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
        {c.nodes.map((n) => (
          <li key={n.node_id} className="flex flex-wrap items-center justify-between gap-2 py-2">
            <span className="inline-flex flex-wrap items-center gap-2">
              <span className="font-medium">{n.hostname}</span>
              <Badge tone={n.role === "master" ? "green" : n.role === "backup" ? "blue" : n.role === "mixed" ? "red" : "slate"}>
                {roleLabel[n.role]}
              </Badge>
              {n.facts.carp?.maintenance && <Badge tone="amber">onderhoudsmodus</Badge>}
              {(n.facts.carp?.demotion ?? 0) > 0 && !n.facts.carp?.maintenance && (
                <Badge tone="amber">demotion {n.facts.carp.demotion}</Badge>
              )}
            </span>
            {admin && c.ha && (
              <button
                type="button"
                className="text-xs text-slate-500 underline"
                onClick={() => {
                  const on = !n.facts.carp?.maintenance;
                  setDlg(
                    on
                      ? {
                          title: `Onderhoudsmodus aan op ${n.hostname}`,
                          label: "Aanzetten",
                          confirm: c.name,
                          body: (
                            <>
                              <p>
                                {n.hostname} geeft al zijn CARP-VIP&apos;s aan de andere firewall en houdt ze niet meer vast, ook na een
                                herstart, tot je de onderhoudsmodus weer uitzet.
                              </p>
                              <p>
                                ClusterForge wacht tot de andere firewall MASTER is en de VIP&apos;s antwoorden. Lukt dat niet binnen
                                anderhalve minuut, dan gaat de onderhoudsmodus meteen weer uit.
                              </p>
                            </>
                          ),
                          start: (a) => carp.mutateAsync({ id: n.node_id, action: "maintenance_on", ...a }),
                        }
                      : {
                          title: `Onderhoudsmodus uit op ${n.hostname}`,
                          label: "Uitzetten",
                          body: (
                            <p>
                              {n.hostname} doet weer mee met CARP. Met preempt aan (de standaard) en de laagste skew neemt hij het verkeer
                              terug.
                            </p>
                          ),
                          start: (a) => carp.mutateAsync({ id: n.node_id, action: "maintenance_off", ...a }),
                        },
                  );
                }}
              >
                {n.facts.carp?.maintenance ? "onderhoudsmodus uit" : "onderhoudsmodus aan"}
              </button>
            )}
          </li>
        ))}
      </ul>
      {!c.ha ? (
        <p className="mt-3 text-sm text-slate-500">Losse firewall: zonder backup is er geen failover.</p>
      ) : (
        <div className="mt-3 space-y-2 text-sm">
          {anyMaint && <Alert kind="info">Er staat een firewall in onderhoudsmodus; zet hem uit als het onderhoud klaar is.</Alert>}
          {admin && (
            <Button
              variant="secondary"
              disabled={!master || anyMaint}
              onClick={() =>
                setDlg({
                  title: `Failovertest op ${c.name}`,
                  label: "Test starten",
                  confirm: c.name,
                  body: (
                    <>
                      <p>
                        {master?.hostname} gaat even in onderhoudsmodus; ClusterForge meet hoe snel de backup alle VIP&apos;s overneemt en
                        ze weer antwoorden, zet de onderhoudsmodus uit en meet hoe snel het verkeer terugkomt.
                      </p>
                      <p>Bestaande verbindingen blijven dankzij pfsync; je merkt hoogstens een paar seconden vertraging.</p>
                    </>
                  ),
                  start: (a) => carp.mutateAsync({ id: master!.node_id, action: "failover_test", ...a }),
                })
              }
            >
              Failovertest
            </Button>
          )}
          {!master && <p className="text-xs text-slate-500">Een failovertest kan pas als één firewall MASTER is voor alle VIP&apos;s.</p>}
        </div>
      )}
    </Card>
  );
}

type FwStatus = { status?: string; status_msg?: string; upgrade_major_version?: string; product_version?: string };

function FirmwareRow({ n }: { n: OpnFirewall }) {
  const q = useOpnGet<FwStatus>(n.why ? "" : n.node_id, "core/firmware/status", { refetch: 300_000 });
  return (
    <li className="py-2">
      <span className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{n.hostname}</span>
        <span className="text-slate-500">OPNsense {n.facts.version || "?"}</span>
        {q.data?.status === "update" && <Badge tone="amber">updates</Badge>}
        {q.data?.status === "upgrade" && <Badge tone="amber">upgrade</Badge>}
        {q.data?.upgrade_major_version && <Badge tone="blue">{q.data.upgrade_major_version} beschikbaar</Badge>}
      </span>
      <span className="block text-xs text-slate-500">
        {q.isError ? (q.error as Error).message : q.data?.status_msg || "Nog niet gecontroleerd: kies Zoeken naar updates."}
      </span>
    </li>
  );
}

function Firmware({ c, admin, onDone }: { c: OpnCluster; admin: boolean; onDone: (j: Job) => void }) {
  const node = readNode(c);
  const fw = useOpnFirmware(node?.node_id ?? c.nodes[0].node_id);
  const [dlg, setDlg] = useState<Dialog>(null);
  const [err, setErr] = useState<string | null>(null);
  const master = c.nodes.find((n) => n.node_id === c.master) ?? c.nodes[0];
  const backups = c.nodes.filter((n) => n.node_id !== master.node_id).map((n) => n.hostname);
  const order = (
    <ol className="list-decimal space-y-1 pl-5">
      {backups.length > 0 && <li>Eerst {backups.join(", ")}: bijwerken, eventueel herstarten, wachten tot de agent terug is.</li>}
      {backups.length > 0 ? (
        <>
          <li>{master.hostname} in onderhoudsmodus; de backup neemt het verkeer over (anders stopt de taak hier).</li>
          <li>{master.hostname} bijwerken en wachten tot hij terug is.</li>
          <li>Onderhoudsmodus uit; {master.hostname} neemt het verkeer terug.</li>
        </>
      ) : (
        <li>{master.hostname} bijwerken. Als hij herstart, is er zolang geen verkeer.</li>
      )}
    </ol>
  );
  const ask = (mode: "update" | "upgrade") =>
    setDlg({
      title: mode === "update" ? `Firmware bijwerken op ${c.name}` : `Upgrade op ${c.name}`,
      label: mode === "update" ? "Bijwerken" : "Upgrade starten",
      confirm: c.name,
      body: (
        <>
          {order}
          <p>Mislukt een stap, dan stopt de taak en blijft de rest zoals hij is. Een update kan tot een uur duren.</p>
        </>
      ),
      start: (a) => fw.mutateAsync({ mode, ...a }),
    });
  return (
    <Card title="Firmware">
      {dlg && (
        <ActionDialog
          title={dlg.title}
          confirm={dlg.confirm}
          prod={c.environment === "prod"}
          label={dlg.label}
          start={dlg.start}
          onClose={() => setDlg(null)}
          onDone={(j) => {
            setDlg(null);
            onDone(j);
          }}
        >
          {dlg.body}
        </ActionDialog>
      )}
      {err && <Alert>{err}</Alert>}
      <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
        {c.nodes.map((n) => (
          <FirmwareRow key={n.node_id} n={n} />
        ))}
      </ul>
      {admin && (
        <div className="mt-3 flex flex-wrap gap-2">
          <Button
            variant="secondary"
            disabled={fw.isPending}
            onClick={async () => {
              setErr(null);
              try {
                onDone(await fw.mutateAsync({ mode: "check" }));
              } catch (e) {
                setErr(e instanceof Error ? e.message : "Starten mislukt");
              }
            }}
          >
            Zoeken naar updates
          </Button>
          <Button variant="secondary" onClick={() => ask("update")}>
            Bijwerken
          </Button>
          <Button variant="secondary" onClick={() => ask("upgrade")}>
            Upgrade
          </Button>
        </div>
      )}
    </Card>
  );
}

function Drift({ c }: { c: OpnCluster }) {
  const node = readNode(c);
  const q = useOpnDrift(node?.node_id);
  return (
    <Card title="Master tegenover backup">
      {q.isError ? (
        <p className="text-sm text-red-700 dark:text-red-400">{(q.error as Error).message}</p>
      ) : !q.data ? (
        <p className="text-sm text-slate-500">Vergelijken…</p>
      ) : (
        <div className="space-y-3 text-sm">
          {q.data.rows.map((r) => (
            <div key={r.node_id}>
              <p className="flex flex-wrap items-center gap-2">
                <span className="font-medium">{r.hostname}</span>
                {r.error ? (
                  <Badge tone="red">niet te lezen</Badge>
                ) : r.sections.length === 0 ? (
                  <Badge tone="green">gelijk aan {q.data.master}</Badge>
                ) : (
                  <Badge tone="amber">{r.sections.length} onderdelen anders</Badge>
                )}
              </p>
              {r.error && <p className="text-xs text-slate-500">{r.error}</p>}
              {r.sections.length > 0 && (
                <p className="mt-1 font-mono text-xs text-slate-600 dark:text-slate-300">{r.sections.join(", ")}</p>
              )}
            </div>
          ))}
          <p className="text-xs text-slate-500">
            Telt niet mee omdat het per firewall hoort te verschillen: interfaces, system, HA-instellingen en de CARP-skew. Een onderdeel
            dat de master niet synchroniseert (System → High Availability) blijft anders; bij de rest helpt Sync nu op het{" "}
            <Link href="/opnsense" className="underline">
              overzicht
            </Link>
            . Regel per regel vergelijken doe je bij{" "}
            <Link href="/opnsense/revisies" className="underline">
              Revisies
            </Link>
            .
          </p>
          {q.data.sync_items.length > 0 && <p className="text-xs text-slate-500">Gesynchroniseerd: {q.data.sync_items.join(", ")}</p>}
        </div>
      )}
    </Card>
  );
}

function Backups({ c, admin, onDone }: { c: OpnCluster; admin: boolean; onDone: (j: Job) => void }) {
  const [nodeId, setNodeId] = useState(readNode(c)?.node_id ?? c.nodes[0].node_id);
  const q = useOpnBackups(nodeId);
  const now = useOpnBackupNow(nodeId);
  const [err, setErr] = useState<string | null>(null);
  const [download, setDownload] = useState<string | null>(null);
  const fmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "short", timeStyle: "short" });
  return (
    <Card title={`Back-ups in ClusterForge${q.data ? ` · ${q.data.items.length}` : ""}`}>
      {download && <DownloadDialog id={download} onClose={() => setDownload(null)} />}
      <div className="mb-3 flex flex-wrap items-center gap-2 text-sm">
        {c.nodes.length > 1 && (
          <Select aria-label="Firewall" value={nodeId} onChange={(e) => setNodeId(e.target.value)} className="w-auto">
            {c.nodes.map((n) => (
              <option key={n.node_id} value={n.node_id}>
                {n.hostname}
              </option>
            ))}
          </Select>
        )}
        {admin && (
          <Button
            variant="secondary"
            disabled={now.isPending}
            onClick={async () => {
              setErr(null);
              try {
                onDone(await now.mutateAsync());
              } catch (e) {
                setErr(e instanceof Error ? e.message : "Starten mislukt");
              }
            }}
          >
            Nu back-up maken
          </Button>
        )}
        <span className="text-xs text-slate-500">
          Elke dag en na elke wijziging; een ongewijzigde configuratie geeft geen nieuwe. De nieuwste {q.data?.keep ?? 30} per firewall
          blijven.
        </span>
      </div>
      {err && <Alert>{err}</Alert>}
      {q.isError ? (
        <p className="text-sm text-red-700 dark:text-red-400">{(q.error as Error).message}</p>
      ) : (q.data?.items.length ?? 0) === 0 ? (
        <p className="text-sm text-slate-500">Nog geen back-up; de eerste komt binnen het uur, of nu met de knop.</p>
      ) : (
        <div className="max-h-[24rem] overflow-auto">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Gemaakt</th>
                <th className={thClass}>Waarom</th>
                <th className={thClass}>Versie</th>
                <th className={thClass}>Grootte</th>
                {admin && <th className={thClass} />}
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {q.data!.items.map((b) => (
                <tr key={b.id}>
                  <td className={`${tdClass} whitespace-nowrap`}>{fmt.format(new Date(b.taken_at))}</td>
                  <td className={tdClass}>{b.reason}</td>
                  <td className={tdClass}>{b.version || "–"}</td>
                  <td className={tdClass}>{formatBytes(b.size)}</td>
                  {admin && (
                    <td className={`${tdClass} text-xs`}>
                      <button type="button" className="text-slate-500 underline" onClick={() => setDownload(b.id)}>
                        downloaden
                      </button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="mt-2 text-xs text-slate-500">
        Een back-up is de volledige config.xml met wachtwoorden en sleutels. Terugzetten doe je in OPNsense via System → Configuration →
        Backups, of een revisie op de firewall zelf via{" "}
        <Link href="/opnsense/revisies" className="underline">
          Revisies
        </Link>
        .
      </p>
    </Card>
  );
}

function DownloadDialog({ id, onClose }: { id: string; onClose: () => void }) {
  const [code, setCode] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  return (
    <Modal title="Back-up downloaden" onClose={onClose}>
      <div className="space-y-4 text-sm">
        {err && <Alert>{err}</Alert>}
        <p className="text-slate-600 dark:text-slate-300">
          Het bestand bevat alle wachtwoorden, sleutels en certificaten van de firewall. Bewaar het veilig. Het downloaden komt in het
          logboek.
        </p>
        <Field label="Code van je tweestapsverificatie (of wachtwoord)" htmlFor="opn-dl-code">
          <Input id="opn-dl-code" type="password" value={code} onChange={(e) => setCode(e.target.value)} autoComplete="one-time-code" />
        </Field>
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            disabled={!code || busy}
            onClick={async () => {
              setBusy(true);
              setErr(null);
              try {
                await downloadOpnBackup(id, code);
                onClose();
              } catch (e) {
                setErr(e instanceof Error ? e.message : "Downloaden mislukt");
              } finally {
                setBusy(false);
              }
            }}
          >
            Downloaden
          </Button>
        </div>
      </div>
    </Modal>
  );
}
