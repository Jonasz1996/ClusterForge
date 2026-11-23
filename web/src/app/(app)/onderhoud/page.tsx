"use client";

import Link from "next/link";
import { useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { Modal } from "@/components/Modal";
import { Tile, TileGrid } from "@/components/Tiles";
import { toast } from "@/components/Toast";
import { Alert, Badge, Button, Card, Input, PageHeader, Select } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useIsAdmin } from "@/lib/inventory";
import {
  engineNames,
  human,
  policyText,
  useSetUpkeepPolicy,
  useStartUpkeep,
  useUpkeep,
  useUpkeepHistory,
  type UpkeepNode,
  type UpkeepPolicy,
  type UpkeepRow,
  type UpkeepScope,
} from "@/lib/upkeep";

// Fase 46: de agents doen zelf het periodieke onderhoud, per cluster één
// node tegelijk en afgesproken met de andere nodes.

function errText(e: unknown, fallback: string) {
  return e instanceof ApiError ? e.message : fallback;
}

function when(s?: string) {
  return s ? new Date(s).toLocaleString("nl-BE", { dateStyle: "short", timeStyle: "short" }) : "—";
}

const modeNames: Record<string, string> = { run: "beurt", urgent: "spoed (schijf vol)", preview: "voorbeeld" };

export default function UpkeepPage() {
  const data = useUpkeep();
  const [edit, setEdit] = useState<UpkeepScope | null>(null);
  const [open, setOpen] = useState<UpkeepScope | null>(null);
  if (data.isError) return <Alert>{errText(data.error, "Het onderhoud laden mislukte.")}</Alert>;
  if (!data.data) return null;
  const scopes = data.data.scopes;
  const envs = scopes.filter((s) => s.kind === "env");
  const groups = scopes.filter((s) => s.kind !== "env" && s.nodes.length > 0);
  const empty = scopes.filter((s) => s.kind === "cluster" && s.nodes.length === 0);
  const current = open ? (scopes.find((s) => s.scope === open.scope) ?? open) : null;
  return (
    <div className="space-y-6">
      <PageHeader
        title="Onderhoud"
        description="De agents houden je servers zelf gezond: schijfruimte vrijhouden en databases onderhouden, in een venster dat jij kiest."
      />
      <Alert kind="info">
        Per cluster doet ClusterForge één node tegelijk: eerst de nodes zonder VIP of primary, dan de primary van de database, de VIP-houder als laatste.
        Na elke node wacht het tot die gezond is. Een beurt houdt het clusterslot, dus nooit samen met een update of uitrol van hetzelfde cluster. Zwaar
        databasewerk (OPTIMIZE, REINDEX) slaat het over zolang een node van het cluster niet verbonden is, en Galera alleen als het volledig Synced is: het
        quorum blijft altijd heel. Met <b>Voorbeeld</b> zie je eerst wat er zou gebeuren, zonder iets te wijzigen. Tijden in {data.data.timezone === "Local" ? "de tijdzone van de server" : data.data.timezone}.
      </Alert>
      <TileGrid>
        <Tile>
          <Card title="Beleid per omgeving">
            <div className="space-y-3">
              {envs.map((e) => (
                <PolicyBox key={e.scope} s={e} onEdit={() => setEdit(e)} />
              ))}
              <p className="text-xs text-slate-500">Een cluster volgt zijn omgeving, tenzij je bij het cluster een eigen beleid kiest of het uitzet.</p>
            </div>
          </Card>
        </Tile>
        <Tile wide>
          <Card title="Clusters en losse nodes">
            <GroupSummary groups={groups} />
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead className="text-left text-xs text-slate-500">
                  <tr>
                    <th className="py-1 pr-3 font-medium">Waar</th>
                    <th className="py-1 pr-3 font-medium">Beleid</th>
                    <th className="py-1 pr-3 font-medium">Volgende</th>
                    <th className="py-1 pr-3 font-medium">Laatst</th>
                    <th className="py-1" />
                  </tr>
                </thead>
                <tbody>
                  {groups.map((s) => (
                    <GroupRow key={s.scope} s={s} onOpen={() => setOpen(s)} onEdit={() => setEdit(s)} />
                  ))}
                </tbody>
              </table>
            </div>
            {empty.length > 0 && <p className="mt-3 text-xs text-slate-500">Zonder nodes met een agent: {empty.map((s) => s.name).join(", ")}.</p>}
          </Card>
        </Tile>
        <Tile wide>
          <History />
        </Tile>
      </TileGrid>
      {edit && <PolicyDialog s={edit} defaults={data.data.defaults} onClose={() => setEdit(null)} />}
      {current && <GroupDialog s={current} onClose={() => setOpen(null)} onEdit={() => setEdit(current)} />}
    </div>
  );
}

function GroupSummary({ groups }: { groups: UpkeepScope[] }) {
  const on = groups.filter((g) => g.effective.enabled).length;
  const running = groups.filter((g) => g.running_job).length;
  return (
    <p className="mb-2 text-sm text-slate-600 dark:text-slate-400">
      {on} van {groups.length} met automatisch onderhoud{running > 0 ? `, ${running} bezig` : ""}.
    </p>
  );
}

function PolicyBox({ s, onEdit }: { s: UpkeepScope; onEdit: () => void }) {
  const isAdmin = useIsAdmin();
  return (
    <div className="space-y-1 rounded-md border border-slate-200 p-3 dark:border-slate-800">
      <p className="flex items-center justify-between font-medium">
        {s.name}
        <Badge tone={s.effective.enabled ? "green" : "slate"}>{s.effective.enabled ? "aan" : "uit"}</Badge>
      </p>
      <p className="text-xs text-slate-600 dark:text-slate-400">{policyText(s.effective)}</p>
      {s.updated_by && <p className="text-xs text-slate-500">gewijzigd door {s.updated_by}</p>}
      {isAdmin && (
        <Button variant="secondary" onClick={onEdit}>
          Wijzigen
        </Button>
      )}
    </div>
  );
}

function GroupRow({ s, onOpen, onEdit }: { s: UpkeepScope; onOpen: () => void; onEdit: () => void }) {
  const isAdmin = useIsAdmin();
  return (
    <tr className="border-t border-slate-100 align-top dark:border-slate-800">
      <td className="py-2 pr-3">
        <button type="button" onClick={onOpen} className="text-left font-medium hover:underline">
          {s.name}
        </button>{" "}
        {s.environment && <Badge tone={s.environment === "prod" ? "red" : "slate"}>{s.environment}</Badge>}
        <p className="text-xs text-slate-500">{s.nodes.length} nodes</p>
      </td>
      <td className="py-2 pr-3 text-xs">
        {s.kind === "cluster" && <span className="font-medium">{s.mode === "inherit" ? "zoals de omgeving" : s.mode === "own" ? "eigen beleid" : "uit"}</span>}
        <br />
        <span className="text-slate-600 dark:text-slate-400">{policyText(s.effective)}</span>
      </td>
      <td className="py-2 pr-3 text-xs">{s.effective.enabled ? when(s.next_start) : "—"}</td>
      <td className="py-2 pr-3 text-xs">
        {s.running_job ? (
          <Link href={`/taken/detail?id=${s.running_job}`} className="text-brand-700 hover:underline dark:text-sky-300">
            bezig…
          </Link>
        ) : (
          <>
            {when(s.last_run?.started_at)}
            {s.last_run?.job_id && (
              <>
                {" "}
                <Link href={`/taken/detail?id=${s.last_run.job_id}`} className="text-brand-700 hover:underline dark:text-sky-300">
                  taak
                </Link>
              </>
            )}
            {s.last_run?.note && <p className="text-slate-500">{s.last_run.note}</p>}
          </>
        )}
      </td>
      <td className="py-2 text-right whitespace-nowrap">
        <Button variant="secondary" onClick={onOpen}>
          Volgorde en voorbeeld
        </Button>{" "}
        {isAdmin && (
          <Button variant="secondary" onClick={onEdit}>
            Wijzigen
          </Button>
        )}
      </td>
    </tr>
  );
}

function roleText(n: UpkeepNode) {
  const parts: string[] = [];
  if (n.vip_holder) parts.push("VIP-houder");
  if (n.db_engine) parts.push(`${engineNames[n.db_engine] ?? n.db_engine} ${n.db_role ?? ""}`.trim());
  return parts.join(" · ");
}

function GroupDialog({ s, onClose, onEdit }: { s: UpkeepScope; onClose: () => void; onEdit: () => void }) {
  const isAdmin = useIsAdmin();
  const start = useStartUpkeep();
  const go = async (preview: boolean) => {
    if (!preview) {
      const ok = await askConfirm(
        `Nu onderhoud doen in ${s.name}? ClusterForge doet de ${s.nodes.length} nodes één voor één, in de volgorde hieronder, en ruimt op volgens het beleid.`,
      );
      if (!ok) return;
    }
    start.mutate(
      { scope: s.scope, preview },
      { onSuccess: () => toast(preview ? "Voorbeeld gestart; het resultaat komt per node hieronder." : "Onderhoud gestart.") },
    );
  };
  return (
    <Modal title={`Onderhoud: ${s.name}`} onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        <p className="text-slate-600 dark:text-slate-400">{policyText(s.effective)}</p>
        {s.running_job && (
          <Alert kind="info">
            Er loopt een beurt of voorbeeld.{" "}
            <Link href={`/taken/detail?id=${s.running_job}`} className="underline">
              Volg de taak
            </Link>
            .
          </Alert>
        )}
        <ol className="space-y-3">
          {s.nodes.map((n) => (
            <li key={n.node_id} className="rounded-md border border-slate-200 p-3 dark:border-slate-800">
              <p className="flex flex-wrap items-center gap-2">
                <span className="font-mono text-xs text-slate-500">{n.order}.</span>
                <Link href={`/nodes/detail?id=${n.node_id}`} className="font-medium hover:underline">
                  {n.hostname}
                </Link>
                {!n.online && <Badge tone="red">niet verbonden</Badge>}
                {n.lifecycle && n.lifecycle !== "active" && <Badge tone="amber">{n.lifecycle}</Badge>}
                {roleText(n) && <Badge tone="blue">{roleText(n)}</Badge>}
              </p>
              <p className="text-xs text-slate-500">{n.why}</p>
              {n.preview && <ResultView row={n.preview} />}
              {n.last && <ResultView row={n.last} />}
            </li>
          ))}
        </ol>
        {start.error && <Alert>{errText(start.error, "Starten mislukte")}</Alert>}
        <div className="flex flex-wrap justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Sluiten
          </Button>
          {isAdmin && (
            <>
              <Button variant="secondary" onClick={onEdit}>
                Beleid wijzigen
              </Button>
              <Button variant="secondary" disabled={start.isPending || !!s.running_job} onClick={() => void go(true)}>
                Voorbeeld
              </Button>
              <Button disabled={start.isPending || !!s.running_job} onClick={() => void go(false)}>
                Nu uitvoeren
              </Button>
            </>
          )}
        </div>
      </div>
    </Modal>
  );
}

function ResultView({ row }: { row: UpkeepRow }) {
  const r = row.result;
  const preview = row.mode === "preview";
  return (
    <div className="mt-2 rounded bg-slate-50 p-2 text-xs dark:bg-slate-800/50">
      <p className="mb-1 font-medium">
        {preview ? "Voorbeeld" : modeNames[row.mode] === "beurt" ? "Laatste beurt" : `Laatste ${modeNames[row.mode]}`} · {when(row.at)}{" "}
        {!preview && <Badge tone={row.ok ? "green" : "red"}>{row.ok ? "gelukt" : "deels mislukt"}</Badge>}{" "}
        <span className="text-slate-500">
          {preview ? "komt vrij: ongeveer " : "vrijgemaakt: "}
          {human(row.freed)}
        </span>
      </p>
      {r.skipped && <p>Overgeslagen: {r.skipped}</p>}
      <ul className="space-y-0.5">
        {r.items.map((it) => (
          <li key={it.id}>
            {it.status === "fail" ? "✗" : it.status === "skip" ? "–" : preview ? "•" : "✓"} {it.title}
            {it.bytes > 0 && <span className="text-slate-500"> ({human(it.bytes)})</span>}
            {it.note && <span className="text-slate-500">: {it.note}</span>}
          </li>
        ))}
        {r.db.map((d, i) => (
          <li key={`db${i}`}>
            {d.action === "fail" ? "✗" : d.action === "skip" ? "–" : preview ? "•" : "✓"} {engineNames[d.engine] ?? d.engine} ({d.role}): {d.note}
          </li>
        ))}
      </ul>
      {r.tables.length > 0 && (
        <details className="mt-1">
          <summary className="cursor-pointer text-slate-500">{r.tables.length} tabellen of indexen om opnieuw op te bouwen</summary>
          <ul className="mt-1 font-mono">
            {r.tables.map((t) => (
              <li key={t.engine + t.name}>
                {t.name} — {t.free > 0 ? `${human(t.free)} vrij van ` : ""}
                {human(t.size)}
              </li>
            ))}
          </ul>
        </details>
      )}
      <Link href={`/taken/detail?id=${row.job_id}`} className="text-brand-700 hover:underline dark:text-sky-300">
        uitvoer
      </Link>
    </div>
  );
}

function History() {
  const [mode, setMode] = useState("");
  const h = useUpkeepHistory();
  const rows = (h.data?.results ?? []).filter((r) => (mode ? r.mode === mode : true));
  const freed = rows.filter((r) => r.mode !== "preview").reduce((a, r) => a + r.freed, 0);
  return (
    <Card title="Logboek">
      <p className="mb-2 text-sm text-slate-600 dark:text-slate-400">
        {h.data ? `${h.data.results.length} beurten bewaard; samen ${human(freed)} vrijgemaakt.` : "Laden…"}
      </p>
      <div className="mb-3 flex items-center gap-2 text-sm">
        <Select value={mode} onChange={(e) => setMode(e.target.value)} className="max-w-xs">
          <option value="">Alles</option>
          <option value="run">Beurten</option>
          <option value="urgent">Spoed (schijf vol)</option>
          <option value="preview">Voorbeelden</option>
        </Select>
        <span className="text-xs text-slate-500">180 dagen bewaard</span>
      </div>
      {rows.length === 0 ? (
        <p className="text-sm text-slate-500">Nog niets.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-slate-500">
              <tr>
                <th className="py-1 pr-3 font-medium">Wanneer</th>
                <th className="py-1 pr-3 font-medium">Node</th>
                <th className="py-1 pr-3 font-medium">Soort</th>
                <th className="py-1 pr-3 font-medium">Vrij</th>
                <th className="py-1 pr-3 font-medium">Wat</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.id} className="border-t border-slate-100 align-top dark:border-slate-800">
                  <td className="py-1.5 pr-3 text-xs whitespace-nowrap">
                    <Link href={`/taken/detail?id=${r.job_id}`} className="hover:underline">
                      {when(r.at)}
                    </Link>
                  </td>
                  <td className="py-1.5 pr-3">
                    <Link href={`/nodes/detail?id=${r.node_id}`} className="hover:underline">
                      {r.hostname}
                    </Link>
                  </td>
                  <td className="py-1.5 pr-3 text-xs">
                    {modeNames[r.mode] ?? r.mode}{" "}
                    {r.mode !== "preview" && <Badge tone={r.ok ? "green" : "red"}>{r.ok ? "ok" : "fout"}</Badge>}
                  </td>
                  <td className="py-1.5 pr-3 text-xs whitespace-nowrap">{human(r.freed)}</td>
                  <td className="py-1.5 pr-3 text-xs text-slate-600 dark:text-slate-400">
                    {r.result.skipped
                      ? `overgeslagen: ${r.result.skipped}`
                      : [
                          ...r.result.items.filter((i) => i.status === "fail").map((i) => `✗ ${i.title}`),
                          ...r.result.db.map((d) => `${engineNames[d.engine] ?? d.engine}: ${d.note}`),
                        ].join(" · ") || `${r.result.items.length} onderdelen`}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

// ------------------------------------------------------------- Beleid

function Check({ checked, onChange, children }: { checked: boolean; onChange: (v: boolean) => void; children: React.ReactNode }) {
  return (
    <label className="flex items-start gap-2">
      <input type="checkbox" className="mt-1" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span>{children}</span>
    </label>
  );
}

function Num({ value, onChange, unit, min, max }: { value: number; onChange: (v: number) => void; unit: string; min: number; max: number }) {
  return (
    <span className="inline-flex items-center gap-1">
      <Input type="number" className="w-24" min={min} max={max} value={value} onChange={(e) => onChange(Number(e.target.value))} />
      <span className="text-xs text-slate-500">{unit}</span>
    </span>
  );
}

function PolicyForm({ p, onChange, showEnabled }: { p: UpkeepPolicy; onChange: (p: UpkeepPolicy) => void; showEnabled: boolean }) {
  const set = (patch: Partial<UpkeepPolicy>) => onChange({ ...p, ...patch });
  return (
    <div className="space-y-4">
      {showEnabled && (
        <Check checked={p.enabled} onChange={(v) => set({ enabled: v })}>
          <b>Automatisch onderhoud aan</b>
        </Check>
      )}
      <fieldset className="space-y-1">
        <legend className="mb-1 text-xs font-semibold tracking-wide text-slate-500 uppercase">Schijf vrijhouden</legend>
        <Check checked={p.journal} onChange={(v) => set({ journal: v })}>
          Systeemlogboek (journal) inkorten tot <Num value={p.journal_mb} onChange={(v) => set({ journal_mb: v })} unit="MB" min={50} max={10000} />
        </Check>
        <Check checked={p.logrotate} onChange={(v) => set({ logrotate: v })}>
          Logbestanden roteren (logrotate, zoals ingesteld op de node)
        </Check>
        <Check checked={p.old_logs} onChange={(v) => set({ old_logs: v })}>
          Geroteerde logbestanden wissen na <Num value={p.old_logs_days} onChange={(v) => set({ old_logs_days: v })} unit="dagen" min={1} max={365} />
        </Check>
        <Check checked={p.apt} onChange={(v) => set({ apt: v })}>
          Gedownloade updates (apt-cache) wissen
        </Check>
        <Check checked={p.coredumps} onChange={(v) => set({ coredumps: v })}>
          Crashbestanden (core dumps) wissen
        </Check>
        <Check checked={p.tmp} onChange={(v) => set({ tmp: v })}>
          Tijdelijke bestanden in /tmp en /var/tmp wissen die <Num value={p.tmp_days} onChange={(v) => set({ tmp_days: v })} unit="dagen" min={1} max={365} />{" "}
          niemand opende
        </Check>
      </fieldset>
      <fieldset className="space-y-1">
        <legend className="mb-1 text-xs font-semibold tracking-wide text-slate-500 uppercase">Docker (volumes en data blijven altijd staan)</legend>
        <Check checked={p.docker_images} onChange={(v) => set({ docker_images: v })}>
          Images die geen container gebruikt en ouder dan{" "}
          <Num value={p.docker_image_days} onChange={(v) => set({ docker_image_days: v })} unit="dagen" min={1} max={365} /> wissen
        </Check>
        <Check checked={p.docker_build_cache} onChange={(v) => set({ docker_build_cache: v })}>
          Bouwcache (docker build) wissen
        </Check>
        <Check checked={p.docker_logs_mb > 0} onChange={(v) => set({ docker_logs_mb: v ? 500 : 0 })}>
          Containerlogs groter dan{" "}
          <Num value={p.docker_logs_mb || 500} onChange={(v) => set({ docker_logs_mb: v })} unit="MB" min={50} max={100000} /> leegmaken (de oude regels zijn
          dan weg)
        </Check>
      </fieldset>
      <fieldset className="space-y-1">
        <legend className="mb-1 text-xs font-semibold tracking-wide text-slate-500 uppercase">Databases</legend>
        <Check checked={p.db_analyze} onChange={(v) => set({ db_analyze: v })}>
          Statistieken bijwerken: MariaDB <code>ANALYZE</code> op elke node (niet gerepliceerd), PostgreSQL <code>VACUUM (ANALYZE)</code> op de primary
        </Check>
        <Check checked={p.db_optimize} onChange={(v) => set({ db_optimize: v })}>
          Opnieuw opbouwen: MariaDB <code>OPTIMIZE</code> van gefragmenteerde tabellen (Galera: de node gaat even uit de pool en haalt daarna in),
          PostgreSQL <code>REINDEX CONCURRENTLY</code> van opgezwollen indexen (vraagt de extensie pgstattuple)
        </Check>
        {p.db_optimize && (
          <p className="ml-6 text-xs">
            Alleen vanaf <Num value={p.optimize_min_mb} onChange={(v) => set({ optimize_min_mb: v })} unit="MB" min={1} max={100000} /> en{" "}
            <Num value={p.optimize_min_pct} onChange={(v) => set({ optimize_min_pct: v })} unit="% verspilde ruimte" min={5} max={90} />
          </p>
        )}
      </fieldset>
      <fieldset className="space-y-2">
        <legend className="mb-1 text-xs font-semibold tracking-wide text-slate-500 uppercase">Wanneer</legend>
        <label className="block">
          <span className="text-xs text-slate-500">Venster, zoals “dagelijks 01:30-03:30”, “za,zo 02:00-04:00” of “zo 01:00-03:00”</span>
          <Input className="font-mono" value={p.window} onChange={(e) => set({ window: e.target.value })} />
        </label>
        <p className="text-xs">
          Pauze tussen twee nodes: <Num value={p.pause_seconds} onChange={(v) => set({ pause_seconds: v })} unit="seconden" min={0} max={3600} />
        </p>
        <Check checked={p.urgent} onChange={(v) => set({ urgent: v })}>
          Een node met een bijna volle schijf (boven de ernstige drempel) meteen opruimen, ook buiten het venster; alleen de schijfonderdelen, hoogstens
          elke 6 uur
        </Check>
      </fieldset>
    </div>
  );
}

function PolicyDialog({ s, defaults, onClose }: { s: UpkeepScope; defaults: UpkeepPolicy; onClose: () => void }) {
  const save = useSetUpkeepPolicy();
  const cluster = s.kind === "cluster";
  const [mode, setMode] = useState(s.mode);
  const [p, setP] = useState<UpkeepPolicy>(cluster ? (s.own ?? { ...s.effective, enabled: true }) : s.effective);
  return (
    <Modal title={`Onderhoud: ${s.name}`} onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        {cluster && (
          <Select value={mode} onChange={(e) => setMode(e.target.value as UpkeepScope["mode"])}>
            <option value="inherit">Zoals de omgeving ({s.environment})</option>
            <option value="own">Eigen beleid</option>
            <option value="off">Uit (nooit vanzelf)</option>
          </Select>
        )}
        {(!cluster || mode === "own") && <PolicyForm p={p} onChange={setP} showEnabled={!cluster} />}
        {(!cluster || mode === "own") && (
          <button type="button" className="text-xs text-slate-500 underline" onClick={() => setP({ ...defaults, enabled: p.enabled })}>
            Standaardwaarden terugzetten
          </button>
        )}
        {save.error && <Alert>{errText(save.error, "Bewaren mislukte")}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            disabled={save.isPending}
            onClick={() =>
              save.mutate(
                { scope: s.scope, mode: cluster ? mode : "own", policy: p },
                {
                  onSuccess: () => {
                    toast("Bewaard.");
                    onClose();
                  },
                },
              )
            }
          >
            Bewaren
          </Button>
        </div>
      </div>
    </Modal>
  );
}
