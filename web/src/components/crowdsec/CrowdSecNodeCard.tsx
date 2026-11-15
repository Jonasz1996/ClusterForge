"use client";

import Link from "next/link";
import { useState } from "react";
import { Modal } from "@/components/Modal";
import { ago, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import {
  crowdSecState,
  flag,
  originLabel,
  useApplyCrowdSecNode,
  useConfigureCrowdSecNode,
  useCrowdSec,
  useCrowdSecNode,
  useEnrollCrowdSecConsole,
  useRefreshCrowdSecNode,
  useSetCrowdSecNodes,
  type CrowdSecNode,
  type CrowdSecOverview,
} from "@/lib/crowdsec";

const timeFmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "short", timeStyle: "short" });

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : "Er ging iets mis.";
}

/** Kaart CrowdSec op de pagina van een node (fase 14). */
export function CrowdSecNodeCard({ nodeId, isAdmin }: { nodeId: string; isAdmin: boolean }) {
  const d = useCrowdSecNode(nodeId);
  const overview = useCrowdSec();
  const set = useSetCrowdSecNodes();
  const apply = useApplyCrowdSecNode();
  const refresh = useRefreshCrowdSecNode();
  const enroll = useEnrollCrowdSecConsole();
  const consoleKey = overview.data?.settings.console_key_set ?? false;
  const [edit, setEdit] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);
  if (!d.data) return null;
  const n = d.data.node;
  const st = crowdSecState(n);
  const onErr = (e: unknown) => setMsg(errText(e));
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span className="flex items-center gap-2">
            CrowdSec <Badge tone={st.tone}>{st.label}</Badge>
            {n.status?.version && <span className="text-xs font-normal text-slate-500">{n.status.version}</span>}
          </span>
          <span className="flex flex-wrap gap-2">
            {isAdmin && n.supported && (!n.enabled || !n.managed) && (
              <Button
                disabled={set.isPending}
                onClick={() =>
                  set.mutate({ node_ids: [n.node_id], managed: true }, { onSuccess: (r) => setMsg(r.skipped.join("; ") || "Aangezet; de stand en het plan volgen meteen."), onError: onErr })
                }
              >
                CrowdSec aanzetten
              </Button>
            )}
            {isAdmin && n.enabled && n.managed && (
              <>
                <Button variant="secondary" disabled={refresh.isPending || !n.online} onClick={() => refresh.mutate(n.node_id, { onError: onErr })}>
                  Vernieuwen
                </Button>
                <Button variant="secondary" disabled={!overview.data} onClick={() => setEdit(true)}>
                  Wijzigen
                </Button>
                <Button
                  disabled={apply.isPending || n.apply_running || !n.online}
                  onClick={() => apply.mutate({ id: n.node_id }, { onSuccess: () => setMsg("Toepassen gestart; volg het in de taak."), onError: onErr })}
                >
                  Toepassen
                </Button>
                {consoleKey && (
                  <Button
                    variant="secondary"
                    disabled={enroll.isPending || !n.online}
                    onClick={() => enroll.mutate(n.node_id, { onSuccess: () => setMsg("Wordt nu aangemeld bij de CrowdSec Console."), onError: onErr })}
                  >
                    Console aanmelden
                  </Button>
                )}
              </>
            )}
            <Link href={`/crowdsec?tab=alerts&node_id=${n.node_id}`} className="self-center text-sm text-brand-700 hover:underline dark:text-sky-300">
              Alle alerts
            </Link>
          </span>
        </span>
      }
    >
      {msg && <Alert kind="info">{msg}</Alert>}
      {!n.supported && <p className="text-sm text-slate-500">De agent is te oud voor CrowdSec. Werk hem bij via Nodes → Agents bijwerken.</p>}
      {n.supported && !n.enabled && (
        <p className="text-sm text-slate-500">
          CrowdSec staat hier niet aan. Aanzetten installeert CrowdSec en de bouncer en zet de profielen voor de software die ClusterForge op deze node herkent.
        </p>
      )}
      {n.apply_error && (
        <Alert>
          Toepassen mislukt{n.apply_failed_at && ` (${ago(n.apply_failed_at)})`}: {n.apply_error}
          {n.apply_job_id && (
            <>
              {" "}
              <Link href={`/taken/detail?id=${n.apply_job_id}`} className="underline">
                Taak bekijken
              </Link>
            </>
          )}
        </Alert>
      )}
      {n.console_error && (
        <Alert>
          Aanmelden bij de CrowdSec Console mislukt{n.console_failed_at && ` (${ago(n.console_failed_at)})`}: {n.console_error}
        </Alert>
      )}
      {n.enabled && (
        <div className="space-y-4">
          {n.plan && (
            <div>
              <div className="mb-1 text-xs font-medium uppercase tracking-wide text-slate-500">Profielen</div>
              <ul className="space-y-1 text-sm">
                {n.plan.profiles.map((p) => (
                  <li key={p.key}>
                    <span className={cx("font-medium", p.origin === "off" && "text-slate-400 line-through")}>{p.name}</span>{" "}
                    <span className="text-xs text-slate-500">
                      {originLabel[p.origin]}
                      {p.reason && ` (${p.reason})`}
                    </span>
                    {p.sources?.map((s) => (
                      <div key={s} className="ml-4 font-mono text-xs text-slate-500">
                        {s}
                      </div>
                    ))}
                    {p.note && <div className="ml-4 text-xs text-amber-700 dark:text-amber-400">{p.note}</div>}
                  </li>
                ))}
              </ul>
              <div className="mt-2 text-xs text-slate-500">
                Bouncer {n.bouncer ? "aan" : "uit"} · toegepast {n.applied_at ? ago(n.applied_at) : "nog niet"}
                {n.plan_hash && n.plan_hash !== n.applied_hash && " · een nieuwe wijziging is klaar"}
                {n.status?.capi && " · community-blocklist aan"}
                {consoleKey && (n.console_enrolled ? ` · Console aangemeld ${n.console_enrolled_at ? ago(n.console_enrolled_at) : ""}` : " · nog niet bij de Console aangemeld")}
              </div>
            </div>
          )}
          {(n.status?.sources?.length ?? 0) > 0 && (
            <div className="overflow-x-auto">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Logbron</th>
                    <th className={thClass}>Gelezen</th>
                    <th className={thClass}>Herkend</th>
                    <th className={thClass}>Naar scenario&apos;s</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {n.status!.sources!.map((s) => (
                    <tr key={s.source}>
                      <td className={cx(tdClass, "font-mono text-xs break-all")}>{s.source}</td>
                      <td className={cx(tdClass, "tabular-nums")}>{s.read}</td>
                      <td className={cx(tdClass, "tabular-nums", s.read > 100 && s.parsed === 0 && "text-red-600")}>{s.parsed}</td>
                      <td className={cx(tdClass, "tabular-nums")}>{s.poured}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {(n.status?.bouncers?.length ?? 0) > 0 && (
            <div className="text-sm">
              <span className="text-xs font-medium uppercase tracking-wide text-slate-500">Bouncers </span>
              {n.status!.bouncers!.map((b) => (
                <span key={b.name} className="mr-2" title={b.last_pull ? `laatst opgehaald ${ago(b.last_pull)}` : undefined}>
                  <Badge tone={b.valid ? "green" : "red"}>
                    {b.name} {b.version}
                  </Badge>
                </span>
              ))}
            </div>
          )}
          {d.data.alerts.length > 0 && (
            <div>
              <div className="mb-1 text-xs font-medium uppercase tracking-wide text-slate-500">Laatste alerts</div>
              <ul className="space-y-0.5 text-sm">
                {d.data.alerts.slice(0, 8).map((a) => (
                  <li key={a.id} className="flex flex-wrap gap-x-2">
                    <span className="text-xs text-slate-500">{timeFmt.format(new Date(a.started_at))}</span>
                    <span className="font-mono text-xs">{a.value}</span>
                    <span className="text-xs">
                      {flag(a.country)} {a.country}
                    </span>
                    <span className="font-mono text-xs text-slate-600 dark:text-slate-400">{a.scenario}</span>
                    {a.decisions > 0 && <Badge tone="red">ban</Badge>}
                  </li>
                ))}
              </ul>
            </div>
          )}
          {(n.status?.errors?.length ?? 0) > 0 && <Alert kind="info">cscli: {n.status!.errors!.join("; ")}</Alert>}
        </div>
      )}
      {edit && overview.data && <NodeDialog o={overview.data} n={n} onClose={() => setEdit(false)} />}
    </Card>
  );
}

export function NodeDialog({ o, n, onClose }: { o: Pick<CrowdSecOverview, "catalog">; n: CrowdSecNode; onClose: () => void }) {
  const configure = useConfigureCrowdSecNode();
  const [bouncer, setBouncer] = useState(n.bouncer);
  const [extra, setExtra] = useState<string[]>(n.extra_profiles);
  const [disabled, setDisabled] = useState<string[]>(n.disabled_profiles);
  const detected = new Set((n.plan?.profiles ?? []).filter((p) => p.origin !== "extra").map((p) => p.key));
  const toggle = (list: string[], set: (v: string[]) => void, key: string) => set(list.includes(key) ? list.filter((k) => k !== key) : [...list, key]);
  return (
    <Modal title={`CrowdSec op ${n.hostname}`} onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        <label className="flex items-start gap-2">
          <input type="checkbox" className="mt-1" checked={bouncer} onChange={(e) => setBouncer(e.target.checked)} />
          <span>
            <span className="font-medium">nftables-bouncer</span>
            <span className="block text-xs text-slate-500">Houdt geband verkeer tegen in de firewall van de node (met Docker ook naar containers). Zonder bouncer ziet CrowdSec alles, maar blokkeert niets.</span>
          </span>
        </label>
        <div>
          <div className="mb-1 font-medium">Profielen</div>
          <p className="mb-2 text-xs text-slate-500">Herkend staat vanzelf aan; zet een profiel uit als je het hier niet wilt, of zet er een bij als de herkenning iets mist.</p>
          <div className="grid gap-1 sm:grid-cols-2">
            {o.catalog.map((p) => {
              const auto = detected.has(p.key);
              const on = auto ? !disabled.includes(p.key) : extra.includes(p.key);
              const locked = p.key === "linux";
              return (
                <label key={p.key} className="flex items-start gap-2 rounded px-1 py-0.5 hover:bg-slate-50 dark:hover:bg-slate-800">
                  <input
                    type="checkbox"
                    className="mt-1"
                    checked={on || locked}
                    disabled={locked}
                    onChange={() => (auto ? toggle(disabled, setDisabled, p.key) : toggle(extra, setExtra, p.key))}
                  />
                  <span>
                    {p.name} {auto && <Badge tone="blue">herkend</Badge>}
                    <span className="block text-xs text-slate-500">{p.collections.join(", ")}</span>
                  </span>
                </label>
              );
            })}
          </div>
        </div>
        {configure.isError && <Alert>{errText(configure.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            disabled={configure.isPending}
            onClick={() =>
              configure.mutate(
                { id: n.node_id, body: { bouncer, extra_profiles: extra.filter((k) => !detected.has(k)), disabled_profiles: disabled.filter((k) => detected.has(k)) } },
                { onSuccess: onClose },
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

