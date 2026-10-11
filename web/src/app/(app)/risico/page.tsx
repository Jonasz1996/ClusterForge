"use client";

import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { Suspense, useMemo, useState, type ReactNode } from "react";
import { Empty, EnvBadge, QueryState, StatusBadge, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { AcceptDialog, FindingItem, GradeBadge } from "@/components/risk/Risk";
import { Alert, Badge, Button, Card, cx, PageHeader, Select } from "@/components/ui";
import { impacts } from "@/lib/deps";
import { environments, envInfo, useIsAdmin } from "@/lib/inventory";
import {
  cardLabels,
  kindInfo,
  kindOrder,
  useRisk,
  useRiskComponents,
  useSPOFs,
  useWhatIf,
  type RiskCardId,
  type RiskClusterOutcome,
  type RiskComponent,
  type RiskFinding,
  type RiskGuestOutcome,
  type RiskSPOF,
  type RiskWhatIf,
} from "@/lib/risk";

const tabs = [
  { id: "bevindingen", label: "Bevindingen" },
  { id: "spof", label: "SPOF's" },
  { id: "wat-als", label: "Wat als" },
] as const;
type Tab = (typeof tabs)[number]["id"];

// Zoveel onderdelen rekent de wat-als in één keer.
const maxComponents = 20;

const linkClass = "text-brand-600 hover:underline dark:text-sky-300";

export default function RiskPage() {
  return (
    <Suspense>
      <RiskInner />
    </Suspense>
  );
}

// De tab en de gekozen onderdelen staan in de URL, zodat een bevinding over
// een SPOF meteen de wat-als met dat onderdeel opent.
function RiskInner() {
  const params = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();
  const isAdmin = useIsAdmin();
  const raw = params.get("tab");
  const tab: Tab = tabs.some((t) => t.id === raw) ? (raw as Tab) : "bevindingen";
  const chosen = params.getAll("onderdeel");

  const go = (t: Tab, onderdelen: string[] = t === "wat-als" ? chosen : []) => {
    const next = new URLSearchParams();
    if (t !== "bevindingen") next.set("tab", t);
    for (const o of onderdelen) next.append("onderdeel", o);
    const s = next.toString();
    router.replace(s ? `${pathname}?${s}` : pathname, { scroll: false });
  };

  return (
    <div className="space-y-5">
      <PageHeader
        title="Risico"
        description="Wat de health score drukt, welke onderdelen alleen al productie down leggen, en wat er gebeurt als iets uitvalt."
      />
      <div className="flex gap-1 overflow-x-auto border-b border-slate-200 dark:border-slate-800" role="tablist">
        {tabs.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={tab === t.id}
            onClick={() => go(t.id)}
            className={cx(
              "-mb-px border-b-2 px-3 py-2 text-sm font-medium whitespace-nowrap",
              tab === t.id
                ? "border-brand-600 text-brand-700 dark:text-sky-300"
                : "border-transparent text-slate-500 hover:text-slate-700 dark:hover:text-slate-300",
            )}
          >
            {t.label}
          </button>
        ))}
      </div>
      {tab === "bevindingen" && <Findings isAdmin={isAdmin} />}
      {tab === "spof" && <SPOFs onOpen={(key) => go("wat-als", [key])} />}
      {tab === "wat-als" && <WhatIf chosen={chosen} onChange={(keys) => go("wat-als", keys)} />}
    </div>
  );
}

// Findings is elke bevinding, de grootste winst bovenaan, te filteren op
// kaart en omgeving.
function Findings({ isAdmin }: { isAdmin: boolean }) {
  const q = useRisk();
  const [card, setCard] = useState<RiskCardId | "">("");
  const [env, setEnv] = useState("");
  const [showAccepted, setShowAccepted] = useState(true);
  const [accepting, setAccepting] = useState<RiskFinding | null>(null);
  const d = q.data;
  const all = d?.findings ?? [];
  const list = all
    .filter((f) => (card ? f.card === card : true))
    .filter((f) => (env === "" ? true : env === "platform" ? f.cluster === null : f.cluster?.environment === env))
    .filter((f) => showAccepted || !f.accepted)
    .sort((a, b) => Number(!!a.accepted) - Number(!!b.accepted) || b.fleet_gain - a.fleet_gain);
  const open = all.filter((f) => !f.accepted).length;
  return (
    <QueryState q={q}>
      {d && (
        <div className="space-y-4">
          <p className="flex flex-wrap items-center gap-2 text-sm text-slate-600 dark:text-slate-400">
            <GradeBadge score={d.score} grade={d.grade} />
            <span>
              {open === 1 ? "1 open bevinding" : `${open} open bevindingen`}
              {all.length > open && `, ${all.length - open} geaccepteerd`}. De grootste winst voor de score staat bovenaan.
            </span>
          </p>
          <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-sm">
            <Select
              aria-label="Kaart"
              className="w-auto"
              value={card}
              onChange={(e) => setCard(e.target.value as RiskCardId | "")}
            >
              <option value="">Alle kaarten</option>
              {(Object.keys(cardLabels) as RiskCardId[]).map((c) => (
                <option key={c} value={c}>
                  {cardLabels[c]}
                </option>
              ))}
            </Select>
            <Select aria-label="Omgeving" className="w-auto" value={env} onChange={(e) => setEnv(e.target.value)}>
              <option value="">Alle omgevingen</option>
              {environments.map((e) => (
                <option key={e.value} value={e.value}>
                  {e.label}
                </option>
              ))}
              <option value="platform">ClusterForge zelf</option>
            </Select>
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={showAccepted} onChange={(e) => setShowAccepted(e.target.checked)} />
              Geaccepteerde tonen
            </label>
          </div>
          {!d.computed_at ? (
            <Empty>De score is nog niet berekend; dat gebeurt enkele minuten na de start.</Empty>
          ) : list.length === 0 ? (
            <Empty>
              {all.length === 0 ? "Geen bevindingen. Alles staat zoals het hoort." : "Geen bevindingen met deze filters."}
            </Empty>
          ) : (
            <Card>
              <ul className="-my-2 divide-y divide-slate-100 dark:divide-slate-800">
                {list.map((f) => (
                  <FindingItem
                    key={`${f.check}-${f.subject.type}-${f.subject.id}-${f.cluster?.id ?? ""}`}
                    f={f}
                    gain={f.fleet_gain}
                    cluster={f.cluster?.name ?? "ClusterForge"}
                    isAdmin={isAdmin}
                    onAccept={setAccepting}
                  />
                ))}
              </ul>
            </Card>
          )}
        </div>
      )}
      {accepting && <AcceptDialog f={accepting} onClose={() => setAccepting(null)} />}
    </QueryState>
  );
}

function KindBadge({ kind }: { kind: RiskComponent["kind"] }) {
  return <Badge tone="blue">{kindInfo[kind].label}</Badge>;
}

// SPOFs zijn de onderdelen die alleen al een prod-cluster of een bevestigde
// prod-dienst down leggen, ook nadat HA herstart wat het kan.
function SPOFs({ onOpen }: { onOpen: (key: string) => void }) {
  const q = useSPOFs();
  const spofs = q.data?.spofs ?? [];
  return (
    <div className="space-y-4">
      <p className="text-sm text-slate-600 dark:text-slate-400">
        Een single point of failure is een host, storage, bridge of node waarvan de uitval alleen al een productiecluster of een
        bevestigde productiedienst down legt, ook nadat Proxmox HA herstart wat het kan. ClusterForge rekent elk onderdeel apart,
        alsof de rest gezond is.
      </p>
      <QueryState q={q}>
        {q.data && spofs.length === 0 ? (
          <Empty>
            Geen single points of failure in productie. Elk onderdeel kan alleen uitvallen zonder dat productie down gaat.
          </Empty>
        ) : (
          <ul className="space-y-3">
            {spofs.map((s) => (
              <SPOFItem key={s.component.key} s={s} onOpen={onOpen} />
            ))}
          </ul>
        )}
      </QueryState>
    </div>
  );
}

function SPOFItem({ s, onOpen }: { s: RiskSPOF; onOpen: (key: string) => void }) {
  const c = s.component;
  const prod = s.clusters.length + s.services.length;
  return (
    <li className="rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <p className="flex flex-wrap items-center gap-2">
            <KindBadge kind={c.kind} />
            <span className="font-medium break-words">{c.name}</span>
            {c.group && <span className="text-sm text-slate-500">{c.group}</span>}
            {s.self && <Badge tone="red">ook ClusterForge zelf</Badge>}
          </p>
          <p className="text-sm text-slate-600 dark:text-slate-400">
            Valt dit uit, dan {s.down === 1 ? "gaat 1 cluster of dienst" : `gaan ${s.down} clusters of diensten`} down
            {s.down > prod && `, waarvan ${prod} in productie`}.
          </p>
        </div>
        <Button variant="secondary" onClick={() => onOpen(c.key)}>
          Bekijk in wat-als
        </Button>
      </div>
      <ul className="mt-3 space-y-1 text-sm">
        {s.clusters.map((cl) => (
          <li key={cl.id} className="flex flex-wrap items-baseline gap-x-2">
            <Badge tone="red">Down</Badge>
            <Link href={`/clusters/detail?id=${cl.id}`} className={linkClass}>
              {cl.name}
            </Link>
            <span className="text-slate-500">{cl.reason}</span>
          </li>
        ))}
        {s.services.map((sv) => (
          <li key={sv.id} className="flex flex-wrap items-baseline gap-x-2">
            <Badge tone="red">Down</Badge>
            <Link href={`/afhankelijkheden?dienst=${sv.id}`} className={linkClass}>
              {sv.name}
            </Link>
            <span className="text-slate-500">
              in {sv.group}: {sv.reason}
            </span>
          </li>
        ))}
      </ul>
    </li>
  );
}

// componentLabel is een onderdeel in de keuzelijst: een node of dienst met
// zijn cluster, een host of storage met de koppeling als er meer zijn.
function componentLabel(c: RiskComponent, manyConns: boolean) {
  switch (c.kind) {
    case "node":
    case "service":
      return c.group ? `${c.name} in ${c.group}` : c.name;
    case "cluster":
      return `${c.name} (${envInfo(c.group).label})`;
    default:
      return manyConns && c.group ? `${c.name} · ${c.group}` : c.name;
  }
}

function WhatIf({ chosen, onChange }: { chosen: string[]; onChange: (keys: string[]) => void }) {
  const comps = useRiskComponents();
  const all = useMemo(() => comps.data?.components ?? [], [comps.data]);
  const byKey = useMemo(() => new Map(all.map((c) => [c.key, c])), [all]);
  const manyConns = new Set(all.filter((c) => ["host", "storage", "bridge"].includes(c.kind)).map((c) => c.group)).size > 1;
  const result = useWhatIf(chosen);
  const full = chosen.length >= maxComponents;

  return (
    <div className="space-y-4">
      <p className="text-sm text-slate-600 dark:text-slate-400">
        Kies wat er uitvalt en zie per cluster de voorspelde status, welke VM&apos;s Proxmox HA herstart en welke diensten geraakt
        worden. De voorspelling gaat uit van de huidige toestand en de bekende afhankelijkheden; er gebeurt niets echt.
      </p>
      <QueryState q={comps}>
        <div className="flex flex-wrap items-center gap-2">
          <Select
            aria-label="Onderdeel toevoegen"
            className="w-full sm:w-80"
            value=""
            disabled={full || all.length === 0}
            onChange={(e) => e.target.value && onChange([...chosen, e.target.value])}
          >
            <option value="">{full ? `Hoogstens ${maxComponents} onderdelen` : "Onderdeel toevoegen…"}</option>
            {kindOrder.map((k) => {
              const list = all.filter((c) => c.kind === k);
              if (list.length === 0) return null;
              return (
                <optgroup key={k} label={kindInfo[k].plural}>
                  {list.map((c) => (
                    <option key={c.key} value={c.key} disabled={chosen.includes(c.key)}>
                      {componentLabel(c, manyConns)}
                    </option>
                  ))}
                </optgroup>
              );
            })}
          </Select>
          {chosen.length > 0 && (
            <Button variant="ghost" onClick={() => onChange([])}>
              Alles wissen
            </Button>
          )}
        </div>
        {chosen.length > 0 && (
          <ul className="flex flex-wrap gap-2" aria-label="Valt uit">
            {chosen.map((key) => {
              const c = byKey.get(key);
              return (
                <li
                  key={key}
                  className="inline-flex items-center gap-1.5 rounded-full border border-slate-300 bg-white py-0.5 pr-1 pl-2.5 text-sm dark:border-slate-700 dark:bg-slate-900"
                >
                  {c ? (
                    <>
                      <span className="text-xs text-slate-500">{kindInfo[c.kind].label}</span>
                      <span>{componentLabel(c, manyConns)}</span>
                    </>
                  ) : (
                    <span className="text-slate-500">{key}</span>
                  )}
                  <button
                    type="button"
                    className="rounded-full px-1.5 text-slate-500 hover:bg-slate-100 hover:text-slate-800 dark:hover:bg-slate-800 dark:hover:text-slate-200"
                    aria-label={`${c ? c.name : key} weghalen`}
                    onClick={() => onChange(chosen.filter((k) => k !== key))}
                  >
                    ×
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </QueryState>
      {chosen.length === 0 ? (
        <Empty>
          Kies hierboven een of meer onderdelen, zoals een Proxmox-host of een storage. Op het tabblad SPOF&apos;s staan de
          onderdelen die alleen al productie down leggen.
        </Empty>
      ) : result.isError ? (
        <Alert>{result.error instanceof Error ? result.error.message : "Rekenen mislukt"}</Alert>
      ) : !result.data ? (
        <p className="text-sm text-slate-500">Rekenen…</p>
      ) : (
        <WhatIfResult r={result.data} stale={result.isFetching && result.isPlaceholderData} />
      )}
    </div>
  );
}

// sentence maakt van een reden zoals "pve1 valt uit" een zin.
function sentence(s: string) {
  if (!s) return "";
  const t = s.charAt(0).toUpperCase() + s.slice(1);
  return /[.!?]$/.test(t) ? t : `${t}.`;
}

function Section({ title, count, children }: { title: string; count?: number; children: ReactNode }) {
  return (
    <Card
      title={
        <span>
          {title}
          {count !== undefined && <span className="ml-1.5 font-normal text-slate-500">({count})</span>}
        </span>
      }
    >
      {children}
    </Card>
  );
}

function WhatIfResult({ r, stale }: { r: RiskWhatIf; stale: boolean }) {
  const down = r.guests.filter((g) => g.outcome === "down");
  const restarted = r.guests.filter((g) => g.outcome === "restarted");
  const partial = r.guests.filter((g) => g.outcome === "partial");
  const nothing = r.guests.length === 0 && r.clusters.length === 0 && r.services.length === 0 && !r.self;
  return (
    <div className={cx("space-y-4", stale && "opacity-60")} aria-busy={stale}>
      {!nothing && (
        <div className="text-right text-sm">
          <Link href={`/kaart?${new URLSearchParams(r.components.map((c) => ["onderdeel", c.key]))}`} className={linkClass}>
            Op de kaart tonen →
          </Link>
        </div>
      )}
      {r.self && (
        <Alert kind={r.self.outcome === "down" ? "error" : "info"}>
          {r.self.outcome === "down" ? (
            <>
              <span className="font-medium">ClusterForge valt zelf uit</span>: {r.self.reason}. Tot hij terug is, zie je niets in
              ClusterForge en draaien er geen failovers of taken.
            </>
          ) : (
            <>
              <span className="font-medium">ClusterForge valt even uit</span>: {r.self.reason}. Proxmox HA start hem opnieuw op{" "}
              {r.self.restart_host}; tot dan zie je niets in ClusterForge.
            </>
          )}
        </Alert>
      )}
      {r.notes.length === 1 && <Alert kind="info">{r.notes[0]}</Alert>}
      {r.notes.length > 1 && (
        <Alert kind="info">
          <ul className="list-disc space-y-0.5 pl-4">
            {r.notes.map((n) => (
              <li key={n}>{n}</li>
            ))}
          </ul>
        </Alert>
      )}
      {nothing && <Empty>Hier valt niets door uit: geen cluster, VM of dienst die ClusterForge kent.</Empty>}
      {r.clusters.length > 0 && (
        <Section title="Clusters" count={r.clusters.length}>
          <ClusterTable clusters={r.clusters} />
        </Section>
      )}
      {r.guests.length > 0 && (
        <Section title="VM's en containers" count={r.guests.length}>
          <div className="space-y-4">
            <GuestGroup title="Vallen uit" tone="red" guests={down} />
            <GuestGroup title="Proxmox HA herstart ze" tone="green" guests={restarted} />
            <GuestGroup title="Draaien door met minder netwerk" tone="amber" guests={partial} />
          </div>
        </Section>
      )}
      {r.services.length > 0 && (
        <Section title="Diensten" count={r.services.length}>
          <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
            {r.services.map((s) => (
              <li key={s.id} className="flex flex-col gap-1 py-2 sm:flex-row sm:items-baseline sm:gap-3">
                <span className="flex shrink-0 flex-wrap items-center gap-1.5">
                  <Badge tone={impacts[s.impact].tone}>{s.impact === "down" ? "Down" : "Verminderd"}</Badge>
                  {s.recovers && <Badge tone="green">komt terug na HA</Badge>}
                </span>
                <span className="min-w-0 break-words">
                  <Link href={`/afhankelijkheden?dienst=${s.id}`} className={linkClass}>
                    {s.name}
                  </Link>
                  <span className="text-slate-500">
                    {" "}
                    in {s.group}
                    {s.environment && <> · {envInfo(s.environment).label}</>}: {s.reason}
                  </span>
                </span>
              </li>
            ))}
          </ul>
        </Section>
      )}
    </div>
  );
}

function Prediction({ p, show }: { p: RiskClusterOutcome["before"]; show: boolean }) {
  return (
    <div className="space-y-0.5">
      <StatusBadge status={p.status} reason={p.reason} />
      {show && p.reason && <p className="max-w-xs text-xs text-slate-500">{p.reason}</p>}
    </div>
  );
}

function ClusterTable({ clusters }: { clusters: RiskClusterOutcome[] }) {
  return (
    <div className="-mx-5 overflow-x-auto">
      <table className={tableClass}>
        <thead>
          <tr className="border-b border-slate-200 dark:border-slate-800">
            <th className={cx(thClass, "pl-5")}>Cluster</th>
            <th className={thClass}>Nu</th>
            <th className={thClass}>Bij uitval</th>
            <th className={thClass}>Na HA</th>
            <th className={cx(thClass, "pr-5")}>Nodes weg</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {clusters.map((c) => (
            <tr key={c.id}>
              <td className={cx(tdClass, "pl-5")}>
                <div className="flex flex-wrap items-center gap-1.5">
                  <Link href={`/clusters/detail?id=${c.id}`} className={cx(linkClass, "font-medium")}>
                    {c.name}
                  </Link>
                  <EnvBadge env={c.environment} />
                </div>
              </td>
              <td className={tdClass}>
                <Prediction p={c.before} show={false} />
              </td>
              <td className={tdClass}>
                <Prediction p={c.during} show={c.during.status !== c.before.status || c.during.reason !== c.before.reason} />
              </td>
              <td className={tdClass}>
                {c.after_ha ? (
                  <Prediction p={c.after_ha} show={c.after_ha.reason !== c.during.reason} />
                ) : (
                  <span className="text-slate-400" title="HA herstart in dit cluster niets">
                    –
                  </span>
                )}
              </td>
              <td className={cx(tdClass, "pr-5 text-slate-600 dark:text-slate-400")}>
                {c.nodes_down.length > 0 ? c.nodes_down.join(", ") : "geen"}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function GuestGroup({ title, tone, guests }: { title: string; tone: "red" | "green" | "amber"; guests: RiskGuestOutcome[] }) {
  if (guests.length === 0) return null;
  return (
    <div>
      <h3 className="flex items-center gap-2 text-sm font-medium">
        <Badge tone={tone}>{guests.length}</Badge>
        {title}
      </h3>
      <ul className="mt-1 divide-y divide-slate-100 text-sm dark:divide-slate-800">
        {guests.map((g) => (
          <li key={`${g.connection}-${g.vmid}`} className="flex flex-col gap-0.5 py-1.5 sm:flex-row sm:items-baseline sm:gap-3">
            <span className="flex shrink-0 flex-wrap items-center gap-1.5">
              {g.node_id ? (
                <Link href={`/nodes/detail?id=${g.node_id}`} className={linkClass}>
                  {g.hostname || g.name}
                </Link>
              ) : (
                <span>{g.name || `VM ${g.vmid}`}</span>
              )}
              <span className="text-xs text-slate-500">
                {g.vmid} op {g.host}
              </span>
              {g.unknown && <Badge>geen node</Badge>}
            </span>
            <span className="min-w-0 text-slate-500">
              {sentence(g.reason)}
              {g.outcome === "restarted" && g.restart_host && <> HA start hem op {g.restart_host}.</>}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}
