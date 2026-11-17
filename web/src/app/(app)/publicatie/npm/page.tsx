"use client";

import { useMemo, useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ActionModal } from "@/components/publish/ActionModal";
import { AKBadge, Empty, StatusBar } from "@/components/publish/bits";
import { toast } from "@/components/Toast";
import { Alert, Badge, Button, Card, Input, PageHeader } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { useAcceptDrift, useAdoptHosts, usePublishOverview, useReleaseHost, type PublishAction, type PublishHost } from "@/lib/publish";

type Open = { title: string; action: PublishAction };

export default function PublishNPMPage() {
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const q = usePublishOverview();
  const ov = q.data;
  const adopt = useAdoptHosts();
  const accept = useAcceptDrift();
  const release = useReleaseHost();
  const [filter, setFilter] = useState("");
  const [only, setOnly] = useState<"" | "problems" | "unmanaged">("");
  const [picked, setPicked] = useState<number[]>([]);
  const [open, setOpen] = useState<Open | null>(null);

  const hosts = useMemo(() => {
    const f = filter.trim().toLowerCase();
    return (ov?.hosts ?? [])
      .filter((h) => (only === "problems" ? h.problems.length > 0 || (h.drift ?? []).length > 0 : only === "unmanaged" ? !h.managed : true))
      .filter((h) => !f || `${h.domains.join(" ")} ${h.forward_host} ${h.service_name ?? ""}`.toLowerCase().includes(f));
  }, [ov, filter, only]);
  const unmanaged = hosts.filter((h) => !h.managed).map((h) => h.id);

  const doAdopt = async (ids: number[]) => {
    try {
      const r = await adopt.mutateAsync(ids);
      toast(`${r.adopted} proxy hosts overgenomen; er is niets aan veranderd`);
      setPicked([]);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };

  return (
    <div className="space-y-6">
      <PageHeader
        title="Proxy hosts"
        description="Alle proxy hosts in Nginx Proxy Manager. Overnemen verandert niets in NPM: ClusterForge onthoudt alleen hoe de host nu is, en meldt het als iemand hem daarna buiten ClusterForge wijzigt. Elke wijziging gaat via de API van NPM, met een kopie vooraf en terugdraaien als een controle faalt."
      />
      {q.isError && <Alert>{(q.error as Error).message}</Alert>}
      {ov && (
        <>
          <StatusBar ov={ov} admin={admin} />
          <Card title={`Proxy hosts · ${hosts.length}`}>
            <div className="mb-3 flex flex-wrap items-center gap-3 text-sm">
              <Input placeholder="Zoeken" value={filter} onChange={(e) => setFilter(e.target.value)} className="max-w-xs" />
              {(
                [
                  ["", "Alle"],
                  ["problems", "Met problemen of drift"],
                  ["unmanaged", "Niet beheerd"],
                ] as const
              ).map(([v, l]) => (
                <label key={v} className="flex items-center gap-1">
                  <input type="radio" name="only" checked={only === v} onChange={() => setOnly(v)} />
                  {l}
                </label>
              ))}
              {admin && unmanaged.length > 0 && (
                <span className="ml-auto flex gap-2">
                  {picked.length > 0 && (
                    <Button variant="secondary" disabled={adopt.isPending} onClick={() => void doAdopt(picked)}>
                      {picked.length} overnemen
                    </Button>
                  )}
                  <Button
                    variant="secondary"
                    disabled={adopt.isPending}
                    onClick={async () => {
                      if (await askConfirm(`${unmanaged.length} proxy hosts overnemen? Er verandert niets in NPM.`)) void doAdopt(unmanaged);
                    }}
                  >
                    Alle niet-beheerde overnemen
                  </Button>
                </span>
              )}
            </div>
            {hosts.length === 0 ? (
              <Empty>{ov.npm.linked ? "Geen proxy hosts die hierbij passen." : "Koppel eerst NPM."}</Empty>
            ) : (
              <div className="overflow-x-auto">
                <table className={tableClass}>
                  <thead>
                    <tr>
                      {admin && <th className={thClass} />}
                      <th className={thClass}>Naam</th>
                      <th className={thClass}>Doel</th>
                      <th className={thClass}>Authentik</th>
                      <th className={thClass}>Stand</th>
                      {admin && <th className={thClass} />}
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                    {hosts.map((h) => (
                      <HostRow
                        key={h.id}
                        h={h}
                        admin={admin}
                        akLinked={ov.authentik.linked}
                        picked={picked.includes(h.id)}
                        onPick={(on) => setPicked((p) => (on ? [...p, h.id] : p.filter((x) => x !== h.id)))}
                        onOpen={setOpen}
                        onAccept={async () => {
                          if (await askConfirm(`De wijziging in NPM aan ${h.domains[0]} aanvaarden als de nieuwe gewenste stand?`)) {
                            accept.mutate(h.id, {
                              onError: (e) => toast((e as Error).message, "error"),
                            });
                          }
                        }}
                        onRelease={async () => {
                          if (await askConfirm(`${h.domains[0]} niet langer beheren? De host blijft in NPM staan zoals hij is.`)) {
                            release.mutate(h.id, {
                              onError: (e) => toast((e as Error).message, "error"),
                            });
                          }
                        }}
                      />
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        </>
      )}
      {open && <ActionModal title={open.title} action={open.action} onClose={() => setOpen(null)} />}
    </div>
  );
}

function HostRow({
  h,
  admin,
  akLinked,
  picked,
  onPick,
  onOpen,
  onAccept,
  onRelease,
}: {
  h: PublishHost;
  admin: boolean;
  akLinked: boolean;
  picked: boolean;
  onPick: (on: boolean) => void;
  onOpen: (o: Open) => void;
  onAccept: () => void;
  onRelease: () => void;
}) {
  const name = h.domains[0] ?? `host ${h.id}`;
  const drift = h.drift ?? [];
  const btn = "underline";
  return (
    <tr>
      {admin && (
        <td className={tdClass}>
          {!h.managed && <input type="checkbox" aria-label={`${name} kiezen`} checked={picked} onChange={(e) => onPick(e.target.checked)} />}
        </td>
      )}
      <td className={tdClass}>
        {h.domains.map((d) => (
          <div key={d}>
            <a href={`https://${d}`} target="_blank" rel="noreferrer" className="underline">
              {d}
            </a>
          </div>
        ))}
        {h.service_name && (
          <div className="text-xs text-slate-500">
            {h.service_name}
            {h.cluster_name ? ` · ${h.cluster_name}` : ""}
          </div>
        )}
      </td>
      <td className={`${tdClass} whitespace-nowrap`}>
        {h.forward_scheme}://{h.forward_host}:{h.forward_port}
        {h.suggest_vip && <div className="text-xs text-amber-700 dark:text-amber-300">wijst naar één node; VIP {h.suggest_vip}</div>}
        <div className="text-xs text-slate-500">
          {h.certificate_id
            ? `SSL ${h.certificate_name ?? ""}${h.certificate_expires ? ` tot ${new Date(h.certificate_expires).toLocaleDateString("nl-BE")}` : ""}`
            : "geen SSL"}
        </div>
      </td>
      <td className={tdClass}>
        <AKBadge state={h.authentik} />
        {h.authentik_detail && <div className="text-xs text-slate-500">{h.authentik_detail}</div>}
      </td>
      <td className={tdClass}>
        <div className="flex flex-wrap gap-1">
          {h.managed ? <Badge tone="blue">beheerd</Badge> : <Badge tone="slate">niet beheerd</Badge>}
          {!h.enabled && <Badge tone="slate">uit</Badge>}
          {drift.length > 0 && <Badge tone="amber">gewijzigd buiten ClusterForge</Badge>}
        </div>
        {drift.length > 0 && <div className="mt-1 text-xs text-amber-700 dark:text-amber-300">{drift.join(", ")}</div>}
        {h.problems.map((p) => (
          <div
            key={p.code}
            className={`mt-1 text-xs ${p.level === "crit" ? "text-red-700 dark:text-red-400" : "text-amber-700 dark:text-amber-300"}`}
          >
            {p.text}
          </div>
        ))}
      </td>
      {admin && (
        <td className={`${tdClass} text-xs whitespace-nowrap`}>
          {h.managed && (
            <div className="flex flex-col items-start gap-1">
              {drift.length > 0 && (
                <>
                  <button
                    type="button"
                    className={btn}
                    onClick={() =>
                      onOpen({
                        title: `${name} herstellen`,
                        action: { action: "restore", host_id: h.id },
                      })
                    }
                  >
                    herstellen
                  </button>
                  <button type="button" className={btn} onClick={onAccept}>
                    wijziging aanvaarden
                  </button>
                </>
              )}
              {h.suggest_vip && (
                <button
                  type="button"
                  className={btn}
                  onClick={() =>
                    onOpen({
                      title: `${name} naar het VIP`,
                      action: { action: "retarget", host_id: h.id },
                    })
                  }
                >
                  naar VIP
                </button>
              )}
              {akLinked &&
                (h.authentik === "beschermd" || h.authentik === "half" ? (
                  h.authentik_by_cf || h.authentik === "half" ? (
                    <button
                      type="button"
                      className={btn}
                      onClick={() =>
                        onOpen({
                          title: `Authentik weghalen bij ${name}`,
                          action: { action: "unprotect", host_id: h.id },
                        })
                      }
                    >
                      Authentik uit
                    </button>
                  ) : null
                ) : (
                  <button
                    type="button"
                    className={btn}
                    onClick={() =>
                      onOpen({
                        title: `${name} achter Authentik`,
                        action: { action: "protect", host_id: h.id },
                      })
                    }
                  >
                    achter Authentik
                  </button>
                ))}
              <button type="button" className={btn} onClick={onRelease}>
                loslaten
              </button>
              <button
                type="button"
                className="text-red-700 underline dark:text-red-400"
                onClick={() =>
                  onOpen({
                    title: `${name} verwijderen`,
                    action: { action: "delete", host_id: h.id },
                  })
                }
              >
                verwijderen
              </button>
            </div>
          )}
        </td>
      )}
    </tr>
  );
}
