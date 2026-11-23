"use client";

import { useMemo, useState } from "react";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ActionModal } from "@/components/publish/ActionModal";
import { AutoPublish } from "@/components/publish/AutoPublish";
import { HostEditor } from "@/components/publish/HostEditor";
import { ThemeEditor } from "@/components/publish/ThemeEditor";
import { AKBadge, Empty, StatusBar, ago } from "@/components/publish/bits";
import { Alert, Badge, Button, Card, Field, Input, PageHeader, Select } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { fullName, usePublishOverview, type PublishHost, type PublishService } from "@/lib/publish";

export default function PublishServicesPage() {
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const q = usePublishOverview();
  const ov = q.data;
  const [filter, setFilter] = useState("");
  const [all, setAll] = useState(false);
  const [internal, setInternal] = useState(false);
  const [open, setOpen] = useState<PublishService | null>(null);
  const [edit, setEdit] = useState<PublishHost | null>(null);
  const [theme, setTheme] = useState(false);

  const rows = useMemo(() => {
    const f = filter.trim().toLowerCase();
    return (ov?.services ?? [])
      .filter((s) => all || s.web)
      .filter((s) => internal || !s.internal || s.published.length > 0)
      .filter(
        (s) =>
          !f ||
          `${s.name} ${s.title ?? ""} ${s.image ?? ""} ${s.cluster_name ?? ""} ${s.nodes.map((n) => n.hostname).join(" ")} ${s.port}`
            .toLowerCase()
            .includes(f),
      );
  }, [ov, filter, all, internal]);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Diensten publiceren"
        description="Wat de agents op elke node zien draaien (containers en services met hun poort), met het VIP als de dienst op alle nodes van een cluster draait. Eén klik maakt een proxy host in Nginx Proxy Manager onder je hoofddomein, met SSL en zo nodig achter Authentik."
        actions={
          admin &&
          ov?.npm.linked && (
            <Button variant="secondary" onClick={() => setTheme(true)}>
              Sjabloon (Advanced en css)
            </Button>
          )
        }
      />
      {q.isError && <Alert>{(q.error as Error).message}</Alert>}
      {ov && (
        <>
          <StatusBar ov={ov} admin={admin} />
          {ov.npm.linked && <AutoPublish ov={ov} admin={admin} />}
          <Card title={`Diensten · ${rows.length}`}>
            <div className="mb-3 flex flex-wrap items-center gap-3 text-sm">
              <Input placeholder="Zoeken" value={filter} onChange={(e) => setFilter(e.target.value)} className="max-w-xs" />
              <label className="flex items-center gap-2">
                <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} />
                Ook poorten zonder webpagina
              </label>
              <label
                className="flex items-center gap-2"
                title="HAProxy-frontends en -statistieken, Patroni, SPICE-proxy, Tailscale, CrowdSec, NPM zelf, ... ClusterForge beheert ze gewoon verder."
              >
                <input type="checkbox" checked={internal} onChange={(e) => setInternal(e.target.checked)} />
                Ook interne tonen{ov.counts.internal ? ` (${ov.counts.internal})` : ""}
              </label>
            </div>
            {rows.length === 0 ? (
              <Empty>
                {ov.services.length === 0
                  ? "Nog niets gevonden. De agents (protocol 51 of nieuwer) lezen elke paar minuten welke diensten op welke poort luisteren."
                  : "Niets dat bij de zoekopdracht past."}
              </Empty>
            ) : (
              <div className="overflow-x-auto">
                <table className={tableClass}>
                  <thead>
                    <tr>
                      <th className={thClass}>Dienst</th>
                      <th className={thClass}>Waar</th>
                      <th className={thClass}>Doel voor NPM</th>
                      <th className={thClass}>Gepubliceerd</th>
                      {admin && <th className={thClass} />}
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                    {rows.map((s) => (
                      <tr key={s.key}>
                        <td className={tdClass}>
                          <div className="font-medium">
                            {s.title || s.name}
                            {s.internal && (
                              <span className="ml-2" title={s.internal_why}>
                                <Badge tone="slate">intern</Badge>
                              </span>
                            )}
                          </div>
                          <div className="text-xs text-slate-500">
                            {[s.container && s.container !== s.name ? s.container : "", s.image, s.unit, s.internal ? s.internal_why : ""]
                              .filter(Boolean)
                              .join(" · ")}
                          </div>
                        </td>
                        <td className={tdClass}>
                          {s.cluster_name && <div>{s.cluster_name}</div>}
                          <div className="text-xs text-slate-500">{s.on_all ? "op alle nodes" : s.nodes.map((n) => n.hostname).join(", ")}</div>
                        </td>
                        <td className={`${tdClass} whitespace-nowrap`}>
                          {s.kind || s.proto}://{s.target}:{s.port}
                          {s.target_vip && (
                            <span className="ml-2">
                              <Badge tone="blue">VIP</Badge>
                            </span>
                          )}
                          {s.status ? <div className="text-xs text-slate-500">antwoordt {s.status}</div> : null}
                        </td>
                        <td className={tdClass}>
                          {s.published.length === 0 ? (
                            <span className="text-slate-400">nee</span>
                          ) : (
                            <ul className="space-y-1">
                              {s.published.map((p) => (
                                <li key={p.host_id} className="flex flex-wrap items-center gap-2">
                                  <a href={`https://${p.domain}`} target="_blank" rel="noreferrer" className="underline">
                                    {p.domain}
                                  </a>
                                  <AKBadge state={p.authentik} />
                                  {admin && ov.npm.linked && ov.hosts.some((h) => h.id === p.host_id) && (
                                    <button
                                      type="button"
                                      className="text-xs text-brand-700 underline dark:text-sky-300"
                                      onClick={() => setEdit(ov.hosts.find((h) => h.id === p.host_id) ?? null)}
                                    >
                                      bewerken
                                    </button>
                                  )}
                                </li>
                              ))}
                            </ul>
                          )}
                        </td>
                        {admin && (
                          <td className={`${tdClass} text-xs whitespace-nowrap`}>
                            {s.web && ov.npm.linked && (!s.internal || internal) && (
                              <button type="button" className="text-brand-700 underline dark:text-sky-300" onClick={() => setOpen(s)}>
                                publiceren
                              </button>
                            )}
                          </td>
                        )}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
          <Card title="Ontdekking per node">
            <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
              {ov.nodes.map((n) => (
                <li key={n.id} className="flex flex-wrap items-center justify-between gap-2 py-1.5">
                  <span>
                    {n.hostname}
                    {n.cluster_name && <span className="ml-2 text-xs text-slate-500">{n.cluster_name}</span>}
                  </span>
                  <span className="flex items-center gap-2 text-xs">
                    {n.ready ? (
                      <span className="text-slate-500">{n.ready}</span>
                    ) : n.error ? (
                      <Badge tone="red">{n.error}</Badge>
                    ) : (
                      <span className="text-slate-500">
                        {n.services} diensten · {ago(n.checked_at)}
                      </span>
                    )}
                  </span>
                </li>
              ))}
            </ul>
          </Card>
        </>
      )}
      {open && ov && <PublishModal s={open} domain={ov.settings.domain} hasAK={ov.authentik.linked} onClose={() => setOpen(null)} />}
      {edit && ov && <HostEditor host={edit} ov={ov} onClose={() => setEdit(null)} />}
      {theme && ov && <ThemeEditor ov={ov} onClose={() => setTheme(false)} />}
    </div>
  );
}

function PublishModal({ s, domain, hasAK, onClose }: { s: PublishService; domain: string; hasAK: boolean; onClose: () => void }) {
  const [label, setLabel] = useState(s.label);
  const [ak, setAK] = useState(hasAK && s.authentik_default);
  const [ssl, setSSL] = useState("auto");
  // Het plan volgt de velden pas na een korte pauze in het typen.
  const [applied, setApplied] = useState({
    label: s.label,
    ak: hasAK && s.authentik_default,
    ssl: "auto",
  });
  const dirty = applied.label !== label || applied.ak !== ak || applied.ssl !== ssl;
  return (
    <ActionModal
      title={`${s.title || s.name} publiceren`}
      onClose={onClose}
      blocked={dirty}
      action={{
        action: "publish",
        service_key: s.key,
        label: applied.label,
        authentik: applied.ak,
        ssl: applied.ssl,
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Naam" htmlFor="pub-label" hint={label ? fullName(label, domain) : "subdomein of volledige naam"}>
          <Input id="pub-label" value={label} onChange={(e) => setLabel(e.target.value)} autoComplete="off" />
        </Field>
        <Field label="SSL" htmlFor="pub-ssl">
          <Select id="pub-ssl" value={ssl} onChange={(e) => setSSL(e.target.value)}>
            <option value="auto">Passend certificaat, anders Let&apos;s Encrypt</option>
            <option value="none">Geen (alleen http)</option>
          </Select>
        </Field>
      </div>
      <label className="flex items-center gap-2">
        <input type="checkbox" checked={ak} disabled={!hasAK} onChange={(e) => setAK(e.target.checked)} />
        Achter Authentik{!hasAK && " (Authentik is niet gekoppeld)"}
        {hasAK && !s.authentik_default && <span className="text-xs text-slate-500">standaard uit: deze app heeft een eigen login</span>}
      </label>
      {dirty && (
        <button type="button" className="text-brand-700 underline dark:text-sky-300" onClick={() => setApplied({ label, ak, ssl })}>
          Plan bijwerken
        </button>
      )}
    </ActionModal>
  );
}
