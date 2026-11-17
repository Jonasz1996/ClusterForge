"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { Modal } from "@/components/Modal";
import { AIVLookup } from "@/components/aiverslag/Lookup";
import { AskView } from "@/components/aiverslag/Ask";
import { FindingsView } from "@/components/aiverslag/Findings";
import { Tabs } from "@/components/Tabs";
import {
  AIVGate,
  NiveauBadge,
  UnitDot,
  useAIVRunner,
  webURL,
  type AIVRunner,
} from "@/components/aiverslag/bits";
import {
  Empty,
  QueryState,
  tableClass,
  tdClass,
  thClass,
} from "@/components/inventory/bits";
import { Tile as Win, TileGrid } from "@/components/Tiles";
import { Alert, Badge, Button, Card, Input, PageHeader } from "@/components/ui";
import { Sparkline } from "@/components/zabbix/Zabbix";
import { useMe } from "@/lib/auth";
import {
  aivDate,
  sevTone,
  useAIVInstall,
  useAIVNow,
  useAIVOverview,
  type AIVInstance,
  type AIVOverview,
  type AIVStart,
  type AIVUnit,
} from "@/lib/aiverslag";

export default function AIVerslagPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="AI-verslag"
        description="De AI-analyse van je netwerk (OPNsense, CrowdSec, Zenarmor, dreigingsfeeds) in ClusterForge: dreigingsniveau, wat nu belangrijk is, alarmen en bevindingen. Via de ClusterForge-agent op de machine van AI-verslag."
      />
      <Suspense>
        <AIVGate>{(v) => <Main key={v.node_id} v={v} />}</AIVGate>
      </Suspense>
    </div>
  );
}

type TabKey = "overzicht" | "bevindingen" | "vraag";

const tabs = [
  { key: "overzicht", label: "Overzicht" },
  { key: "bevindingen", label: "Bevindingen" },
  { key: "vraag", label: "Vraag het AI-verslag" },
] as const;

function Main({ v }: { v: AIVInstance }) {
  const [tab, setTab] = useState<TabKey>("overzicht");
  const params = useSearchParams();
  const router = useRouter();
  const ip = params.get("ip") ?? "";
  const [look, setLook] = useState("");
  const open = (q: string) =>
    router.replace(q ? `/aiverslag?ip=${encodeURIComponent(q)}` : "/aiverslag");
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <Tabs tabs={tabs} value={tab} onChange={setTab} label="AI-verslag" />
        <form
          className="flex gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (look.trim()) open(look.trim());
          }}
        >
          <Input
            value={look}
            onChange={(e) => setLook(e.target.value)}
            placeholder="IP, domein of CVE opzoeken"
            aria-label="IP, domein of CVE opzoeken"
            className="w-56"
          />
          <Button variant="secondary" type="submit">
            Opzoeken
          </Button>
        </form>
      </div>
      {ip && (
        <Modal title={`Opzoeken: ${ip}`} onClose={() => open("")}>
          <AIVLookup v={v} q={ip} />
        </Modal>
      )}
      {tab === "overzicht" && <OverviewView v={v} />}
      {tab === "bevindingen" && <FindingsView v={v} />}
      {tab === "vraag" && <AskView v={v} />}
    </div>
  );
}

function OverviewView({ v }: { v: AIVInstance }) {
  const o = useAIVOverview(v.node_id);
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const runner = useAIVRunner(v.node_id);
  return (
    <QueryState q={o}>
      {o.data && (
        <div className="space-y-6">
          {runner.banner}
          <Band o={o.data} admin={admin} runner={runner} />
          {o.data.warnings.length > 0 && (
            <Card title="Aandacht nodig">
              <ul className="space-y-1.5 text-sm">
                {o.data.warnings.map((w) => (
                  <li key={w.text} className="flex items-start gap-2">
                    <Badge tone={w.level === "critical" ? "red" : "amber"}>
                      {w.level === "critical" ? "Ernstig" : "Let op"}
                    </Badge>
                    <span>{w.text}</span>
                  </li>
                ))}
              </ul>
            </Card>
          )}
          <TileGrid>
            <Win wide>
              <NowCard o={o.data} />
            </Win>
            <Win>
              <NetCard s={o.data.start} />
            </Win>
            <Win>
              <AlarmCard o={o.data} admin={admin} />
            </Win>
            <Win wide>
              <FindingsCard o={o.data} />
            </Win>
            <Win>
              <ServiceCard o={o.data} />
            </Win>
            <Win>
              <ExtCard o={o.data} admin={admin} />
            </Win>
          </TileGrid>
          {Object.keys(o.data.errors).length > 0 && (
            <p className="text-xs text-slate-500">
              Niet gelezen:{" "}
              {Object.entries(o.data.errors)
                .map(([k, e]) => `${k} (${e})`)
                .join(" · ")}
            </p>
          )}
        </div>
      )}
    </QueryState>
  );
}

function Band({
  o,
  admin,
  runner,
}: {
  o: AIVOverview;
  admin: boolean;
  runner: AIVRunner;
}) {
  const s = o.start;
  const n =
    s?.niveau ??
    (o.app?.niveau
      ? { niveau: o.app.niveau, redenen: o.app.redenen ?? [] }
      : undefined);
  const score = s?.score?.score ?? o.app?.score;
  const ring =
    n?.niveau === "kritiek"
      ? "border-red-300 dark:border-red-900"
      : n?.niveau === "verhoogd"
        ? "border-amber-300 dark:border-amber-900"
        : "border-slate-200 dark:border-slate-800";
  const running = o.state?.running;
  return (
    <section
      className={`rounded-lg border bg-white p-5 shadow-xs dark:bg-slate-900 ${ring}`}
    >
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm text-slate-500">Dreigingsniveau</span>
            <NiveauBadge niveau={n?.niveau} />
          </div>
          <ul className="text-sm">
            {(n?.redenen ?? []).map((r) => (
              <li key={r}>{r}</li>
            ))}
          </ul>
          {o.state?.summary && (
            <p className="max-w-3xl text-sm text-slate-600 dark:text-slate-400">
              {o.state.summary}
            </p>
          )}
        </div>
        <div className="flex flex-wrap items-start gap-6">
          <div>
            <div className="text-sm text-slate-500">Netwerkscore</div>
            <div className="text-2xl font-semibold tabular-nums">
              {score ?? "–"}
            </div>
            {s?.score?.gisteren != null && (
              <div className="text-xs text-slate-500">
                gisteren {s.score.gisteren}
              </div>
            )}
            <Sparkline
              points={(s?.score?.reeks ?? []).filter(
                (x): x is number => typeof x === "number",
              )}
            />
          </div>
          <div>
            <div className="text-sm text-slate-500">Incidenten open</div>
            <div className="text-2xl font-semibold tabular-nums">
              {s?.incidenten_n ?? o.app?.incidenten ?? "–"}
            </div>
          </div>
          <div className="space-y-2">
            <div className="text-xs text-slate-500">
              Laatste analyse: {o.state?.last || o.app?.laatste_run || "–"}
              {running ? " · bezig" : ""}
            </div>
            {admin && (
              <Button
                disabled={runner.pending || running}
                onClick={() => runner.run({ path: "/api/run" })}
              >
                {running ? "Analyse loopt" : "Analyse nu"}
              </Button>
            )}
          </div>
        </div>
      </div>
    </section>
  );
}

function NowCard({ o }: { o: AIVOverview }) {
  const nu = o.start?.nu ?? [];
  const inc = o.start?.incidenten ?? [];
  const url = webURL(o.instance);
  return (
    <Card title="Nu belangrijk">
      {nu.length === 0 && inc.length === 0 ? (
        <Empty>Niets dringends volgens AI-verslag.</Empty>
      ) : (
        <ul className="space-y-3 text-sm">
          {nu.map((x) => (
            <li
              key={x.titel + (x.link ?? "")}
              className="flex items-start gap-2"
            >
              <span aria-hidden className="w-5 shrink-0 text-center">
                {x.icoon ?? "•"}
              </span>
              <div className="min-w-0">
                <div className="font-medium">
                  {x.titel}
                  {x.prio != null && (
                    <span className="ml-2 text-xs font-normal text-slate-500">
                      prio {Math.round(x.prio * 10) / 10}
                    </span>
                  )}
                </div>
                {x.tekst && (
                  <div className="text-slate-600 dark:text-slate-400">
                    {x.tekst}
                  </div>
                )}
                {x.link && url && (
                  <a
                    href={webURL(o.instance, x.link)}
                    target="_blank"
                    rel="noreferrer"
                    className="text-xs underline"
                  >
                    Openen in AI-verslag
                  </a>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
      {(o.start?.risico ?? []).length > 0 && (
        <div className="mt-4 text-sm">
          <div className="mb-1 text-xs font-medium tracking-wide text-slate-500 uppercase">
            Hoogste risico per toestel
          </div>
          <div className="flex flex-wrap gap-2">
            {o.start!.risico!.map((r) => (
              <Badge
                key={r.ip}
                tone={r.score >= 7 ? "red" : r.score >= 4 ? "amber" : "slate"}
              >
                {r.naam || r.ip} · {r.score}
              </Badge>
            ))}
          </div>
        </div>
      )}
    </Card>
  );
}

function num(v: unknown) {
  return typeof v === "number" ? v.toLocaleString("nl-BE") : "–";
}

function NetCard({ s }: { s?: AIVStart }) {
  const n = s?.ngfw;
  const rows: [string, string][] = [
    ["DNS-opzoekingen", num(n?.dns?.n)],
    ["DNS geblokkeerd", num(n?.dns?.blok)],
    ["Nieuwe domeinen", num(n?.dns?.nieuw)],
    ["Firewall geblokkeerd", num(n?.firewall?.blok)],
    ["  waarvan binnenkomend", num(n?.firewall?.blok_in)],
    ["CrowdSec-alerts", num(n?.crowdsec?.alerts)],
    ["CrowdSec-bans", num(n?.crowdsec?.bans)],
    ["Zenarmor dreiging", num(n?.zenarmor?.dreiging)],
    ["Open detecties", num(n?.detecties)],
  ];
  return (
    <Card title="Netwerk (24 u)">
      {!n ? (
        <Empty>Niet gelezen.</Empty>
      ) : (
        <dl className="grid grid-cols-[1fr_auto] gap-x-4 gap-y-1 text-sm">
          {rows.map(([k, val]) => (
            <div key={k} className="contents">
              <dt className="whitespace-pre text-slate-500">{k}</dt>
              <dd className="text-right tabular-nums">{val}</dd>
            </div>
          ))}
        </dl>
      )}
      {s?.feeds && (
        <p className="mt-3 text-xs text-slate-500">
          Dreigingsfeeds: {s.feeds.ok ?? 0} ok
          {s.feeds.fout ? `, ${s.feeds.fout} met fout` : ""} · bijgewerkt{" "}
          {aivDate(s.feeds.laatst)}
        </p>
      )}
    </Card>
  );
}

function AlarmCard({ o, admin }: { o: AIVOverview; admin: boolean }) {
  const list = o.app?.meldingen ?? [];
  const now = useAIVNow(o.instance.node_id);
  return (
    <Card title="Alarmen">
      {now.isError && (
        <div className="mb-2">
          <Alert kind="error">
            {now.error instanceof Error ? now.error.message : "Mislukt"}
          </Alert>
        </div>
      )}
      {list.length === 0 ? (
        <Empty>Geen alarmen.</Empty>
      ) : (
        <ul className="space-y-2 text-sm">
          {list.map((m) => (
            <li key={m.id} className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <div className={m.gezien ? "text-slate-500" : "font-medium"}>
                  {m.titel}
                </div>
                <div className="text-xs text-slate-500">
                  {aivDate(m.ts)}
                  {m.gezien ? " · gezien" : ""}
                </div>
                {m.tekst && !m.gezien && (
                  <div className="text-xs text-slate-600 dark:text-slate-400">
                    {m.tekst}
                  </div>
                )}
              </div>
              {admin && !m.gezien && (
                <Button
                  variant="secondary"
                  disabled={now.isPending}
                  onClick={() =>
                    now.mutate({
                      path: "/api/app/actie",
                      body: { gezien: m.id },
                    })
                  }
                >
                  Gezien
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function FindingsCard({ o }: { o: AIVOverview }) {
  const st = o.state;
  return (
    <Card title="Bevindingen">
      {!st ? (
        <Empty>Niet gelezen.</Empty>
      ) : (
        <div className="space-y-4">
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Categorie</th>
                  <th className={thClass}>Hoog</th>
                  <th className={thClass}>Midden</th>
                  <th className={thClass}>Laag</th>
                  <th className={thClass}>Actie nodig</th>
                </tr>
              </thead>
              <tbody>
                {st.cats.map((c) => (
                  <tr
                    key={c.id}
                    className="border-t border-slate-100 dark:border-slate-800"
                  >
                    <td className={tdClass}>
                      {c.label}
                      {c.err && (
                        <div className="text-xs text-red-600 dark:text-red-400">
                          {c.err}
                        </div>
                      )}
                    </td>
                    <td className={tdClass}>
                      {c.hoog > 0 ? <Badge tone="red">{c.hoog}</Badge> : 0}
                    </td>
                    <td className={tdClass}>
                      {c.midden > 0 ? (
                        <Badge tone="amber">{c.midden}</Badge>
                      ) : (
                        0
                      )}
                    </td>
                    <td className={tdClass}>{c.laag}</td>
                    <td className={tdClass}>{c.actie}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {st.top.length > 0 && (
            <ul className="space-y-2 text-sm">
              {st.top.map((it) => (
                <li
                  key={`${it.cat}-${it.id}`}
                  className="flex items-start gap-2"
                >
                  <Badge tone={sevTone[it.sev] ?? "slate"}>{it.sev}</Badge>
                  <div className="min-w-0">
                    <div className="font-medium">{it.titel}</div>
                    {it.uitleg && (
                      <div className="text-slate-600 dark:text-slate-400">
                        {it.uitleg}
                      </div>
                    )}
                    {it.actie_nodig && it.actie && (
                      <div className="text-xs">Advies: {it.actie}</div>
                    )}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </Card>
  );
}

const unitLabel: Record<string, string> = {
  "aiverslag-web.service": "Webdienst",
  "aiverslag-run.timer": "Analyse (elk uur)",
  "aiverslag-run.service": "Laatste analyse",
  "aiverslag-inzicht.timer": "Inzicht (elke 10 min)",
  "aiverslag-inzicht.service": "Laatste Inzicht-ronde",
  "aiverslag-werker.timer": "Werker (elke 5 min)",
  "aiverslag-werker.service": "Laatste werkerronde",
  "aiverslag-github.timer": "Momentopname naar GitHub",
  "ollama.service": "Ollama (AI-model)",
  "nginx.service": "nginx",
};

function unitState(u: AIVUnit) {
  if (u.active === "failed")
    return `mislukt${u.result ? ` (${u.result})` : ""}`;
  if (u.active === "active")
    return u.sub === "running"
      ? "draait"
      : u.sub === "waiting"
        ? "wacht"
        : u.sub;
  if (u.active === "activating") return "bezig";
  if (u.active === "inactive" && u.result === "success") return "klaar";
  return u.active;
}

function ServiceCard({ o }: { o: AIVOverview }) {
  const v = o.instance;
  return (
    <Card title="Dienst">
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
        <dt className="text-slate-500">Machine</dt>
        <dd>
          {v.hostname} {v.address ? `(${v.address})` : ""}
        </dd>
        <dt className="text-slate-500">Versie</dt>
        <dd>{v.facts.version ?? o.start?.versie ?? "–"}</dd>
        <dt className="text-slate-500">AI-model</dt>
        <dd>{v.facts.model || "–"}</dd>
        <dt className="text-slate-500">Schijf</dt>
        <dd>
          {v.facts.data_used_pct != null
            ? `${Math.round(v.facts.data_used_pct)}% gebruikt`
            : "–"}
        </dd>
        <dt className="text-slate-500">Koppeling</dt>
        <dd>
          {v.facts.paired
            ? "gekoppeld als app ClusterForge"
            : "nog niet (gebeurt bij de eerste vraag)"}
        </dd>
      </dl>
      <ul className="mt-3 space-y-1 text-sm">
        {v.units.map((u) => (
          <li key={u.name} className="flex items-center gap-2" title={u.name}>
            <UnitDot active={u.active} />
            <span className="min-w-0 flex-1 truncate">
              {unitLabel[u.name] ?? u.name}
            </span>
            <span className="text-xs text-slate-500">
              {unitState(u)}
              {u.since ? ` · ${aivDate(u.since)}` : ""}
            </span>
          </li>
        ))}
      </ul>
      {webURL(v) && (
        <a
          href={webURL(v)}
          target="_blank"
          rel="noreferrer"
          className="mt-3 inline-block text-sm underline"
        >
          AI-verslag openen
        </a>
      )}
    </Card>
  );
}

// ExtCard: de koppeling ext56 in AI-verslag, die context uit ClusterForge
// (servers, VIP's, onderhoud, wijzigingen) in de kennisbank zet.
function ExtCard({ o, admin }: { o: AIVOverview; admin: boolean }) {
  const ins = useAIVInstall(o.instance.node_id);
  const [sure, setSure] = useState(false);
  const e = o.ext;
  const tooOld = o.instance.protocol < 54;
  return (
    <Card title="Koppeling in AI-verslag">
      <p className="text-sm text-slate-600 dark:text-slate-400">
        ClusterForge vertelt AI-verslag elke 5 minuten welke adressen jouw
        servers en VIP&apos;s zijn, wanneer er gepland onderhoud is en wat er de
        laatste 24 uur veranderde. Minder vals alarm bij een herstart of
        failover; een gekaapte server valt nog altijd op.
      </p>
      {!e ? (
        <p className="mt-3 text-sm text-slate-500">Stand niet gelezen.</p>
      ) : (
        <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
          <dt className="text-slate-500">Stand</dt>
          <dd>
            {e.installed ? (
              <Badge tone={e.update ? "amber" : "green"}>
                {e.update ? "update klaar" : "geïnstalleerd"}
              </Badge>
            ) : (
              <Badge>niet geïnstalleerd</Badge>
            )}
          </dd>
          <dt className="text-slate-500">Versie</dt>
          <dd>
            {e.version ?? "–"}
            {e.update || !e.installed ? ` (nieuw: ${e.bundled})` : ""}
          </dd>
          {e.installed && (
            <>
              <dt className="text-slate-500">Regels</dt>
              <dd>
                {e.rules} in de kennisbank
                {e.stand?.nodes != null
                  ? ` (${e.stand.nodes} servers, ${e.stand.vips ?? 0} VIP's)`
                  : ""}
              </dd>
              <dt className="text-slate-500">Laatst gestuurd</dt>
              <dd>
                {e.pushed_at
                  ? aivDate(Date.parse(e.pushed_at))
                  : e.stand?.ts
                    ? aivDate(e.stand.ts)
                    : "–"}
              </dd>
              {(e.stand?.onderhoud?.length ?? 0) > 0 && (
                <>
                  <dt className="text-slate-500">Onderhoud nu</dt>
                  <dd>{e.stand?.onderhoud?.join(", ")}</dd>
                </>
              )}
            </>
          )}
        </dl>
      )}
      {e?.push_error && (
        <div className="mt-3">
          <Alert kind="error">Context sturen mislukt: {e.push_error}</Alert>
        </div>
      )}
      {ins.isError && (
        <div className="mt-3">
          <Alert kind="error">
            {ins.error instanceof Error ? ins.error.message : "Mislukt"}
          </Alert>
        </div>
      )}
      {ins.data && (
        <div className="mt-3">
          <Alert kind="success">
            Taak gestart.{" "}
            <Link
              href={`/taken/detail?id=${ins.data.id}`}
              className="underline"
            >
              Volgen
            </Link>
          </Alert>
        </div>
      )}
      {admin && e && (!e.installed || e.update) && (
        <div className="mt-3 space-y-2">
          {tooOld ? (
            <p className="text-xs text-slate-500">
              Werk eerst de agent bij (Nodes → Agents bijwerken).
            </p>
          ) : (
            <>
              <label className="flex items-start gap-2 text-xs text-slate-600 dark:text-slate-400">
                <input
                  type="checkbox"
                  checked={sure}
                  onChange={(x) => setSure(x.target.checked)}
                  className="mt-0.5"
                />
                <span>
                  De webdienst van AI-verslag herstart even (een kopie van
                  app.py wordt teruggezet als hij niet meer start).
                </span>
              </label>
              <Button
                disabled={!sure || ins.isPending}
                onClick={() => ins.mutate()}
              >
                {e.installed ? "Koppeling bijwerken" : "Koppeling installeren"}
              </Button>
            </>
          )}
        </div>
      )}
    </Card>
  );
}
