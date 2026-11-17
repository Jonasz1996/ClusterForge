"use client";

import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { AKBadge, Empty, StatusBar } from "@/components/publish/bits";
import { Alert, Badge, Card, PageHeader } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { usePublishOverview } from "@/lib/publish";

export default function PublishAuthentikPage() {
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const q = usePublishOverview();
  const ov = q.data;
  const open = (ov?.hosts ?? []).filter((h) => h.authentik === "open" && h.enabled);
  return (
    <div className="space-y-6">
      <PageHeader
        title="Authentik"
        description="De applicaties met een proxy provider (forward auth) in Authentik, met de proxy host in NPM die erbij hoort. Een host is pas echt beschermd als de applicatie, de provider in de embedded outpost en de forward-auth-config in NPM alle drie kloppen; ClusterForge controleert dat na elke wijziging met een echte aanvraag."
      />
      {q.isError && <Alert>{(q.error as Error).message}</Alert>}
      {ov && (
        <>
          <StatusBar ov={ov} admin={admin} />
          {!ov.authentik.linked && (
            <Alert kind="info">
              Authentik is nog niet gekoppeld. Geef de Authentik-node en een API-token in Instellingen → Domeinen en publicatie.
            </Alert>
          )}
          <Card title={`Applicaties · ${ov.apps.length}`}>
            {ov.apps.length === 0 ? (
              <Empty>Geen applicaties met een proxy provider.</Empty>
            ) : (
              <div className="overflow-x-auto">
                <table className={tableClass}>
                  <thead>
                    <tr>
                      <th className={thClass}>Applicatie</th>
                      <th className={thClass}>Provider</th>
                      <th className={thClass}>Adres</th>
                      <th className={thClass}>Outpost</th>
                      <th className={thClass}>NPM</th>
                      <th className={thClass}>Stand</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                    {ov.apps.map((a) => (
                      <tr key={a.slug}>
                        <td className={tdClass}>
                          <div className="font-medium">{a.name}</div>
                          <div className="text-xs text-slate-500">{a.slug}</div>
                        </td>
                        <td className={tdClass}>
                          {a.provider}
                          {a.mode && <div className="text-xs text-slate-500">{a.mode}</div>}
                        </td>
                        <td className={tdClass}>{a.external_host}</td>
                        <td className={tdClass}>{a.in_outpost ? <Badge tone="green">ja</Badge> : <Badge tone="amber">nee</Badge>}</td>
                        <td className={tdClass}>
                          {a.host_id ? (
                            a.snippet ? (
                              <Badge tone="green">forward auth</Badge>
                            ) : (
                              <Badge tone="amber">zonder forward auth</Badge>
                            )
                          ) : (
                            <span className="text-slate-400">geen host</span>
                          )}
                        </td>
                        <td className={tdClass}>
                          <AKBadge state={a.state} />
                          {a.detail && <div className="text-xs text-slate-500">{a.detail}</div>}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
          <Card title={`Open proxy hosts · ${open.length}`}>
            {open.length === 0 ? (
              <Empty>Elke actieve proxy host zit achter Authentik of heeft een eigen login.</Empty>
            ) : (
              <>
                <p className="mb-2 text-sm text-slate-600 dark:text-slate-300">
                  Deze hosts zijn zonder Authentik bereikbaar. Zet ze erachter op de pagina Proxy hosts (eerst overnemen als ze nog niet beheerd
                  zijn).
                </p>
                <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
                  {open.map((h) => (
                    <li key={h.id} className="py-1.5">
                      {h.domains[0]}
                      {h.service_name && <span className="ml-2 text-xs text-slate-500">{h.service_name}</span>}
                    </li>
                  ))}
                </ul>
              </>
            )}
          </Card>
        </>
      )}
    </div>
  );
}
