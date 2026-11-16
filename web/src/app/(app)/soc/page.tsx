"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { AuditList } from "@/components/audit/AuditList";
import { askConfirm } from "@/components/Confirm";
import { Modal } from "@/components/Modal";
import { toast } from "@/components/Toast";
import { ago, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, PageHeader, Select, cx } from "@/components/ui";
import { levelStyle } from "@/components/soc/SocStrip";
import { ApiError } from "@/lib/api/client";
import { useAuditRecent } from "@/lib/audit";
import { copyText } from "@/lib/clipboard";
import { flag, useCrowdSecDecide } from "@/lib/crowdsec";
import { useIsAdmin } from "@/lib/inventory";
import {
  levelLabel,
  useSoc,
  windowLabel,
  type SocAttacker,
  type SocCount,
  type SocCoverage,
  type SocItem,
  type SocOverview,
  type SocWindow,
} from "@/lib/soc";

const nf = new Intl.NumberFormat("nl-BE");
const timeFmt = new Intl.DateTimeFormat("nl-BE", { hour: "2-digit", minute: "2-digit" });
const dayFmt = new Intl.DateTimeFormat("nl-BE", { weekday: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });

export default function SocPage() {
  return (
    <Suspense>
      <Soc />
    </Suspense>
  );
}

function Soc() {
  const params = useSearchParams();
  const router = useRouter();
  const win: SocWindow = params.get("venster") === "7d" ? "7d" : "24h";
  const ip = params.get("ip") ?? "";
  const q = useSoc(win);
  const go = (w: SocWindow, ipNext: string) => {
    const next = new URLSearchParams();
    if (w === "7d") next.set("venster", "7d");
    if (ipNext) next.set("ip", ipNext);
    const qs = next.toString();
    router.replace(qs ? `/soc?${qs}` : "/soc", { scroll: false });
  };
  const setWin = (w: SocWindow) => go(w, ip);
  const openIP = (a: string) => go(win, a);
  const o = q.data;

  return (
    <div className="space-y-6">
      <PageHeader
        title="SOC"
        description="Hoe veilig je clusters er nu voor staan: wie aanvalt, wat tegengehouden wordt en waar de bescherming nog gaten heeft. Alles uit CrowdSec, de firewall en de logins van ClusterForge."
        actions={
          <div className="flex items-center gap-2">
            <div role="group" aria-label="Periode" className="inline-flex rounded-md border border-slate-300 p-0.5 dark:border-slate-700">
              {(["24h", "7d"] as const).map((w) => (
                <button
                  key={w}
                  onClick={() => setWin(w)}
                  aria-pressed={win === w}
                  className={cx(
                    "rounded px-3 py-1.5 text-sm font-medium",
                    win === w ? "bg-brand-600 text-white" : "text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800",
                  )}
                >
                  {windowLabel[w]}
                </button>
              ))}
            </div>
            <Button variant="secondary" onClick={() => void q.refetch()} disabled={q.isFetching} aria-label="Vernieuwen">
              {q.isFetching ? "…" : "↻"}
            </Button>
          </div>
        }
      />
      {q.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      {q.isError && <Alert>Het beveiligingsoverzicht laden mislukte. Het wordt zo opnieuw geprobeerd.</Alert>}
      {o && (
        <>
          <LevelBand o={o} />
          <Tiles o={o} />
          <div className="grid gap-6 xl:grid-cols-5">
            <NowCard items={o.now} className="xl:col-span-3" />
            <ScoreCard o={o} className="xl:col-span-2" />
          </div>
          <ActivityCard o={o} />
          <AttackersCard o={o} onOpen={openIP} />
          <TargetsCard o={o} />
          <CoverageCard rows={o.coverage} />
          <SecurityLog />
          {ip && <AttackerDialog ip={ip} o={o} onClose={() => openIP("")} />}
        </>
      )}
    </div>
  );
}

// --- niveau -------------------------------------------------------------------


function LevelBand({ o }: { o: SocOverview }) {
  const l = o.level.level;
  const st = levelStyle[l];
  return (
    <section aria-label="Dreigingsniveau" className={cx("flex flex-wrap items-center gap-x-4 gap-y-2 rounded-lg border px-4 py-3", st.ring, st.bg)}>
      <span className={cx("inline-flex items-center gap-2 text-sm font-bold uppercase tracking-widest", st.text)}>
        <span className="relative flex size-2.5" aria-hidden>
          {l !== "normaal" && <span className={cx("absolute inline-flex size-full animate-ping rounded-full opacity-60", st.dot)} />}
          <span className={cx("relative inline-flex size-2.5 rounded-full", st.dot)} />
        </span>
        {levelLabel[l]}
        <span className="text-xs font-normal normal-case tracking-normal text-slate-500">dreigingsniveau</span>
      </span>
      <span className="order-last w-full min-w-0 text-sm text-slate-700 sm:order-none sm:w-auto sm:flex-1 dark:text-slate-300">
        {o.level.reasons.join(" · ")}
      </span>
      <span className="ml-auto text-xs text-slate-500 sm:ml-0">bijgewerkt {timeFmt.format(new Date(o.generated_at))}</span>
    </section>
  );
}

// --- tegels -------------------------------------------------------------------

function Delta({ now, prev, win, upIsBad = true }: { now: number; prev: number; win: SocWindow; upIsBad?: boolean }) {
  const d = now - prev;
  const per = win === "24h" ? "gisteren" : "vorige week";
  if (d === 0) return <>gelijk aan {per}</>;
  const bad = upIsBad ? d > 0 : d < 0;
  return (
    <>
      <span className={bad ? "text-red-600 dark:text-red-400" : "text-emerald-700 dark:text-emerald-400"}>
        {d > 0 ? "▲" : "▼"} {nf.format(Math.abs(d))}
      </span>{" "}
      t.o.v. {per}
    </>
  );
}

function Tile({
  href,
  label,
  value,
  sub,
  tone,
  children,
}: {
  href: string;
  label: string;
  value: string;
  sub: React.ReactNode;
  tone?: "red" | "amber" | "green";
  children?: React.ReactNode;
}) {
  return (
    <Link
      href={href}
      className="group flex min-w-0 flex-col rounded-lg border border-slate-200 bg-white p-4 shadow-xs transition hover:border-brand-500 dark:border-slate-800 dark:bg-slate-900"
    >
      <span className="text-xs font-medium uppercase tracking-wide text-slate-500">{label}</span>
      <span
        className={cx(
          "mt-1 text-2xl font-semibold tabular-nums",
          tone === "red" && "text-red-600 dark:text-red-400",
          tone === "amber" && "text-amber-600 dark:text-amber-400",
          tone === "green" && "text-emerald-600 dark:text-emerald-400",
        )}
      >
        {value}
      </span>
      <span className="mt-0.5 text-xs text-slate-500">{sub}</span>
      {children}
    </Link>
  );
}

function Sparkline({ points }: { points: number[] }) {
  if (points.length < 2) return null;
  const w = 120;
  const h = 24;
  const min = Math.min(...points);
  const max = Math.max(...points);
  const span = max - min || 1;
  const xy = points.map((v, i) => [1 + (i * (w - 2)) / (points.length - 1), h - 2 - ((v - min) / span) * (h - 4)]);
  return (
    <svg viewBox={`0 0 ${w} ${h}`} width={w} height={h} className="mt-2 text-brand-500" aria-hidden>
      <polyline fill="none" stroke="currentColor" strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" points={xy.map((p) => p.join(",")).join(" ")} />
    </svg>
  );
}

function Tiles({ o }: { o: SocOverview }) {
  const s = o.stats;
  const sc = o.score;
  const w = o.window;
  return (
    <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
      <Tile
        href="#score"
        label="Beveiligingsscore"
        value={`${sc.score}/100`}
        tone={sc.score < 50 ? "red" : sc.score < 80 ? "amber" : "green"}
        sub={sc.previous == null ? "nog geen vergelijking" : <Delta now={sc.score} prev={sc.previous} win={w} upIsBad={false} />}
      >
        <Sparkline points={sc.trend.map((p) => p.score)} />
      </Tile>
      <Tile
        href="/crowdsec?tab=alerts"
        label="Aanvallen"
        value={nf.format(s.attacks)}
        tone={s.attacks > 0 ? "amber" : undefined}
        sub={<Delta now={s.attacks} prev={s.attacks_prev} win={w} />}
      />
      <Tile href="/crowdsec?tab=bans" label="Actieve bans" value={nf.format(s.active_bans)} sub="eigen bans, zonder de community-lijst" />
      <Tile
        href="/firewall?tab=verkeer"
        label="Tegengehouden"
        value={nf.format(s.blocked)}
        sub={<Delta now={s.blocked} prev={s.blocked_prev} win={w} />}
      />
      <Tile
        href="/logboek?category=security"
        label="Mislukte logins"
        value={nf.format(s.login_failures)}
        tone={s.login_failures > 0 ? "amber" : undefined}
        sub={<Delta now={s.login_failures} prev={s.login_failures_prev} win={w} />}
      />
      <Tile
        href="#dekking"
        label="Beschermd"
        value={`${s.protected}/${s.nodes}`}
        tone={s.nodes > 0 && s.protected < s.nodes ? "amber" : "green"}
        sub={s.security_nodes > 0 ? `${s.security_nodes} node${s.security_nodes === 1 ? "" : "s"} met beveiligingsupdates` : "nodes zonder gaten"}
      />
    </div>
  );
}

// --- nu belangrijk ---------------------------------------------------------------

const toneDot: Record<SocItem["tone"], string> = { red: "bg-red-500", amber: "bg-amber-400", info: "bg-sky-400" };
const toneLabel: Record<SocItem["tone"], string> = { red: "ernstig", amber: "waarschuwing", info: "tip" };
const shownItems = 6;

function NowCard({ items, className }: { items: SocItem[]; className?: string }) {
  const [all, setAll] = useState(false);
  const list = all ? items : items.slice(0, shownItems);
  return (
    <Card title="Nu belangrijk" className={className}>
      {items.length === 0 ? (
        <p className="flex items-center gap-2 text-sm text-emerald-700 dark:text-emerald-300">
          <span className="size-2 rounded-full bg-emerald-500" aria-hidden />
          Niets dat nu je aandacht vraagt.
        </p>
      ) : (
        <ul className="-mx-2 space-y-1">
          {list.map((it) => (
            <li key={it.key} className="flex items-start gap-3 rounded-md px-2 py-2 hover:bg-slate-50 dark:hover:bg-slate-800/60">
              <span aria-label={toneLabel[it.tone]} className={cx("mt-1.5 size-2.5 shrink-0 rounded-full", toneDot[it.tone])} />
              <span className="min-w-0 flex-1">
                <span className="block text-sm font-medium break-words">{it.title}</span>
                {it.detail && <span className="block text-xs text-slate-500 break-words">{it.detail}</span>}
              </span>
              <Link
                href={it.href}
                className="shrink-0 rounded-md border border-slate-300 px-2.5 py-1 text-xs font-medium text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
              >
                {it.action}
              </Link>
            </li>
          ))}
        </ul>
      )}
      {items.length > shownItems && (
        <button className="mt-2 text-sm font-medium text-brand-700 hover:underline dark:text-sky-300" onClick={() => setAll(!all)}>
          {all ? "Minder tonen" : `Alle ${items.length} tonen`}
        </button>
      )}
    </Card>
  );
}

// --- score ----------------------------------------------------------------------

function ScoreCard({ o, className }: { o: SocOverview; className?: string }) {
  const parts = o.score.parts;
  const lost = parts.flatMap((p) => p.deductions);
  return (
    <section id="score" className={cx("scroll-mt-20", className)}>
      <Card title={`Beveiligingsscore ${o.score.score}/100`}>
        <ul className="space-y-3">
          {parts.map((p) => (
            <li key={p.key}>
              <div className="flex justify-between text-sm">
                <span className="font-medium">{p.label}</span>
                <span className="tabular-nums text-slate-600 dark:text-slate-400">
                  {nf.format(p.points)} / {p.max}
                </span>
              </div>
              <div className="mt-1 h-2 overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800" role="img" aria-label={`${p.label}: ${p.points} van ${p.max}`}>
                <div
                  className={cx("h-full rounded-full", p.points / p.max >= 0.8 ? "bg-emerald-500" : p.points / p.max >= 0.5 ? "bg-amber-400" : "bg-red-500")}
                  style={{ width: `${(p.points / p.max) * 100}%` }}
                />
              </div>
            </li>
          ))}
        </ul>
        {lost.length > 0 ? (
          <details className="mt-4 text-sm">
            <summary className="cursor-pointer font-medium text-brand-700 dark:text-sky-300">Waarom niet 100?</summary>
            <ul className="mt-2 space-y-1">
              {lost
                .toSorted((a, b) => b.points - a.points)
                .map((d, i) => (
                  <li key={i} className="flex justify-between gap-3">
                    <Link href={d.href} className="min-w-0 hover:underline">
                      {d.text}
                    </Link>
                    <span className="shrink-0 tabular-nums text-slate-500">−{nf.format(d.points)}</span>
                  </li>
                ))}
            </ul>
          </details>
        ) : (
          <p className="mt-4 text-sm text-emerald-700 dark:text-emerald-300">Niets af te trekken.</p>
        )}
      </Card>
    </section>
  );
}

// --- activiteit -----------------------------------------------------------------

// Drie kleine grafieken onder elkaar met elk een eigen schaal: pakketten op
// een dichte poort zijn er vaak duizend keer meer dan alerts.
const series = [
  { key: "crowdsec", label: "CrowdSec-alerts", color: "bg-brand-500" },
  { key: "firewall", label: "Tegengehouden door de firewall", color: "bg-slate-400 dark:bg-slate-500" },
  { key: "logins", label: "Mislukte logins op ClusterForge", color: "bg-amber-500" },
] as const;

function ActivityCard({ o }: { o: SocOverview }) {
  const [hover, setHover] = useState<number | null>(null);
  const b = o.activity;
  const fmt = o.window === "24h" ? timeFmt : dayFmt;
  const h = hover == null ? null : b[hover];
  return (
    <Card title={`Activiteit (${windowLabel[o.window]})`}>
      <div className="relative" onMouseLeave={() => setHover(null)}>
        <div className="space-y-3">
          {series.map((s) => {
            const vals = b.map((x) => x[s.key]);
            const max = Math.max(1, ...vals);
            const total = vals.reduce((a, v) => a + v, 0);
            return (
              <div key={s.key}>
                <div className="mb-1 flex justify-between text-xs text-slate-500">
                  <span className="font-medium text-slate-700 dark:text-slate-300">{s.label}</span>
                  <span className="tabular-nums">
                    {nf.format(total)} · piek {nf.format(Math.max(...vals, 0))}
                  </span>
                </div>
                <div className="flex h-12 items-end gap-0.5" role="img" aria-label={`${s.label}: ${nf.format(total)} in totaal`}>
                  {vals.map((v, i) => (
                    <div key={i} className="flex h-full flex-1 items-end" onMouseEnter={() => setHover(i)}>
                      <div
                        className={cx("w-full rounded-t-sm", s.color, hover != null && hover !== i && "opacity-50")}
                        style={{ height: `${(v / max) * 100}%`, minHeight: v > 0 ? 2 : 0 }}
                      />
                    </div>
                  ))}
                </div>
              </div>
            );
          })}
        </div>
        <div className="mt-1 flex justify-between text-xs text-slate-500">
          <span>{b[0] && fmt.format(new Date(b[0].at))}</span>
          <span>nu</span>
        </div>
        {h && hover != null && (
          <div
            className="pointer-events-none absolute top-0 z-10 w-max -translate-x-1/2 rounded-md border border-slate-200 bg-white px-2.5 py-1.5 text-xs shadow-lg dark:border-slate-700 dark:bg-slate-900"
            style={{ left: `${Math.min(88, Math.max(12, ((hover + 0.5) / b.length) * 100))}%` }}
          >
            <div className="font-medium">{fmt.format(new Date(h.at))}</div>
            <div className="text-slate-600 dark:text-slate-400">
              {nf.format(h.crowdsec)} alerts · {nf.format(h.firewall)} pakketten · {nf.format(h.logins)} logins
            </div>
          </div>
        )}
      </div>
    </Card>
  );
}

// --- aanvallers -------------------------------------------------------------------

const kindFilter = [
  { key: "alle", label: "Alle" },
  { key: "extern", label: "Van buiten" },
  { key: "intern", label: "Intern" },
] as const;

const sourceLabel: Record<SocAttacker["sources"][number], string> = { crowdsec: "CrowdSec", firewall: "firewall", login: "login" };

function KindBadge({ a }: { a: SocAttacker }) {
  if (a.kind === "extern") return <Badge tone="red">van buiten</Badge>;
  if (a.kind === "node") return <Badge tone="blue">{a.node}</Badge>;
  return <Badge>intern</Badge>;
}

function AttackersCard({ o, onOpen }: { o: SocOverview; onOpen: (ip: string) => void }) {
  const [kind, setKind] = useState<(typeof kindFilter)[number]["key"]>("alle");
  const list = o.attackers.filter((a) => kind === "alle" || (kind === "extern" ? a.kind === "extern" : a.kind !== "extern"));
  return (
    <Card title="Aanvallers">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <p className="text-sm text-slate-600 dark:text-slate-400">
          Bronadressen samengevoegd over CrowdSec, de firewall en de login. Een eigen node die op een dichte poort klopt, is meestal een vergeten regel.
        </p>
        <div role="group" aria-label="Soort" className="inline-flex rounded-md border border-slate-300 p-0.5 dark:border-slate-700">
          {kindFilter.map((k) => (
            <button
              key={k.key}
              onClick={() => setKind(k.key)}
              aria-pressed={kind === k.key}
              className={cx(
                "rounded px-2.5 py-1 text-xs font-medium",
                kind === k.key ? "bg-slate-800 text-white dark:bg-slate-200 dark:text-slate-900" : "text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800",
              )}
            >
              {k.label}
            </button>
          ))}
        </div>
      </div>
      {list.length === 0 ? (
        <p className="text-sm text-slate-500">Geen aanvallers in deze periode.</p>
      ) : (
        <div className="-mx-5 overflow-x-auto">
          <table className={tableClass}>
            <thead>
              <tr className="border-b border-slate-200 dark:border-slate-800">
                <th className={cx(thClass, "pl-5")}>Adres</th>
                <th className={thClass}>Herkomst</th>
                <th className={thClass}>Wat</th>
                <th className={thClass}>Op</th>
                <th className={thClass}>Laatst</th>
                <th className={cx(thClass, "pr-5")}>Ban</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {list.map((a) => (
                <tr key={a.ip}>
                  <td className={cx(tdClass, "pl-5")}>
                    <button onClick={() => onOpen(a.ip)} className="font-mono text-xs text-brand-700 hover:underline dark:text-sky-300" title="Alles over dit adres">
                      {a.ip}
                    </button>
                    <div className="mt-1 flex flex-wrap gap-1">
                      <KindBadge a={a} />
                      {a.sources.map((s) => (
                        <Badge key={s}>{sourceLabel[s]}</Badge>
                      ))}
                    </div>
                  </td>
                  <td className={cx(tdClass, "max-w-48 text-xs")}>
                    {a.country || a.as ? (
                      <span className="break-words">
                        {flag(a.country)} {a.country} <span className="text-slate-500">{a.as}</span>
                      </span>
                    ) : (
                      <span className="text-slate-400">—</span>
                    )}
                  </td>
                  <td className={cx(tdClass, "text-xs")}>
                    <div className="tabular-nums">
                      {[
                        a.alerts > 0 && `${nf.format(a.alerts)} alert${a.alerts === 1 ? "" : "s"}`,
                        a.blocked > 0 && `${nf.format(a.blocked)} pakket${a.blocked === 1 ? "" : "ten"}`,
                        a.logins > 0 && `${nf.format(a.logins)} login${a.logins === 1 ? "" : "s"}`,
                      ]
                        .filter(Boolean)
                        .join(" · ")}
                    </div>
                    <div className="mt-0.5 max-w-64 truncate text-slate-500" title={[...a.scenarios, ...a.ports].join(", ")}>
                      {[...a.scenarios.map((s) => s.replace(/^crowdsecurity\//, "")), ...a.ports].join(", ")}
                    </div>
                  </td>
                  <td className={cx(tdClass, "text-xs")}>
                    <span title={a.nodes.join(", ")}>
                      {a.nodes.slice(0, 3).join(", ")}
                      {a.nodes.length > 3 && ` +${a.nodes.length - 3}`}
                    </span>
                  </td>
                  <td className={cx(tdClass, "whitespace-nowrap text-xs text-slate-500")}>{ago(a.last_seen)}</td>
                  <td className={cx(tdClass, "pr-5 text-xs")}>
                    {a.banned ? (
                      <Badge tone="green">tot {a.ban_until ? dayFmt.format(new Date(a.ban_until)) : "?"}</Badge>
                    ) : a.kind === "extern" && a.alerts > 0 ? (
                      <Badge tone="amber">niet geband</Badge>
                    ) : (
                      <span className="text-slate-400">—</span>
                    )}
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

// --- doelwitten ----------------------------------------------------------------------

function TopList({ items, empty, href, mono }: { items: SocCount[]; empty: string; href: (c: SocCount) => string; mono?: boolean }) {
  if (items.length === 0) return <p className="text-sm text-slate-500">{empty}</p>;
  const max = Math.max(...items.map((i) => i.count));
  return (
    <ul className="space-y-1.5 text-sm">
      {items.map((i) => (
        <li key={i.key} className="relative">
          <div className="absolute inset-y-0 left-0 rounded bg-brand-500/10" style={{ width: `${(i.count / max) * 100}%` }} />
          <Link href={href(i)} className="relative flex justify-between gap-2 px-2 py-0.5 hover:underline" title={i.detail || undefined}>
            <span className={cx("truncate", mono && "font-mono text-xs leading-5")}>{i.label}</span>
            <span className="tabular-nums">{nf.format(i.count)}</span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function TargetsCard({ o }: { o: SocOverview }) {
  const t = o.targets;
  return (
    <div className="grid gap-6 lg:grid-cols-3">
      <Card title="Meest aangevallen nodes">
        <TopList items={t.nodes} empty="Geen aanvallen." href={(c) => `/nodes/detail?id=${c.key}`} />
      </Card>
      <Card title="Dichte poorten waarop geklopt wordt">
        <TopList items={t.ports} empty="Niets tegengehouden." href={() => "/firewall?tab=verkeer"} mono />
      </Card>
      <Card title="Scenario's">
        <TopList
          items={t.scenarios.map((c) => ({ ...c, label: c.label.replace(/^crowdsecurity\//, ""), detail: c.detail || c.key }))}
          empty="Geen alerts."
          href={(c) => `/crowdsec?tab=alerts&scenario=${encodeURIComponent(c.key)}`}
          mono
        />
      </Card>
    </div>
  );
}

// --- dekking ---------------------------------------------------------------------------

type StateInfo = { label: string; tone: "green" | "amber" | "red" | "slate" | "blue"; icon: string };

const stateInfo: Record<string, StateInfo> = {
  on: { label: "aan", tone: "green", icon: "✓" },
  preview: { label: "voorbeeld", tone: "amber", icon: "◐" },
  off: { label: "bewust uit", tone: "slate", icon: "–" },
  missing: { label: "ontbreekt", tone: "amber", icon: "!" },
  down: { label: "draait niet", tone: "red", icon: "✕" },
  error: { label: "fout", tone: "red", icon: "✕" },
  unsupported: { label: "agent te oud", tone: "amber", icon: "!" },
  unknown: { label: "onbekend", tone: "slate", icon: "?" },
  unmanaged: { label: "niet beheerd", tone: "blue", icon: "◐" },
};

function State({ s }: { s: string }) {
  if (!s) return <span className="text-slate-400">—</span>;
  const i = stateInfo[s] ?? { label: s, tone: "slate", icon: "?" };
  return (
    <Badge tone={i.tone}>
      <span aria-hidden className="mr-1">
        {i.icon}
      </span>
      {i.label}
    </Badge>
  );
}

function CoverageCard({ rows }: { rows: SocCoverage[] }) {
  const [gapsOnly, setGapsOnly] = useState(false);
  const list = gapsOnly ? rows.filter((r) => r.gaps.length > 0) : rows;
  const withGaps = rows.filter((r) => r.gaps.length > 0).length;
  return (
    <section id="dekking" className="scroll-mt-20">
      <Card title="Bescherming per node">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
          <p className="text-sm text-slate-600 dark:text-slate-400">
            {withGaps === 0 ? "Elke actieve node is beschermd." : `${withGaps} van de ${rows.length} actieve nodes heeft een gat.`} Bewust uit telt niet als gat,
            maar wel in de score.
          </p>
          <label className="inline-flex items-center gap-2 text-sm">
            <input type="checkbox" checked={gapsOnly} onChange={(e) => setGapsOnly(e.target.checked)} className="size-4 rounded border-slate-300" />
            Alleen nodes met een gat
          </label>
        </div>
        {list.length === 0 ? (
          <p className="text-sm text-slate-500">{rows.length === 0 ? "Nog geen nodes met een agent." : "Geen gaten."}</p>
        ) : (
          <div className="-mx-5 overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr className="border-b border-slate-200 dark:border-slate-800">
                  <th className={cx(thClass, "pl-5")}>Node</th>
                  <th className={thClass}>Agent</th>
                  <th className={thClass}>Firewall</th>
                  <th className={thClass}>CrowdSec</th>
                  <th className={thClass}>Bouncer</th>
                  <th className={thClass}>Updates</th>
                  <th className={cx(thClass, "pr-5")}>Gaten</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {list.map((r) => (
                  <tr key={r.node_id}>
                    <td className={cx(tdClass, "pl-5")}>
                      <Link href={`/nodes/detail?id=${r.node_id}`} className="font-medium hover:underline">
                        {r.hostname}
                      </Link>
                      {r.cluster_name && <div className="text-xs text-slate-500">{r.cluster_name}</div>}
                    </td>
                    <td className={tdClass}>{r.online ? <State s="on" /> : <Badge tone="red">✕ offline</Badge>}</td>
                    <td className={tdClass}>
                      <Link href="/firewall?tab=nodes">
                        <State s={r.firewall} />
                      </Link>
                    </td>
                    <td className={tdClass}>
                      <Link href="/crowdsec?tab=nodes">
                        <State s={r.crowdsec} />
                      </Link>
                    </td>
                    <td className={tdClass}>
                      <State s={r.bouncer} />
                    </td>
                    <td className={cx(tdClass, "tabular-nums")}>
                      {r.security_updates > 0 ? (
                        <Link href="/updates">
                          <Badge tone="amber">{r.security_updates} beveiliging</Badge>
                        </Link>
                      ) : (
                        <span className="text-slate-400">—</span>
                      )}
                    </td>
                    <td className={cx(tdClass, "pr-5 text-xs text-slate-600 dark:text-slate-400")}>{r.gaps.join(", ") || "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </section>
  );
}

// --- aanvaller ---------------------------------------------------------------------------

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : "Er ging iets mis.";
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-slate-500">{label}</dt>
      <dd className="text-sm break-words">{children}</dd>
    </div>
  );
}

// AttackerDialog is alles wat ClusterForge over één adres weet, met de
// acties erbij: bannen of de ban opheffen op alle nodes met CrowdSec, en
// doorklikken naar de alerts, het firewallverkeer en het logboek.
function AttackerDialog({ ip, o, onClose }: { ip: string; o: SocOverview; onClose: () => void }) {
  const a = o.attackers.find((x) => x.ip === ip);
  const isAdmin = useIsAdmin();
  const decide = useCrowdSecDecide();
  const [duration, setDuration] = useState("168h");
  const scope = ip.includes("/") ? "range" : "ip";
  const enc = encodeURIComponent(ip);
  const external = !a || a.kind === "extern";

  const ban = async () => {
    if (!(await askConfirm(`${ip} bannen op alle nodes met CrowdSec?`, { confirmLabel: "Bannen", danger: true }))) return;
    decide.mutate(
      { scope, value: ip, duration, reason: "handmatig via SOC" },
      {
        onSuccess: (r) =>
          toast(
            `${ip} geband op ${r.nodes.join(", ") || "geen node"}${r.errors.length ? `; niet op: ${r.errors.join("; ")}` : ""}. Het SOC toont de ban na de volgende ophaalronde.`,
            r.errors.length ? "error" : "success",
          ),
        onError: (e) => toast(errText(e), "error"),
      },
    );
  };
  const unban = async () => {
    if (!(await askConfirm(`De ban van ${ip} opheffen op alle nodes?`, { confirmLabel: "Opheffen" }))) return;
    decide.mutate(
      { delete: true, scope, value: ip },
      {
        onSuccess: (r) => toast(`Ban van ${ip} opgeheven op ${r.nodes.join(", ") || "geen node"}.`),
        onError: (e) => toast(errText(e), "error"),
      },
    );
  };

  return (
    <Modal title={`Adres ${ip}`} onClose={onClose} wide>
      <div className="space-y-4">
        {!a ? (
          <Alert kind="info">Dit adres staat niet bij de aanvallers van de laatste {windowLabel[o.window]}.</Alert>
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-2">
              <KindBadge a={a} />
              {a.sources.map((s) => (
                <Badge key={s}>{sourceLabel[s]}</Badge>
              ))}
              {(a.country || a.as) && (
                <span className="text-sm">
                  {flag(a.country)} {a.country} <span className="text-slate-500">{a.as}</span>
                </span>
              )}
            </div>
            <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <Fact label="CrowdSec-alerts">{nf.format(a.alerts)}</Fact>
              <Fact label="Tegengehouden pakketten">{nf.format(a.blocked)}</Fact>
              <Fact label="Mislukte logins">{nf.format(a.logins)}</Fact>
              <Fact label="Ban">
                {a.banned ? `tot ${a.ban_until ? dayFmt.format(new Date(a.ban_until)) : "?"}` : external && a.alerts > 0 ? <span className="text-amber-700 dark:text-amber-400">niet geband</span> : "—"}
              </Fact>
              <Fact label="Eerst gezien">{dayFmt.format(new Date(a.first_seen))}</Fact>
              <Fact label="Laatst gezien">{ago(a.last_seen)}</Fact>
              <Fact label="Op">{a.nodes.join(", ") || "—"}</Fact>
            </dl>
            {(a.scenarios.length > 0 || a.ports.length > 0) && (
              <div className="grid gap-3 sm:grid-cols-2">
                {a.scenarios.length > 0 && (
                  <Fact label="Scenario's">
                    <ul className="font-mono text-xs">
                      {a.scenarios.map((s) => (
                        <li key={s}>{s}</li>
                      ))}
                    </ul>
                  </Fact>
                )}
                {a.ports.length > 0 && (
                  <Fact label="Dichte poorten">
                    <span className="font-mono text-xs">{a.ports.join(", ")}</span>
                  </Fact>
                )}
              </div>
            )}
            {a.kind === "node" && (
              <Alert kind="info">
                Dit is het adres van {a.node}. Een eigen node die op een dichte poort klopt, is meestal een vergeten firewallregel, geen aanval. Open de poort voor
                deze node bij Firewall → Verkeer naar dichte poorten.
              </Alert>
            )}
          </>
        )}
        <div className="flex flex-wrap gap-2 border-t border-slate-200 pt-4 dark:border-slate-800">
          <Link href={`/crowdsec?tab=alerts&value=${enc}`} className="text-sm font-medium text-brand-700 hover:underline dark:text-sky-300">
            CrowdSec-alerts
          </Link>
          <span className="text-slate-300 dark:text-slate-700">·</span>
          <Link href="/firewall?tab=verkeer" className="text-sm font-medium text-brand-700 hover:underline dark:text-sky-300">
            Firewallverkeer
          </Link>
          <span className="text-slate-300 dark:text-slate-700">·</span>
          <Link href={`/logboek?q=${enc}`} className="text-sm font-medium text-brand-700 hover:underline dark:text-sky-300">
            Logboek
          </Link>
          <span className="text-slate-300 dark:text-slate-700">·</span>
          <button className="text-sm font-medium text-brand-700 hover:underline dark:text-sky-300" onClick={() => void copyText(ip, `${ip} gekopieerd`)}>
            Kopiëren
          </button>
        </div>
        {isAdmin && external && (
          <div className="flex flex-wrap items-center justify-end gap-2">
            {a?.banned ? (
              <Button variant="secondary" disabled={decide.isPending} onClick={() => void unban()}>
                Ban opheffen
              </Button>
            ) : (
              <>
                <Select aria-label="Duur van de ban" value={duration} onChange={(e) => setDuration(e.target.value)} className="w-auto">
                  <option value="4h">4 uur</option>
                  <option value="24h">24 uur</option>
                  <option value="168h">7 dagen</option>
                  <option value="720h">30 dagen</option>
                  <option value="8760h">1 jaar</option>
                </Select>
                <Button variant="danger" disabled={decide.isPending} onClick={() => void ban()}>
                  Bannen op alle nodes
                </Button>
              </>
            )}
          </div>
        )}
      </div>
    </Modal>
  );
}

// --- beveiligingslogboek ---------------------------------------------------------------------

// SecurityLog toont de laatste beveiligingsevents: logins, bans, firewall- en
// CrowdSec-wijzigingen. Het logboek is alleen voor beheerders.
function SecurityLog() {
  const isAdmin = useIsAdmin();
  const events = useAuditRecent({ category: "security" }, 15, isAdmin);
  if (!isAdmin) return null;
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>Beveiligingslogboek</span>
          <Link href="/logboek?category=security" className="text-sm font-medium text-brand-700 hover:underline dark:text-sky-300">
            Alles in het logboek
          </Link>
        </span>
      }
    >
      {events.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      {events.isError && <Alert>Het logboek laden mislukte.</Alert>}
      {events.data && events.data.length === 0 && <p className="text-sm text-slate-500">Nog geen beveiligingsevents.</p>}
      {events.data && events.data.length > 0 && <AuditList items={events.data} />}
    </Card>
  );
}
