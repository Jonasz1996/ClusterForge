"use client";

import { useMemo, useState } from "react";
import { TimeChart } from "@/components/charts/TimeChart";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { FeedLine, Stat, WindowPicker, n, when } from "@/components/publish/feeds";
import { Alert, Badge, Card, PageHeader } from "@/components/ui";
import { actionLabel, usePublishLogins, type LoginsView, type TrafficWindow } from "@/lib/publish";

export default function PublishLoginsPage() {
  const [win, setWin] = useState<TrafficWindow>("24h");
  const q = usePublishLogins(win);
  const v = q.data;
  return (
    <div className="space-y-6">
      <PageHeader
        title="Logins"
        description="Aanmeldingen en mislukte aanmeldingen in Authentik, per gebruiker, per IP en per app. Een IP met veel mislukte logins staat naast de bans van CrowdSec: zonder ban komt het ook bij Aandacht nodig."
        actions={<WindowPicker value={win} onChange={setWin} />}
      />
      {q.isError && <Alert>{(q.error as Error).message}</Alert>}
      {v && <Logins v={v} />}
    </div>
  );
}

function Logins({ v }: { v: LoginsView }) {
  const t = v.totals;
  const chart = useMemo(
    () => ({
      ts: v.series.map((p) => new Date(p.t).getTime() / 1000),
      series: [
        { label: "Aangemeld", values: v.series.map((p) => p.logins) },
        { label: "Mislukt", values: v.series.map((p) => p.failed) },
      ],
    }),
    [v.series],
  );
  return (
    <>
      <FeedLine feed={v.feed} name="Events van Authentik" />
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
        <Stat label="Aangemeld" value={n(t.logins)} />
        <Stat label="Mislukt" value={n(t.failed)} tone={t.failed > 20 ? "amber" : undefined} />
        <Stat label="Gebruikers" value={n(t.users)} />
        <Stat label="IP's met mislukte logins" value={n(t.failed_ips)} />
        <Stat label="Daarvan zonder ban" value={n(t.failed_ips_not_banned)} tone={t.failed_ips_not_banned > 0 ? "amber" : "green"} />
        <Stat label="Apps geopend" value={n(t.app_authorizations)} />
      </div>
      <Card title="Aanmeldingen">
        {chart.ts.length === 0 ? (
          <p className="text-sm text-slate-500">Geen aanmeldingen in dit venster.</p>
        ) : (
          <TimeChart timestamps={chart.ts} series={chart.series} unit="count" />
        )}
      </Card>
      <div className="grid gap-6">
        <Card title={`Mislukte logins per IP · ${v.failed_ips.length}`}>
          {v.failed_ips.length === 0 ? (
            <p className="text-sm text-slate-500">Geen mislukte logins.</p>
          ) : (
            <div className="overflow-x-auto">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>IP</th>
                    <th className={thClass}>Mislukt</th>
                    <th className={thClass}>Gebruikersnamen</th>
                    <th className={thClass}>CrowdSec</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {v.failed_ips.map((f) => (
                    <tr key={f.ip}>
                      <td className={`${tdClass} font-mono text-xs`}>
                        {f.ip}
                        {f.country && <span className="ml-1 font-sans text-slate-500">{f.country}</span>}
                        {f.requests > 0 && <div className="font-sans text-slate-500">{n(f.requests)} aanvragen bij NPM</div>}
                      </td>
                      <td className={tdClass}>{n(f.failed)}</td>
                      <td className={`${tdClass} text-xs`}>{f.usernames.join(", ")}</td>
                      <td className={`${tdClass} text-xs`}>
                        {f.ban ? (
                          <>
                            <Badge tone="green">geblokkeerd</Badge>
                            <div className="mt-1 text-slate-500">{f.ban.scenario}</div>
                          </>
                        ) : (
                          <Badge tone={f.failed >= 10 ? "red" : "amber"}>geen ban</Badge>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
        <Card title={`Per gebruiker · ${v.users.length}`}>
          {v.users.length === 0 ? (
            <p className="text-sm text-slate-500">Geen aanmeldingen.</p>
          ) : (
            <div className="overflow-x-auto">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Gebruiker</th>
                    <th className={thClass}>Aangemeld</th>
                    <th className={thClass}>Mislukt</th>
                    <th className={thClass}>Laatst</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {v.users.map((u) => (
                    <tr key={u.username}>
                      <td className={`${tdClass} font-medium`}>{u.username}</td>
                      <td className={tdClass}>{n(u.logins)}</td>
                      <td className={tdClass}>{u.failed > 0 ? <span className="text-amber-700 dark:text-amber-300">{n(u.failed)}</span> : 0}</td>
                      <td className={`${tdClass} text-xs`}>
                        {u.last && when.format(new Date(u.last))}
                        {u.last_ip && <div className="font-mono text-slate-500">{u.last_ip}</div>}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      </div>
      {v.apps.length > 0 && (
        <Card title="Apps geopend via Authentik">
          <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
            {v.apps.map((a) => (
              <li key={a.app} className="flex justify-between py-1.5">
                <span>{a.app}</span>
                <span className="text-slate-500">
                  {n(a.count)} keer · {n(a.users)} gebruikers
                </span>
              </li>
            ))}
          </ul>
        </Card>
      )}
      <Card title="Laatste gebeurtenissen">
        {v.recent.length === 0 ? (
          <p className="text-sm text-slate-500">Niets in dit venster.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Wanneer</th>
                  <th className={thClass}>Wat</th>
                  <th className={thClass}>Gebruiker</th>
                  <th className={thClass}>IP</th>
                  <th className={thClass}>Detail</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {v.recent.map((e, i) => (
                  <tr key={i}>
                    <td className={`${tdClass} text-xs whitespace-nowrap`}>{when.format(new Date(e.created))}</td>
                    <td className={tdClass}>
                      <Badge tone={e.action === "login_failed" ? "amber" : e.action === "login" ? "green" : "slate"}>
                        {actionLabel[e.action] ?? e.action}
                      </Badge>
                    </td>
                    <td className={tdClass}>{e.username}</td>
                    <td className={`${tdClass} font-mono text-xs`}>
                      {e.ip}
                      {e.banned && <span className="ml-1 font-sans text-green-700 dark:text-green-400">geblokkeerd</span>}
                    </td>
                    <td className={`${tdClass} text-xs text-slate-500`}>
                      {[e.app, e.reason, [e.city, e.country].filter(Boolean).join(", ")].filter(Boolean).join(" · ")}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </>
  );
}
