"use client";

import { useMemo, useState } from "react";
import { TimeChart } from "@/components/charts/TimeChart";
import { formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { FeedLine, Stat, WindowPicker, n, when } from "@/components/publish/feeds";
import { Alert, Badge, Card, Input, PageHeader } from "@/components/ui";
import { usePublishTraffic, type TrafficView, type TrafficWindow } from "@/lib/publish";

export default function PublishTrafficPage() {
  const [win, setWin] = useState<TrafficWindow>("24h");
  const q = usePublishTraffic(win);
  const v = q.data;
  return (
    <div className="space-y-6">
      <PageHeader
        title="Verkeer"
        description="Wat er via Nginx Proxy Manager binnenkomt, uit de toegangslogs van NPM: aanvragen en fouten per host, de drukste bezoekers en de open verbindingen. Elk IP staat naast de bans van CrowdSec, zodat je ziet wie geblokkeerd is maar toch nog binnenkomt, en wie verdacht is zonder ban."
        actions={<WindowPicker value={win} onChange={setWin} />}
      />
      {q.isError && <Alert>{(q.error as Error).message}</Alert>}
      {v && <Traffic v={v} />}
    </div>
  );
}

function Traffic({ v }: { v: TrafficView }) {
  const t = v.totals;
  const errRate = t.requests > 0 ? (t.s5xx * 100) / t.requests : 0;
  const npmFeed = v.feeds.find((f) => f.source === "npm");
  const chart = useMemo(
    () => ({
      ts: v.series.map((p) => new Date(p.t).getTime() / 1000),
      series: [
        { label: "Aanvragen", values: v.series.map((p) => p.requests) },
        { label: "4xx", values: v.series.map((p) => p.s4xx) },
        { label: "5xx", values: v.series.map((p) => p.s5xx) },
      ],
    }),
    [v.series],
  );
  const conn = useMemo(
    () => ({
      ts: v.connections.map((p) => new Date(p.t).getTime() / 1000),
      series: [{ label: "Open verbindingen", values: v.connections.map((p) => p.count) }],
    }),
    [v.connections],
  );
  return (
    <>
      <FeedLine feed={npmFeed} name="Logs van NPM" />
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
        <Stat label="Aanvragen" value={n(t.requests)} />
        <Stat label="Bezoekers (IP's)" value={n(t.visitors)} />
        <Stat
          label="Serverfouten (5xx)"
          value={n(t.s5xx)}
          tone={errRate >= 5 ? "red" : errRate >= 1 ? "amber" : undefined}
          hint={`${errRate.toLocaleString("nl-BE", { maximumFractionDigits: 1 })} %`}
        />
        <Stat label="Geweigerd (401/403)" value={n(t.denied)} />
        <Stat label="Verstuurd" value={formatBytes(t.bytes)} />
        <Stat
          label="Open verbindingen"
          value={t.connections_at ? n(t.connections) : "–"}
          hint={t.connections_at ? `gemeten ${when.format(new Date(t.connections_at))}` : undefined}
        />
      </div>

      <Card title="NPM naast CrowdSec">
        <div className="grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
          <Stat label="Actieve bans in CrowdSec" value={n(v.compare.active_bans)} />
          <Stat label="Geblokkeerde IP's gezien bij NPM" value={n(v.compare.banned_seen)} />
          <Stat
            label="Geblokkeerd maar komt nog binnen"
            value={n(v.compare.banned_passing)}
            tone={v.compare.banned_passing > 0 ? "red" : "green"}
            hint={v.compare.banned_passing > 0 ? "Staat er een CrowdSec-bouncer op de NPM-node?" : undefined}
          />
          <Stat
            label="Verdacht zonder ban"
            value={n(v.compare.suspicious)}
            tone={v.compare.suspicious > 0 ? "amber" : "green"}
            hint="Veel 401/403, veel 4xx of mislukte logins"
          />
        </div>
      </Card>

      <Card title="Aanvragen">
        {chart.ts.length === 0 ? (
          <p className="text-sm text-slate-500">Nog geen verkeer in dit venster.</p>
        ) : (
          <TimeChart timestamps={chart.ts} series={chart.series} unit="count" />
        )}
      </Card>
      {conn.ts.length > 0 && (
        <Card title="Open verbindingen van NPM">
          <TimeChart timestamps={conn.ts} series={conn.series} unit="count" height={140} />
        </Card>
      )}

      <Card title={`Per proxy host · ${v.hosts.length}`}>
        {v.hosts.length === 0 ? (
          <p className="text-sm text-slate-500">Nog geen verkeer.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Host</th>
                  <th className={thClass}>Aanvragen</th>
                  <th className={thClass}>Bezoekers</th>
                  <th className={thClass}>4xx</th>
                  <th className={thClass}>5xx</th>
                  <th className={thClass}>Geweigerd</th>
                  <th className={thClass}>Verstuurd</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {v.hosts.map((h) => {
                  const rate = h.requests > 0 ? (h.s5xx * 100) / h.requests : 0;
                  return (
                    <tr key={h.host_id}>
                      <td className={`${tdClass} font-medium`}>{h.domain}</td>
                      <td className={tdClass}>{n(h.requests)}</td>
                      <td className={tdClass}>{n(h.visitors)}</td>
                      <td className={tdClass}>{n(h.s4xx)}</td>
                      <td className={tdClass}>
                        {n(h.s5xx)}
                        {rate >= 5 && (
                          <span className="ml-2">
                            <Badge tone="red">{rate.toLocaleString("nl-BE", { maximumFractionDigits: 0 })} %</Badge>
                          </span>
                        )}
                      </td>
                      <td className={tdClass}>{n(h.denied)}</td>
                      <td className={tdClass}>{formatBytes(h.bytes)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <IPs v={v} />
    </>
  );
}

function IPs({ v }: { v: TrafficView }) {
  const [filter, setFilter] = useState("");
  const [only, setOnly] = useState<"attention" | "all">("attention");
  const rows = v.ips
    .filter((p) => only === "all" || p.suspicious || (p.ban && p.after_ban > 0))
    .filter((p) => !filter || `${p.ip} ${p.hosts.join(" ")} ${p.ban?.scenario ?? ""}`.toLowerCase().includes(filter.toLowerCase()));
  return (
    <Card title="Bezoekers en CrowdSec">
      <div className="mb-3 flex flex-wrap items-center gap-3 text-sm">
        <Input placeholder="Zoeken op IP of host" value={filter} onChange={(e) => setFilter(e.target.value)} className="max-w-xs" />
        <label className="flex items-center gap-1">
          <input type="radio" name="ips" checked={only === "attention"} onChange={() => setOnly("attention")} />
          Vraagt aandacht
        </label>
        <label className="flex items-center gap-1">
          <input type="radio" name="ips" checked={only === "all"} onChange={() => setOnly("all")} />
          Drukste {v.ips.length}
        </label>
      </div>
      {rows.length === 0 ? (
        <p className="text-sm text-slate-500">
          {only === "attention"
            ? "Niets dat aandacht vraagt: geen geblokkeerd IP dat nog binnenkomt en niets verdachts zonder ban."
            : "Niets gevonden."}
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>IP</th>
                <th className={thClass}>Aanvragen</th>
                <th className={thClass}>Geweigerd</th>
                <th className={thClass}>4xx / 5xx</th>
                <th className={thClass}>Hosts</th>
                <th className={thClass}>CrowdSec</th>
                <th className={thClass}>Laatst</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {rows.map((p) => (
                <tr key={p.ip}>
                  <td className={`${tdClass} font-mono text-xs`}>
                    {p.ip}
                    {p.failed_logins > 0 && <div className="font-sans text-amber-700 dark:text-amber-300">{p.failed_logins} mislukte logins</div>}
                  </td>
                  <td className={tdClass}>{n(p.requests)}</td>
                  <td className={tdClass}>{n(p.denied)}</td>
                  <td className={tdClass}>
                    {n(p.s4xx)} / {n(p.s5xx)}
                  </td>
                  <td className={`${tdClass} text-xs`}>{p.hosts.join(", ")}</td>
                  <td className={`${tdClass} text-xs`}>
                    {p.ban ? (
                      <>
                        <Badge tone={p.after_ban > 0 ? "red" : "green"}>
                          {p.after_ban > 0 ? `geblokkeerd, nog ${n(p.after_ban)} binnen` : "geblokkeerd"}
                        </Badge>
                        <div className="mt-1 text-slate-500">
                          {p.ban.scenario} · tot {when.format(new Date(p.ban.until))}
                          {p.ban.country ? ` · ${p.ban.country}` : ""}
                        </div>
                      </>
                    ) : p.suspicious ? (
                      <>
                        <Badge tone="amber">verdacht, geen ban</Badge>
                        <div className="mt-1 text-slate-500">{p.why}</div>
                      </>
                    ) : (
                      <span className="text-slate-400">geen ban</span>
                    )}
                  </td>
                  <td className={`${tdClass} text-xs whitespace-nowrap`}>{when.format(new Date(p.last))}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}
