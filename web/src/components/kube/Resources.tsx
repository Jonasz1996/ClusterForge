"use client";

import type { ReactNode } from "react";
import { EventList, KubeError, Learn, ObjectLink, Term } from "@/components/kube/Kube";
import { Empty, formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Badge, Card, cx, Select } from "@/components/ui";
import {
  age,
  kubectl,
  nsFlag,
  objectHelp,
  refOf,
  serviceTypeHelp,
  useKubeConfig,
  useKubeEvents,
  useKubeLearning,
  useKubeNetwork,
  useKubeObjects,
  useKubeResources,
  useKubeStorage,
  type KubeResourceType,
} from "@/lib/kube";

type TabProps = { id: string; namespace: string; system: boolean };

function Table({ children }: { children: ReactNode }) {
  return (
    <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
      <table className={tableClass}>{children}</table>
    </div>
  );
}

function Name({ id, kind, namespace, name }: { id: string; kind: string; namespace: string; name: string }) {
  return (
    <>
      <div className="font-medium break-all">
        <ObjectLink clusterId={id} objRef={refOf(kind, namespace, name)}>
          {name}
        </ObjectLink>
      </div>
      {namespace && <div className="text-xs text-slate-500">{namespace}</div>}
    </>
  );
}

function Outside({ href }: { href: string }) {
  return (
    <a href={href} target="_blank" rel="noreferrer" className="break-all text-brand-700 hover:underline dark:text-sky-300">
      {href}
    </a>
  );
}

export function NetworkTab({ id, namespace, system }: TabProps) {
  const net = useKubeNetwork(id, namespace, system);
  const d = net.data;
  return (
    <div className="space-y-4">
      <Learn commands={[kubectl(`get services,ingresses ${nsFlag(namespace)}`), kubectl("get endpointslices -A")]}>
        <p>
          Pods komen en gaan, en elke nieuwe pod krijgt een ander IP-adres. Een <strong>Service</strong> geeft een groep pods
          daarom één vast adres en een naam in DNS, zoals <code>web.demo.svc.cluster.local</code>. Hij kiest de pods op hun
          labels: kiest hij er geen, dan klopt er meestal een label niet.
        </p>
        <p>
          Een <strong>Ingress</strong> brengt webverkeer van buiten naar een Service, op hostnaam en pad. In k3s doet Traefik dat
          op poort 80 en 443 van elke node, dus ook op het VIP. Zet de hostnaam in je DNS of hosts-bestand op het VIP.
        </p>
      </Learn>
      {net.isError && <KubeError error={net.error} />}
      {d && (
        <>
          <Card title="Services">
            {d.services.length === 0 ? (
              <Empty>Geen Services{namespace ? ` in ${namespace}` : ""}.</Empty>
            ) : (
              <Table>
                <thead className="border-b border-slate-200 dark:border-slate-800">
                  <tr>
                    <th className={thClass}>Service</th>
                    <th className={thClass}>Soort</th>
                    <th className={thClass}>Intern adres</th>
                    <th className={thClass}>Poorten</th>
                    <th className={thClass}>Van buiten</th>
                    <th className={`${thClass} text-right`}>Pods</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {d.services.map((s) => (
                    <tr key={`${s.namespace}/${s.name}`}>
                      <td className={tdClass}>
                        <Name id={id} kind="Service" namespace={s.namespace} name={s.name} />
                      </td>
                      <td className={tdClass}>
                        <Term help={serviceTypeHelp[s.type] ?? s.type}>{s.type}</Term>
                      </td>
                      <td className={`${tdClass} font-mono text-xs whitespace-nowrap`}>{s.cluster_ip || "–"}</td>
                      <td className={`${tdClass} text-xs`}>
                        {s.ports.map((p) => (
                          <div key={`${p.port}/${p.protocol}`} className="whitespace-nowrap">
                            {p.port}
                            {p.target_port && p.target_port !== String(p.port) && ` → ${p.target_port}`}
                            {p.protocol !== "TCP" && ` ${p.protocol}`}
                            {p.node_port > 0 && <span className="text-slate-500"> · node {p.node_port}</span>}
                          </div>
                        ))}
                      </td>
                      <td className={`${tdClass} text-xs`}>
                        {s.url ? <Outside href={s.url} /> : s.external.length > 0 ? s.external.join(", ") : "–"}
                      </td>
                      <td className={`${tdClass} text-right`}>
                        {Object.keys(s.selector).length === 0 ? (
                          <span className="text-slate-500" title="Zonder selector kiest deze Service zelf geen pods">
                            –
                          </span>
                        ) : (
                          <span
                            title={Object.entries(s.selector)
                              .map(([k, v]) => `${k}=${v}`)
                              .join(", ")}
                          >
                            <Badge tone={s.pods === 0 ? "red" : "green"}>{s.pods}</Badge>
                          </span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
            {d.services.some((s) => s.pods === 0 && Object.keys(s.selector).length > 0) && (
              <p className="mt-3 text-xs text-red-700 dark:text-red-300">
                Een Service met 0 pods kiest geen enkele pod: de labels in zijn selector staan op geen pod in die namespace.
              </p>
            )}
          </Card>
          <Card title="Ingresses">
            {d.ingresses.length === 0 ? (
              <Empty>Geen Ingresses{namespace ? ` in ${namespace}` : ""}.</Empty>
            ) : (
              <Table>
                <thead className="border-b border-slate-200 dark:border-slate-800">
                  <tr>
                    <th className={thClass}>Ingress</th>
                    <th className={thClass}>Webadres</th>
                    <th className={thClass}>Naar</th>
                    <th className={thClass}>Bereikbaar op</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {d.ingresses.map((i) => (
                    <tr key={`${i.namespace}/${i.name}`}>
                      <td className={tdClass}>
                        <Name id={id} kind="Ingress" namespace={i.namespace} name={i.name} />
                        {i.class && <div className="text-xs text-slate-500">via {i.class}</div>}
                      </td>
                      <td className={`${tdClass} text-xs`}>
                        {i.rules.map((r) => (
                          <div key={`${r.host}${r.path}`} className="flex flex-wrap items-center gap-1">
                            {r.url ? <Outside href={r.url} /> : `${r.host || "*"}${r.path}`}
                            {r.tls && <Badge tone="green">TLS</Badge>}
                          </div>
                        ))}
                      </td>
                      <td className={`${tdClass} text-xs`}>
                        {i.rules.map((r) => (
                          <div key={`${r.host}${r.path}`}>
                            {r.service ? (
                              <ObjectLink clusterId={id} objRef={refOf("Service", i.namespace, r.service)}>
                                {r.service}:{r.port}
                              </ObjectLink>
                            ) : (
                              "–"
                            )}
                          </div>
                        ))}
                      </td>
                      <td className={`${tdClass} font-mono text-xs`}>{i.address.join(", ") || "–"}</td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>
        </>
      )}
    </div>
  );
}

export function StorageTab({ id, namespace, system }: TabProps) {
  const st = useKubeStorage(id, namespace, system);
  const d = st.data;
  const tone = (phase: string) => (phase === "Bound" ? "green" : phase === "Pending" ? "amber" : "red");
  return (
    <div className="space-y-4">
      <Learn commands={[kubectl(`get pvc ${nsFlag(namespace)}`), kubectl("get pv"), kubectl("get storageclass")]}>
        <p>
          Een container verliest zijn bestanden als hij herstart. Wat moet blijven, zet je op een volume: de pod vraagt opslag met
          een <strong>PersistentVolumeClaim</strong> (PVC) en Kubernetes bindt die aan een <strong>PersistentVolume</strong> (PV).
        </p>
        <p>
          k3s heeft <strong>local-path</strong>: een map op de schijf van één node. Simpel en snel, maar de pod kan alleen op die
          node draaien, en valt de node weg, dan is de data er niet. Een claim blijft Pending tot een pod hem gebruikt.
        </p>
      </Learn>
      {st.isError && <KubeError error={st.error} />}
      {d && (
        <>
          <Card title="Claims">
            {d.claims.length === 0 ? (
              <Empty>Geen claims{namespace ? ` in ${namespace}` : ""}.</Empty>
            ) : (
              <Table>
                <thead className="border-b border-slate-200 dark:border-slate-800">
                  <tr>
                    <th className={thClass}>Claim</th>
                    <th className={thClass}>Status</th>
                    <th className={thClass}>Grootte</th>
                    <th className={thClass}>Klasse</th>
                    <th className={thClass}>Volume</th>
                    <th className={thClass}>Gebruikt door</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {d.claims.map((c) => (
                    <tr key={`${c.namespace}/${c.name}`}>
                      <td className={tdClass}>
                        <Name id={id} kind="PersistentVolumeClaim" namespace={c.namespace} name={c.name} />
                      </td>
                      <td className={tdClass}>
                        <Badge tone={tone(c.phase)}>{c.phase}</Badge>
                      </td>
                      <td className={`${tdClass} whitespace-nowrap`}>{c.bytes > 0 ? formatBytes(c.bytes) : "–"}</td>
                      <td className={tdClass}>{c.class || "–"}</td>
                      <td className={`${tdClass} text-xs break-all`}>
                        {c.volume ? (
                          <ObjectLink clusterId={id} objRef={refOf("PersistentVolume", "", c.volume)}>
                            {c.volume}
                          </ObjectLink>
                        ) : (
                          "–"
                        )}
                      </td>
                      <td className={`${tdClass} text-xs`}>
                        {c.used_by.length === 0
                          ? "–"
                          : c.used_by.map((p) => (
                              <div key={p}>
                                <ObjectLink clusterId={id} objRef={refOf("Pod", c.namespace, p)}>
                                  {p}
                                </ObjectLink>
                              </div>
                            ))}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>
          <Card title="Volumes">
            {d.volumes.length === 0 ? (
              <Empty>Geen volumes.</Empty>
            ) : (
              <Table>
                <thead className="border-b border-slate-200 dark:border-slate-800">
                  <tr>
                    <th className={thClass}>Volume</th>
                    <th className={thClass}>Status</th>
                    <th className={thClass}>Grootte</th>
                    <th className={thClass}>Claim</th>
                    <th className={thClass}>Op</th>
                    <th className={thClass}>Na de claim</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {d.volumes.map((v) => (
                    <tr key={v.name}>
                      <td className={`${tdClass} break-all`}>
                        <Name id={id} kind="PersistentVolume" namespace="" name={v.name} />
                      </td>
                      <td className={tdClass}>
                        <Badge tone={v.phase === "Bound" ? "green" : v.phase === "Available" ? "slate" : "amber"}>
                          {v.phase}
                        </Badge>
                      </td>
                      <td className={`${tdClass} whitespace-nowrap`}>{formatBytes(v.bytes)}</td>
                      <td className={`${tdClass} text-xs`}>{v.claim || "–"}</td>
                      <td className={`${tdClass} text-xs`}>
                        {v.node ? (
                          <ObjectLink clusterId={id} objRef={refOf("Node", "", v.node)}>
                            {v.node}
                          </ObjectLink>
                        ) : (
                          "–"
                        )}
                        {v.path && <div className="font-mono break-all text-slate-500">{v.path}</div>}
                      </td>
                      <td className={tdClass}>
                        <Term
                          help={
                            v.reclaim === "Delete"
                              ? "Delete: verdwijnt de claim, dan verdwijnen het volume en de data."
                              : "Retain: het volume en de data blijven als de claim verdwijnt."
                          }
                        >
                          {v.reclaim}
                        </Term>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>
          <Card title="StorageClasses">
            {d.classes.length === 0 ? (
              <Empty>Geen StorageClasses.</Empty>
            ) : (
              <Table>
                <thead className="border-b border-slate-200 dark:border-slate-800">
                  <tr>
                    <th className={thClass}>Klasse</th>
                    <th className={thClass}>Maker</th>
                    <th className={thClass}>Binden</th>
                    <th className={thClass}>Na de claim</th>
                    <th className={thClass}>Vergroten</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {d.classes.map((c) => (
                    <tr key={c.name}>
                      <td className={tdClass}>
                        <span className="font-medium">{c.name}</span> {c.default && <Badge tone="blue">standaard</Badge>}
                      </td>
                      <td className={`${tdClass} font-mono text-xs`}>{c.provisioner}</td>
                      <td className={tdClass}>
                        <Term
                          help={
                            c.binding === "WaitForFirstConsumer"
                              ? "Het volume wordt pas gemaakt als een pod de claim gebruikt, op de node van die pod."
                              : "Het volume wordt meteen gemaakt."
                          }
                        >
                          {c.binding}
                        </Term>
                      </td>
                      <td className={tdClass}>{c.reclaim}</td>
                      <td className={tdClass}>{c.expansion ? "ja" : "nee"}</td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>
        </>
      )}
    </div>
  );
}

export function ConfigTab({ id, namespace, system }: TabProps) {
  const cfg = useKubeConfig(id, namespace, system);
  const d = cfg.data;
  return (
    <div className="space-y-4">
      <Learn commands={[kubectl(`get configmaps,secrets ${nsFlag(namespace)}`)]}>
        <p>
          Instellingen horen niet in het image. Een <strong>ConfigMap</strong> bewaart ze als sleutels en waarden; een pod leest
          ze als omgevingsvariabelen of als bestanden. Een <strong>Secret</strong> doet hetzelfde voor wachtwoorden en sleutels.
        </p>
        <p>
          ClusterForge toont van een Secret alleen de sleutels en hoe lang de waarde is: de waarden zelf verlaten de server van
          het cluster nooit. Let op: in Kubernetes zijn ze alleen base64, geen versleuteling; wie <code>get secret</code> mag,
          leest ze.
        </p>
      </Learn>
      {cfg.isError && <KubeError error={cfg.error} />}
      {d && (
        <>
          <Card title="ConfigMaps">
            {d.config_maps.length === 0 ? (
              <Empty>Geen ConfigMaps{namespace ? ` in ${namespace}` : ""}.</Empty>
            ) : (
              <Table>
                <thead className="border-b border-slate-200 dark:border-slate-800">
                  <tr>
                    <th className={thClass}>ConfigMap</th>
                    <th className={thClass}>Sleutels</th>
                    <th className={thClass}>Grootte</th>
                    <th className={thClass}>Leeftijd</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {d.config_maps.map((m) => (
                    <tr key={`${m.namespace}/${m.name}`}>
                      <td className={tdClass}>
                        <Name id={id} kind="ConfigMap" namespace={m.namespace} name={m.name} />
                      </td>
                      <td className={`${tdClass} font-mono text-xs break-all`}>{m.keys.join(", ") || "–"}</td>
                      <td className={`${tdClass} whitespace-nowrap`}>{formatBytes(m.bytes)}</td>
                      <td className={`${tdClass} text-slate-500`} title={m.created_at}>
                        {age(m.created_at)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>
          <Card title="Secrets">
            {d.secrets.length === 0 ? (
              <Empty>Geen Secrets{namespace ? ` in ${namespace}` : ""}.</Empty>
            ) : (
              <Table>
                <thead className="border-b border-slate-200 dark:border-slate-800">
                  <tr>
                    <th className={thClass}>Secret</th>
                    <th className={thClass}>Soort</th>
                    <th className={thClass}>Sleutels</th>
                    <th className={thClass}>Leeftijd</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {d.secrets.map((s) => (
                    <tr key={`${s.namespace}/${s.name}`}>
                      <td className={tdClass}>
                        <Name id={id} kind="Secret" namespace={s.namespace} name={s.name} />
                      </td>
                      <td className={`${tdClass} text-xs`}>{s.type}</td>
                      <td className={`${tdClass} text-xs`}>
                        {s.keys.length === 0
                          ? "–"
                          : s.keys.map((k) => (
                              <div key={k.name} className="break-all">
                                <span className="font-mono">{k.name}</span>{" "}
                                <span className="text-slate-500">({k.bytes} bytes, verborgen)</span>
                              </div>
                            ))}
                      </td>
                      <td className={`${tdClass} text-slate-500`} title={s.created_at}>
                        {age(s.created_at)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>
        </>
      )}
    </div>
  );
}

export function EventsTab({
  id,
  namespace,
  system,
  warnings,
  onWarnings,
}: TabProps & { warnings: boolean; onWarnings: (on: boolean) => void }) {
  const events = useKubeEvents(id, namespace, system, warnings);
  return (
    <div className="space-y-4">
      <Learn
        commands={[
          kubectl(`get events ${nsFlag(namespace)} --sort-by=.lastTimestamp`),
          kubectl(`get events ${nsFlag(namespace)} --field-selector type=Warning`),
        ]}
      >
        <p>
          Kubernetes schrijft een <strong>event</strong> bij alles wat het doet: een pod ingepland, een image gedownload, een
          container herstart. <strong>Warnings</strong> staan bovenaan: daar zie je meestal meteen waarom iets niet werkt, zoals
          een image dat niet bestaat of een node zonder plaats. Events blijven een uur bewaard.
        </p>
      </Learn>
      <label className="inline-flex cursor-pointer items-center gap-2 text-sm text-slate-700 dark:text-slate-300">
        <input type="checkbox" className="size-4" checked={warnings} onChange={(e) => onWarnings(e.target.checked)} />
        Alleen waarschuwingen
      </label>
      {events.isError && <KubeError error={events.error} />}
      {events.data &&
        (events.data.length === 0 ? (
          <Empty>
            Geen {warnings ? "waarschuwingen" : "events"}
            {namespace ? ` in ${namespace}` : ""} in het laatste uur.
          </Empty>
        ) : (
          <Card>
            <EventList clusterId={id} events={events.data} />
            {events.data.length >= 300 && <p className="mt-3 text-xs text-slate-500">De 300 belangrijkste en nieuwste.</p>}
          </Card>
        ))}
    </div>
  );
}

// typeKey is groep/versie/soort, zoals apps/v1/deployments; de kerngroep
// is leeg.
export function typeKey(t: Pick<KubeResourceType, "group" | "version" | "resource">) {
  return `${t.group}/${t.version}/${t.resource}`;
}

export function AllTab({ id, namespace, system, type, onType }: TabProps & { type: string; onType: (key: string) => void }) {
  const types = useKubeResources(id);
  const learning = useKubeLearning();
  const chosen = types.data?.find((t) => typeKey(t) === type) ?? types.data?.find((t) => t.resource === "pods" && t.group === "");
  const objects = useKubeObjects(id, chosen, namespace, system);
  const groups = new Map<string, KubeResourceType[]>();
  for (const t of types.data ?? []) {
    const g = t.group || "kern (v1)";
    groups.set(g, [...(groups.get(g) ?? []), t]);
  }
  return (
    <div className="space-y-4">
      <Learn commands={[kubectl("api-resources"), kubectl(`get ${chosen?.resource ?? "pods"} ${nsFlag(namespace)}`)]}>
        <p>
          Alles in Kubernetes is een object van een <strong>soort</strong> (kind), en elke soort hoort bij een{" "}
          <strong>API-groep</strong>: Pods in de kerngroep, Deployments in <code>apps</code>. Apps zoals Traefik voegen hun eigen
          soorten toe met een CustomResourceDefinition (CRD), zoals IngressRoute. Hier zie je ze allemaal.
        </p>
      </Learn>
      {types.isError && <KubeError error={types.error} />}
      {types.data && (
        <Select
          className="w-auto max-w-full"
          aria-label="Soort"
          value={chosen ? typeKey(chosen) : ""}
          onChange={(e) => onType(e.target.value)}
        >
          {[...groups.entries()].map(([g, ts]) => (
            <optgroup key={g} label={g}>
              {ts.map((t) => (
                <option key={typeKey(t)} value={typeKey(t)}>
                  {t.kind} ({t.resource})
                </option>
              ))}
            </optgroup>
          ))}
        </Select>
      )}
      {chosen && learning.on && objectHelp[chosen.kind] && (
        <p className="text-sm text-slate-600 dark:text-slate-400">{objectHelp[chosen.kind]}</p>
      )}
      {objects.isError && <KubeError error={objects.error} />}
      {chosen &&
        objects.data &&
        (objects.data.length === 0 ? (
          <Empty>
            Geen {chosen.kind}
            {chosen.namespaced && namespace ? ` in ${namespace}` : ""}.
            {chosen.namespaced && !system && !namespace && " Zet Systeem tonen aan om die van Kubernetes zelf te zien."}
          </Empty>
        ) : (
          <Table>
            <thead className="border-b border-slate-200 dark:border-slate-800">
              <tr>
                <th className={thClass}>Naam</th>
                {chosen.namespaced && <th className={thClass}>Namespace</th>}
                <th className={thClass}>Status</th>
                <th className={thClass}>Leeftijd</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {objects.data.map((o) => (
                <tr key={`${o.namespace}/${o.name}`}>
                  <td className={`${tdClass} font-medium break-all`}>
                    <ObjectLink
                      clusterId={id}
                      objRef={{
                        group: chosen.group,
                        version: chosen.version,
                        resource: chosen.resource,
                        namespace: o.namespace,
                        name: o.name,
                      }}
                    >
                      {o.name}
                    </ObjectLink>
                  </td>
                  {chosen.namespaced && <td className={tdClass}>{o.namespace}</td>}
                  <td className={cx(tdClass, o.status.startsWith("Niet ") && "text-amber-700 dark:text-amber-300")}>
                    {o.status || "–"}
                  </td>
                  <td className={`${tdClass} text-slate-500`} title={o.created_at}>
                    {age(o.created_at)}
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        ))}
    </div>
  );
}
