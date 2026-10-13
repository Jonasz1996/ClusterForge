"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { JoinDialog, SettingsDialog, UpgradeDialog } from "@/components/databases/ComponentChanges";
import { Modal } from "@/components/Modal";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { QueryState, formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card } from "@/components/ui";
import {
  positionFrom,
  recoverAction,
  recovering,
  roleLabel,
  sinceFmt,
  stateInfo,
  useClusterComponents,
  useComponentManage,
  useComponentTemplates,
  useManageComponent,
  useReapplyComponent,
  useRecoverComponent,
  useRecoverPlan,
  useRemoveComponent,
  useSetAutoRecover,
  type ClusterComponent,
  type ComponentManageAction,
  type ComponentManageNode,
} from "@/lib/components";
import type { ClusterDetail } from "@/lib/inventory";

// ClusterDatabaseCard toont de databases die ClusterForge op de nodes van
// dit cluster zette, en de knop om er een toe te voegen zolang er een soort
// ontbreekt.
export function ClusterDatabaseCard({ c, isAdmin }: { c: ClusterDetail; isAdmin: boolean }) {
  const q = useClusterComponents(c.id);
  const templates = useComponentTemplates();
  const list = q.data ?? [];
  const add = `/clusters/database?id=${c.id}`;
  const missing = (templates.data ?? []).some((t) => !list.some((x) => x.engine === t.engine));
  return (
    <Card
      title={
        <span className="flex flex-wrap items-start justify-between gap-2">
          <span>Database</span>
          {isAdmin && q.data && missing && (
            <Link href={add}>
              <Button variant="secondary">Database toevoegen</Button>
            </Link>
          )}
        </span>
      }
    >
      <QueryState q={q}>
        {q.data && list.length === 0 && (
          <p className="text-sm text-slate-600 dark:text-slate-400">
            ClusterForge heeft hier geen database gezet. Met Database toevoegen komt MariaDB Galera of PostgreSQL met Patroni op
            de nodes van dit cluster, zodat de app zijn database naast zich heeft. Wat er al aan databases draait, staat bij{" "}
            <Link href="/databases" className="underline">
              Databases
            </Link>
            .
          </p>
        )}
        <div className="space-y-6">
          {list.map((comp) => (
            <Component key={comp.id} c={c} comp={comp} isAdmin={isAdmin} />
          ))}
        </div>
      </QueryState>
    </Card>
  );
}

function Component({ c, comp, isAdmin }: { c: ClusterDetail; comp: ClusterComponent; isAdmin: boolean }) {
  const st = stateInfo[comp.state] ?? { label: comp.state, tone: "slate" as const };
  const busy = comp.state === "installing" || comp.state === "applying";
  const [reapply, setReapply] = useState(false);
  const [recover, setRecover] = useState(false);
  const [manage, setManage] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [upgrade, setUpgrade] = useState(false);
  const [joinOpen, setJoinOpen] = useState(false);
  const remove = useRemoveComponent(c.id);
  const auto = useSetAutoRecover(c.id);
  const rec = recovering(comp);
  const applied = comp.applied_revision !== null;
  // Het VIP staat er al apart.
  // Het VIP staat hieronder; welke nodes later bij etcd kwamen, houdt
  // ClusterForge zelf bij.
  const settings = Object.entries(comp.params).filter(([k, v]) => k !== "vip" && k !== "etcd_joined" && v !== null);
  const out = new Map(comp.nodes_out.map((o) => [o.hostname, o]));
  // Bij PostgreSQL start Patroni de nodes zelf opnieuw; Beheer kan wel
  // overschakelen en een replica opnieuw opbouwen.
  const pg = comp.engine === "postgresql";
  return (
    <div className="space-y-3 text-sm">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="flex flex-wrap items-center gap-2">
          <span className="font-medium">{comp.title}</span>
          <Badge tone={st.tone}>{st.label}</Badge>
          <span className="text-slate-500">
            revisie {comp.revision}
            {comp.applied_revision !== null && comp.applied_revision !== comp.revision && `, toegepast ${comp.applied_revision}`}
          </span>
        </span>
        {(isAdmin || applied) && (
          <span className="flex flex-wrap gap-2">
            {applied && (
              <Link href={`/clusters/databases?id=${c.id}&component=${comp.id}`}>
                <Button variant="secondary">Databases en gebruikers</Button>
              </Link>
            )}
            {isAdmin && comp.applied_revision === null && !busy && (
              <Button variant="secondary-danger" disabled={remove.isPending} onClick={() => remove.mutate(comp.id)}>
                Weghalen
              </Button>
            )}
            {isAdmin && applied && (
              <Button variant="secondary" disabled={busy || rec} onClick={() => setManage(true)}>
                Beheer…
              </Button>
            )}
            {isAdmin && applied && (
              <Button variant="secondary" disabled={busy || rec} onClick={() => setSettingsOpen(true)}>
                Instellingen…
              </Button>
            )}
            {isAdmin && applied && (
              <Button variant="secondary" disabled={busy || rec} onClick={() => setUpgrade(true)}>
                Bijwerken…
              </Button>
            )}
            {isAdmin && applied && (
              <Button variant="secondary" disabled={busy || rec} onClick={() => setJoinOpen(true)}>
                Node toevoegen…
              </Button>
            )}
            {isAdmin && applied && !pg && (
              <Button variant="secondary" disabled={busy || rec} onClick={() => setRecover(true)}>
                Opnieuw starten
              </Button>
            )}
            {isAdmin && (
              <Button variant="secondary" disabled={busy || rec} onClick={() => setReapply(true)}>
                Opnieuw toepassen
              </Button>
            )}
          </span>
        )}
      </div>
      {comp.down_since !== null && !rec && (
        <Alert>
          De database draait niet sinds {sinceFmt.format(new Date(comp.down_since))}: {comp.down_reason}.{" "}
          {comp.auto_recover
            ? "ClusterForge start haar vanzelf opnieuw op de node met de nieuwste data, zodra alle datanodes antwoorden en MariaDB nergens meer start. Ontbreekt er een node, kies dan zelf Opnieuw starten."
            : "Vanzelf opnieuw starten staat uit; kies Opnieuw starten."}
        </Alert>
      )}
      {comp.recovery && rec && (
        <Alert kind="info">
          ClusterForge start de database opnieuw.{" "}
          <Link href={`/taken/detail?id=${comp.recovery.job_id}`} className="underline">
            Bekijk het logboek
          </Link>
          .
        </Alert>
      )}
      {comp.recovery && comp.recovery.status === "failed" && comp.down_since !== null && (
        <Alert>
          Opnieuw starten mislukte{comp.recovery.error && `: ${comp.recovery.error}`}.{" "}
          <Link href={`/taken/detail?id=${comp.recovery.job_id}`} className="underline">
            Bekijk het logboek
          </Link>
          .
        </Alert>
      )}
      {comp.job_id && (
        <p className="text-slate-500">
          {busy ? "Bezig: " : "Laatste taak: "}
          <Link href={`/taken/detail?id=${comp.job_id}`} className="underline">
            bekijk het logboek
          </Link>
          {busy &&
            (pg
              ? ". ClusterForge zet eerst etcd op alle nodes, en dan Patroni node voor node; het wacht tot elke replica de primary bijhoudt."
              : ". ClusterForge zet de nodes een voor een op en wacht na elke node tot hij Synced is.")}
        </p>
      )}
      {comp.state === "failed" && (
        <Alert>
          De laatste taak mislukte{comp.job_error && `: ${comp.job_error}`}. Los het op en kies Opnieuw toepassen; wat al klaar
          was, blijft staan.
          {comp.applied_revision === null && " Weghalen haalt de database uit ClusterForge; de nodes zelf blijven zoals ze zijn."}
        </Alert>
      )}
      {remove.isError && <Alert>{remove.error.message}</Alert>}
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Node</th>
              <th className={thClass}>Rol</th>
              {applied && !pg && <th className={thClass}>Doet mee</th>}
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {comp.nodes.map((n) => {
              const o = out.get(n.hostname);
              return (
                <tr key={n.node_id}>
                  <td className={tdClass}>
                    <Link href={`/nodes/detail?id=${n.node_id}`} className="font-medium hover:underline">
                      {n.hostname}
                    </Link>
                  </td>
                  <td className={tdClass}>{roleLabel[n.role] ?? n.role}</td>
                  {applied && !pg && (
                    <td className={tdClass}>
                      {n.role !== "data" || comp.down_since !== null ? (
                        <span className="text-slate-500">–</span>
                      ) : o ? (
                        <span className="text-amber-700 dark:text-amber-300">
                          nee, sinds {sinceFmt.format(new Date(o.since))}: {o.reason}
                        </span>
                      ) : (
                        "ja"
                      )}
                    </td>
                  )}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {comp.vip && pg && (
        <p className="text-slate-600 dark:text-slate-400">
          Eigen VIP <span className="font-mono">{comp.vip.address}</span>
          {comp.vip.owner ? ` staat op ${comp.vip.owner}, de primary` : " staat op geen enkele node"}. keepalived zet het op{" "}
          {comp.vip.nodes.join(", ")} alleen op de node die volgens Patroni de primary is; de app verbindt met dit adres.
        </p>
      )}
      {comp.vip && !pg && (
        <p className="text-slate-600 dark:text-slate-400">
          {comp.vip.own ? "Eigen VIP" : "VIP"} <span className="font-mono">{comp.vip.address}</span>
          {comp.vip.owner ? ` staat op ${comp.vip.owner}` : " staat op geen enkele node"} en volgt de database op{" "}
          {comp.vip.nodes.join(", ")}: keepalived zet het alleen op een node waarvan MariaDB mag schrijven.
        </p>
      )}
      {settings.length > 0 && (
        <p className="text-slate-600 dark:text-slate-400">
          Instellingen:{" "}
          {settings.map(([k, v], i) => (
            <span key={k}>
              {i > 0 && ", "}
              <span className="font-mono">{k}</span> {String(v)}
            </span>
          ))}
          .
        </p>
      )}
      {applied && !pg && (
        <label className="flex items-start gap-2">
          <input
            type="checkbox"
            className="mt-0.5"
            checked={comp.auto_recover}
            disabled={!isAdmin || auto.isPending}
            onChange={(e) => auto.mutate({ id: comp.id, on: e.target.checked })}
          />
          <span>
            Vanzelf opnieuw starten na een volledige uitval
            <span className="block text-slate-500">
              ClusterForge wacht tot alle datanodes er zijn en MariaDB nergens meer start, en begint het cluster dan op de node
              met de nieuwste data. Een node die niet startte terwijl het cluster draait, start het daar opnieuw. Is MariaDB
              overal met de hand gestopt, dan blijft ze uit.
            </span>
          </span>
        </label>
      )}
      {auto.isError && <Alert>{auto.error.message}</Alert>}
      {reapply && <ReapplyDialog c={c} comp={comp} onClose={() => setReapply(false)} />}
      {recover && <RecoverDialog c={c} comp={comp} onClose={() => setRecover(false)} />}
      {manage && <ManageDialog c={c} comp={comp} onClose={() => setManage(false)} />}
      {settingsOpen && <SettingsDialog c={c} comp={comp} onClose={() => setSettingsOpen(false)} />}
      {upgrade && <UpgradeDialog c={c} comp={comp} onClose={() => setUpgrade(false)} />}
      {joinOpen && <JoinDialog c={c} comp={comp} onClose={() => setJoinOpen(false)} />}
    </div>
  );
}

// ManageDialog is Beheer: per datanode hoe het ermee staat en wat er kan,
// met bij elke actie waarom ze niet kan of wat er gebeurt.
function ManageDialog({ c, comp, onClose }: { c: ClusterDetail; comp: ClusterComponent; onClose: () => void }) {
  const view = useComponentManage(c.id, comp.id);
  const run = useManageComponent(c.id);
  const router = useRouter();
  const totp = useProdTotp();
  const [confirm, setConfirm] = useState("");
  // De keuze houdt de hash van het overzicht waarop de gebruiker klikte.
  const [pick, setPick] = useState<{ node: ComponentManageNode; action: ComponentManageAction; hash: string } | null>(null);
  const prod = c.environment === "prod";
  const pg = comp.engine === "postgresql";
  const v = view.data;
  return (
    <Modal title={`${comp.title} beheren`} onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        {pick ? (
          <>
            <p className="font-medium">
              {pick.action.title}: {pick.node.hostname}
            </p>
            <ul className="list-disc space-y-1 pl-5">
              {pick.action.effects.map((e) => (
                <li key={e}>{e}</li>
              ))}
            </ul>
            <p className="text-slate-500">ClusterForge doet het als taak; het logboek toont elke stap.</p>
            {prod && <ProdConfirm slug={c.slug} value={confirm} onChange={setConfirm} />}
            {run.isError && <Alert>{run.error.message}</Alert>}
            <div className="flex justify-end gap-2">
              <Button
                variant="secondary"
                onClick={() => {
                  setPick(null);
                  run.reset();
                }}
              >
                Terug
              </Button>
              <Button
                disabled={run.isPending || (prod && !prodConfirmed(c.slug, confirm, totp))}
                onClick={async () => {
                  const j = await run
                    .mutateAsync({
                      id: comp.id,
                      action: pick.action.action,
                      node_id: pick.node.node_id,
                      hash: pick.hash,
                      confirm: prod ? confirm : undefined,
                    })
                    .catch(() => null);
                  if (j) router.push(`/taken/detail?id=${j.id}`);
                }}
              >
                {run.isPending ? "Bezig…" : pick.action.title}
              </Button>
            </div>
          </>
        ) : (
          <>
            <p>
              {pg
                ? "Primary maken schakelt over naar de synchrone replica, bijvoorbeeld voor onderhoud aan de primary; het VIP gaat mee. Opnieuw opbouwen gooit de data van een replica weg en kopieert alles opnieuw van de primary, voor een replica die niet meer bijkomt."
                : "Opnieuw synchroniseren gooit de positie van een node weg; hij haalt dan alle data opnieuw bij een node die Synced is. Gebruik het voor een node die niet meer bijkomt, of waarvan de data niet klopt."}
            </p>
            <p className="text-slate-500">
              Onderhoud en herstarten doe je bij de node zelf.{" "}
              {pg
                ? "Is de node de primary, dan schakelt ClusterForge eerst over; na de herstart wacht het tot de node weer bijhoudt."
                : "Na de herstart wacht ClusterForge tot de node weer Synced is."}
            </p>
            <QueryState q={view}>
              {v && (
                <ul className="divide-y divide-slate-100 border-y border-slate-100 dark:divide-slate-800 dark:border-slate-800">
                  {v.nodes.map((n) => (
                    <li key={n.node_id} className="gap-4 py-3 sm:grid sm:grid-cols-[minmax(0,1fr)_18rem]">
                      <div className="space-y-1">
                        <span className="flex flex-wrap items-center gap-1">
                          <span className="font-medium">{n.hostname}</span>
                          {n.role === "primary" && <Badge tone="blue">primary</Badge>}
                          {n.sync && <Badge tone="slate">synchroon</Badge>}
                          {n.vip && <Badge tone="green">VIP</Badge>}
                        </span>
                        <span
                          className={`block ${n.healthy ? "text-slate-600 dark:text-slate-300" : "text-amber-700 dark:text-amber-300"}`}
                        >
                          {n.state}
                        </span>
                        {pg && n.role !== "primary" && (
                          <span className="block text-slate-500">
                            achterstand: {n.lag_bytes === null ? "onbekend" : formatBytes(n.lag_bytes)}
                          </span>
                        )}
                        {n.pending_restart && <span className="block text-slate-500">wacht op een herstart van PostgreSQL</span>}
                      </div>
                      <div className="mt-2 space-y-2 sm:mt-0">
                        {n.actions.length === 0
                          ? pg && <p className="text-slate-500">De primary; maak een replica primary om over te schakelen.</p>
                          : n.actions.map((a) => (
                              <div key={a.action}>
                                <Button
                                  variant="secondary"
                                  disabled={!a.allowed}
                                  onClick={() => {
                                    setPick({ node: n, action: a, hash: v.hash });
                                    run.reset();
                                  }}
                                >
                                  {a.title}
                                </Button>
                                {!a.allowed && <span className="mt-1 block text-xs text-slate-500">{a.reason}</span>}
                              </div>
                            ))}
                      </div>
                    </li>
                  ))}
                </ul>
              )}
            </QueryState>
            <div className="flex justify-end">
              <Button variant="secondary" onClick={onClose}>
                Sluiten
              </Button>
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}

function RecoverDialog({ c, comp, onClose }: { c: ClusterDetail; comp: ClusterComponent; onClose: () => void }) {
  const plan = useRecoverPlan(c.id, comp.id);
  const recover = useRecoverComponent(c.id);
  const router = useRouter();
  const totp = useProdTotp();
  const [confirm, setConfirm] = useState("");
  const prod = c.environment === "prod";
  const p = plan.data;
  return (
    <Modal title={`${comp.title} opnieuw starten`} onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        <QueryState q={plan}>
          {p && (
            <>
              <p>
                {p.primary
                  ? `De database draait nog op ${p.primary}. De andere nodes sluiten daar aan, een voor een; het cluster begint niet opnieuw.`
                  : p.begin
                    ? `${p.begin} heeft de nieuwste data en begint het cluster; daarna sluiten de andere nodes een voor een aan en halen ze wat ze missen. Er gaat geen transactie verloren.`
                    : "ClusterForge leest eerst de positie van elke node en begint het cluster op de node met de nieuwste data; daarna sluiten de andere een voor een aan."}
              </p>
              <div className="overflow-x-auto">
                <table className={tableClass}>
                  <thead>
                    <tr>
                      <th className={thClass}>Node</th>
                      <th className={thClass}>Seqno</th>
                      <th className={thClass}>Wat er gebeurt</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                    {p.nodes.map((n) => (
                      <tr key={n.node_id}>
                        <td className={tdClass}>
                          <span className="font-medium">{n.hostname}</span>
                          <span className="block text-slate-500">{n.state}</span>
                        </td>
                        <td className={tdClass}>
                          {n.seqno >= 0 ? (
                            <span title={positionFrom[n.from] ?? n.from}>
                              {n.seqno}
                              {n.safe_to_bootstrap && <span className="text-slate-500"> (stopte laatst)</span>}
                            </span>
                          ) : (
                            <span className="text-slate-500">onbekend</span>
                          )}
                        </td>
                        <td className={tdClass}>
                          {n.action === "begin" ? (
                            <Badge tone="blue">{recoverAction[n.action]}</Badge>
                          ) : n.action === "skip" ? (
                            <Badge tone="amber">{recoverAction[n.action]}</Badge>
                          ) : (
                            (recoverAction[n.action] ?? n.action)
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {p.findings.map((f) => (
                <Alert key={f.code + f.hostname} kind={f.level === "blocked" ? "error" : "info"}>
                  {f.message}
                </Alert>
              ))}
              {prod && <ProdConfirm slug={c.slug} value={confirm} onChange={setConfirm} />}
            </>
          )}
        </QueryState>
        {recover.isError && <Alert>{recover.error.message}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            disabled={!p || p.blocked || recover.isPending || (prod && !prodConfirmed(c.slug, confirm, totp))}
            onClick={async () => {
              if (!p) return;
              const j = await recover
                .mutateAsync({ id: comp.id, hash: p.hash, confirm: prod ? confirm : undefined })
                .catch(() => null);
              if (j) router.push(`/taken/detail?id=${j.id}`);
            }}
          >
            {recover.isPending ? "Bezig…" : "Opnieuw starten"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function ReapplyDialog({ c, comp, onClose }: { c: ClusterDetail; comp: ClusterComponent; onClose: () => void }) {
  const reapply = useReapplyComponent(c.id);
  const router = useRouter();
  const totp = useProdTotp();
  const [confirm, setConfirm] = useState("");
  const prod = c.environment === "prod";
  return (
    <Modal title={`${comp.title} opnieuw toepassen`} onClose={onClose}>
      <div className="space-y-4 text-sm">
        {comp.engine === "postgresql" ? (
          <p>
            ClusterForge zet de configuratie van revisie {comp.revision} terug op elke node: eerst etcd overal, dan Patroni node
            voor node. Na elke node wacht het tot Patroni er weer meedoet. Herstart Patroni op de primary, dan wordt een replica
            primary en gaat het VIP mee, dus de database blijft bereikbaar. Wacht een instelling nog op een herstart van
            PostgreSQL, dan doet ClusterForge die daarna node voor node, de primary als laatste.
          </p>
        ) : (
          <p>
            ClusterForge zet de configuratie van revisie {comp.revision} terug op elke node, een voor een. Een node waarop iets
            veranderde, herstart MariaDB en ClusterForge wacht tot hij weer Synced is voor hij verder gaat. De andere nodes
            blijven draaien, dus de database blijft bereikbaar.
          </p>
        )}
        {prod && <ProdConfirm slug={c.slug} value={confirm} onChange={setConfirm} />}
        {reapply.isError && <Alert>{reapply.error.message}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            disabled={reapply.isPending || (prod && !prodConfirmed(c.slug, confirm, totp))}
            onClick={async () => {
              const j = await reapply.mutateAsync({ id: comp.id, confirm: prod ? confirm : undefined }).catch(() => null);
              if (j) router.push(`/taken/detail?id=${j.id}`);
            }}
          >
            {reapply.isPending ? "Bezig…" : "Opnieuw toepassen"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
