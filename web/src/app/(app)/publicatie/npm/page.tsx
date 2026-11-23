"use client";

import { useMemo, useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ActionModal } from "@/components/publish/ActionModal";
import { HostEditor } from "@/components/publish/HostEditor";
import { ThemeEditor } from "@/components/publish/ThemeEditor";
import { AKBadge, Empty, StatusBar } from "@/components/publish/bits";
import { toast } from "@/components/Toast";
import { Alert, Badge, Button, Card, Input, PageHeader } from "@/components/ui";
import { useMe } from "@/lib/auth";
import {
  useAcceptDrift,
  useAdoptHosts,
  usePublishChanges,
  usePublishOverview,
  useReleaseHost,
  type PublishAction,
  type PublishHost,
} from "@/lib/publish";

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
  const [edit, setEdit] = useState<PublishHost | "new" | null>(null);
  const [theme, setTheme] = useState(false);

  const hosts = useMemo(() => {
    const f = filter.trim().toLowerCase();
    return (ov?.hosts ?? [])
      .filter((h) => (only === "problems" ? h.problems.length > 0 || (h.drift ?? []).length > 0 : only === "unmanaged" ? !h.managed : true))
      .filter((h) => !f || `${h.domains.join(" ")} ${h.forward_host} ${h.service_name ?? ""}`.toLowerCase().includes(f));
  }, [ov, filter, only]);
  const unmanaged = hosts.filter((h) => !h.managed).map((h) => h.id);
  // Samen toepassen: alleen zekere voorstellen en oude adressen in Advanced; een gok gaat per host.
  const akHalf = (ov?.hosts ?? []).filter((h) => h.authentik === "half" && !h.typo);
  const suggested = (ov?.hosts ?? []).filter((h) => h.suggest?.sure || (h.advanced_fix?.length ?? 0) > 0);
  const typos = (ov?.hosts ?? []).filter((h) => h.typo_domains);
  const deadNets = ov?.dead_nets ?? [];
  const inDeadNet = new Set(deadNets.flatMap((d) => d.hosts));
  const dead = (ov?.hosts ?? []).filter((h) => h.cleanup && !inDeadNet.has(h.id));

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
          {admin && ov.npm.linked && (
            <>
              <div className="flex flex-wrap gap-2">
                <Button onClick={() => setEdit("new")}>Proxy host toevoegen</Button>
                <Button variant="secondary" onClick={() => setTheme(true)}>
                  Sjabloon (Advanced en css)
                </Button>
                {suggested.length > 0 && (
                  <Button
                    variant="secondary"
                    onClick={() => setOpen({ title: `Nieuw doel voor ${suggested.length} proxy hosts`, action: { action: "retarget_many" } })}
                  >
                    Alle voorstellen toepassen ({suggested.length})
                  </Button>
                )}
                {akHalf.length > 0 && (
                  <Button
                    variant="secondary"
                    onClick={() => setOpen({ title: `Authentik aanvullen voor ${akHalf.length} proxy hosts`, action: { action: "ak_repair" } })}
                  >
                    Authentik aanvullen ({akHalf.length})
                  </Button>
                )}
                {typos.length > 0 && (
                  <span className="text-sm text-amber-700 dark:text-amber-300">
                    {typos.length} {typos.length === 1 ? "host" : "hosts"} met een tikfout in de naam: gebruik naam rechtzetten per host
                  </span>
                )}
              </div>
              <p className="text-xs text-slate-500">
                Volgorde als er veel tegelijk moet: 1. tikfouten in de naam rechtzetten{typos.length ? ` (${typos.length})` : ""}, 2. nieuwe doelen
                {suggested.length ? ` (${suggested.length})` : ""}, 3. sjabloon, 4. Authentik aanvullen{akHalf.length ? ` (${akHalf.length})` : ""}.
                Er loopt altijd maar één taak tegelijk op NPM; hosts met een tikfout slaat Authentik aanvullen over.
              </p>
            </>
          )}
          {admin && (deadNets.length > 0 || dead.length > 0) && (
            <Card title="Opruimen? Doelen die niet meer antwoorden">
              <p className="mb-2 text-sm text-slate-600 dark:text-slate-300">
                ClusterForge vroeg deze adressen zelf op vanaf de NPM-node: er antwoordt niets en geen enkele dienst heeft dezelfde naam. Er wordt
                niets gegokt. Opruimen maakt per host eerst een kopie; terugzetten kan per host hieronder bij &quot;Wat ClusterForge veranderde&quot;.
              </p>
              <ul className="space-y-2 text-sm">
                {deadNets.map((d) => (
                  <li key={d.prefix} className="flex flex-wrap items-center gap-2">
                    <span>
                      Net <b>{d.prefix}</b> bestaat niet meer: {d.hosts.length} proxy hosts ({d.names.slice(0, 6).join(", ")}
                      {d.names.length > 6 ? ", …" : ""})
                    </span>
                    <Button
                      variant="secondary"
                      onClick={() => setOpen({ title: `${d.hosts.length} proxy hosts in ${d.prefix} opruimen`, action: { action: "delete_dead", net: d.prefix } })}
                    >
                      Alles in {d.prefix} opruimen
                    </Button>
                  </li>
                ))}
                {dead.length > 0 && (
                  <li className="flex flex-wrap items-center gap-2">
                    <span>
                      {dead.length} losse {dead.length === 1 ? "host" : "hosts"}: {dead.map((h) => h.domains[0]).join(", ")}
                    </span>
                  </li>
                )}
              </ul>
            </Card>
          )}
          <ChangesCard admin={admin} onOpen={setOpen} />
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
                        onEdit={() => setEdit(h)}
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
      {theme && ov && <ThemeEditor ov={ov} onClose={() => setTheme(false)} />}
      {edit && ov && <HostEditor host={edit === "new" ? null : edit} ov={ov} onClose={() => setEdit(null)} />}
    </div>
  );
}

// ChangesCard: wat ClusterForge de laatste 24 uur aan proxy hosts veranderde,
// met per host terugdraaien (testlijst 2, 1.1).
function ChangesCard({ admin, onOpen }: { admin: boolean; onOpen: (o: Open) => void }) {
  const q = usePublishChanges(24);
  const list = q.data ?? [];
  if (list.length === 0) return null;
  return (
    <Card title={`Laatste 24 uur door ClusterForge veranderd · ${list.length}`}>
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Wanneer</th>
              <th className={thClass}>Host</th>
              <th className={thClass}>Wat</th>
              <th className={thClass}>Pagina</th>
              {admin && <th className={thClass} />}
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {list.map((c) => (
              <tr key={c.id}>
                <td className={`${tdClass} whitespace-nowrap text-xs`}>
                  {new Date(c.changed_at).toLocaleString("nl-BE", { dateStyle: "short", timeStyle: "short" })}
                  {c.changed_by && <div className="text-slate-500">{c.changed_by}</div>}
                </td>
                <td className={tdClass}>
                  {c.domain}
                  <div className="text-xs text-slate-500">{c.action}</div>
                </td>
                <td className={`${tdClass} text-xs`}>
                  {c.summary.map((l) => (
                    <div key={l}>{l}</div>
                  ))}
                </td>
                <td className={`${tdClass} text-xs`}>
                  {c.title_before || c.title_after ? (
                    <>
                      {c.title_before || "?"} → {c.title_after || "?"}
                    </>
                  ) : (
                    <span className="text-slate-500">geen titel</span>
                  )}
                </td>
                {admin && (
                  <td className={`${tdClass} text-xs whitespace-nowrap`}>
                    {c.reverted_at ? (
                      <Badge tone="slate">teruggedraaid</Badge>
                    ) : (
                      <button
                        type="button"
                        className="underline"
                        onClick={() => onOpen({ title: `Terugdraaien: ${c.domain}`, action: { action: "revert", change_id: c.id } })}
                      >
                        terugdraaien
                      </button>
                    )}
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

function HostRow({
  h,
  admin,
  akLinked,
  picked,
  onPick,
  onOpen,
  onEdit,
  onAccept,
  onRelease,
}: {
  h: PublishHost;
  admin: boolean;
  akLinked: boolean;
  picked: boolean;
  onPick: (on: boolean) => void;
  onOpen: (o: Open) => void;
  onEdit: () => void;
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
        {h.suggest && (
          <div className="text-xs text-sky-700 dark:text-sky-300" title={h.suggest.why}>
            voorstel: {h.suggest.forward_scheme}://{h.suggest.forward_host}:{h.suggest.forward_port} ({h.suggest.service_name}
            {h.suggest.sure ? "" : ", gok"})
          </div>
        )}
        {h.advanced_fix?.map((f) => (
          <div key={f.old} className="text-xs text-sky-700 dark:text-sky-300" title={f.why}>
            Advanced: {f.old} → {f.new}
          </div>
        ))}
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
            className={`mt-1 text-xs ${p.level === "crit" ? "text-red-700 dark:text-red-400" : p.level === "warn" ? "text-amber-700 dark:text-amber-300" : "text-slate-500"}`}
          >
            {p.text}
          </div>
        ))}
      </td>
      {admin && (
        <td className={`${tdClass} text-xs whitespace-nowrap`}>
          <div className="mb-1 flex flex-col items-start gap-1">
            <button type="button" className={btn} onClick={onEdit}>
              bewerken
            </button>
            {(h.suggest || (h.advanced_fix?.length ?? 0) > 0) && (
              <button
                type="button"
                className={btn}
                onClick={() => onOpen({ title: `Nieuw doel voor ${name}`, action: { action: "retarget_many", hosts: [h.id] } })}
              >
                voorstel toepassen
              </button>
            )}
            {h.cleanup && (
              <button
                type="button"
                className="text-red-700 underline dark:text-red-400"
                onClick={() => onOpen({ title: `${name} opruimen`, action: { action: "delete_dead", hosts: [h.id] } })}
              >
                opruimen
              </button>
            )}
            {h.typo_domains && (
              <button
                type="button"
                className={btn}
                onClick={() => onOpen({ title: `Naam rechtzetten: ${h.typo}`, action: { action: "update", host_id: h.id, domains: h.typo_domains } })}
              >
                naam rechtzetten
              </button>
            )}
          </div>
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
                      action: { action: "retarget", host_id: h.id, forward_host: h.suggest_vip },
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
