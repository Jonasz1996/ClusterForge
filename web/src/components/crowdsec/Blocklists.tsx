"use client";

import { useState } from "react";
import { Modal } from "@/components/Modal";
import { ago, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, Field, Input, Select, Textarea } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import {
  everyLabel,
  useAddCrowdSecList,
  useChangeCrowdSecListEntries,
  useCrowdSecAutoRules,
  useCrowdSecListEntries,
  useCrowdSecLists,
  useCrowdSecListVersions,
  useDeleteCrowdSecList,
  usePreviewCrowdSecList,
  useRefreshCrowdSecList,
  useRollbackCrowdSecList,
  useUpdateCrowdSecList,
  type CrowdSecAutoRules,
  type CrowdSecList,
  type CrowdSecListDiff,
  type CrowdSecOverview,
  type CrowdSecPublicSource,
} from "@/lib/crowdsec";

// Centrale blocklists (fase 47): alles lokaal, zonder de CrowdSec-cloud.

const timeFmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "short", timeStyle: "short" });

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : "Er ging iets mis.";
}

const kindLabel: Record<CrowdSecList["kind"], string> = { own: "eigen", auto: "automatisch", public: "publiek" };

export function BlocklistsTab({ o, isAdmin }: { o: CrowdSecOverview; isAdmin: boolean }) {
  const lists = useCrowdSecLists();
  const update = useUpdateCrowdSecList();
  const refresh = useRefreshCrowdSecList();
  const del = useDeleteCrowdSecList();
  const [open, setOpen] = useState<CrowdSecList | null>(null);
  const [adding, setAdding] = useState<CrowdSecList | null>(null);
  const [addPublic, setAddPublic] = useState(false);
  const [removing, setRemoving] = useState<CrowdSecList | null>(null);
  const items = lists.data?.items ?? [];
  const own = items.find((l) => l.kind === "own");
  const old = o.nodes.filter((n) => n.managed && n.protocol < 57).length;
  const err = update.error ?? refresh.error ?? del.error;
  return (
    <div className="space-y-6">
      <Alert kind="info">
        ClusterForge is de centrale server: hij bewaart de lijsten en zet ze met <code>cscli</code> op elke beheerde node (de nftables-bouncer voert ze uit). Er is
        geen CrowdSec-cloud nodig. Privé-, multicast- en eigen netwerken en alles uit de whitelist laat ClusterForge altijd weg, ook als een lijst ze bevat.
      </Alert>
      {old > 0 && (
        <Alert>
          {old} beheerde node(s) hebben een oudere agent en krijgen de lijsten nog niet: Nodes → Agents bijwerken (protocol 57).
        </Alert>
      )}
      {lists.isError && <Alert>De lijsten laden mislukte.</Alert>}
      {err && <Alert>{errText(err)}</Alert>}
      <Card
        title={
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span>Blocklists</span>
            {isAdmin && (
              <span className="flex gap-2">
                {own && (
                  <Button variant="danger" onClick={() => setAdding(own)}>
                    IP&apos;s blokkeren
                  </Button>
                )}
                <Button variant="secondary" onClick={() => setAddPublic(true)}>
                  Publieke lijst toevoegen
                </Button>
              </span>
            )}
          </div>
        }
      >
        <div className="overflow-x-auto">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Lijst</th>
                <th className={thClass}>IP&apos;s en ranges</th>
                <th className={thClass}>Op de nodes</th>
                <th className={thClass}>Bijgewerkt</th>
                <th className={thClass}></th>
              </tr>
            </thead>
            <tbody>
              {items.map((l) => (
                <tr key={l.id} className={l.enabled ? "" : "opacity-60"}>
                  <td className={tdClass}>
                    <button className="font-medium text-brand-700 hover:underline dark:text-brand-300" onClick={() => setOpen(l)}>
                      {l.name}
                    </button>{" "}
                    <Badge tone={l.kind === "public" ? "blue" : l.kind === "auto" ? "amber" : "slate"}>{kindLabel[l.kind]}</Badge>
                    {!l.enabled && <Badge tone="slate">uit</Badge>}
                    {l.pinned && <Badge tone="amber">vastgezet</Badge>}
                    {l.kind === "public" && <span className="block text-xs text-slate-500">{everyLabel(l.refresh_minutes)} ophalen</span>}
                    {l.fetch_error && <span className="block text-xs text-red-600 dark:text-red-400">Ophalen: {l.fetch_error}</span>}
                  </td>
                  <td className={tdClass}>
                    <span className="tabular-nums">{l.count.toLocaleString("nl-BE")}</span>
                    {l.dropped > 0 && <span className="block text-xs text-slate-500">{l.dropped} weggelaten (eigen of speciaal netwerk)</span>}
                  </td>
                  <td className={tdClass}>
                    {l.enabled ? (
                      <Badge tone={l.nodes_synced === l.nodes_wanted ? "green" : "amber"}>
                        {l.nodes_synced}/{l.nodes_wanted}
                      </Badge>
                    ) : (
                      "—"
                    )}
                    {l.failed.slice(0, 3).map((f) => (
                      <span key={f.hostname} className="block text-xs text-amber-700 dark:text-amber-400">
                        {f.hostname}: {f.error}
                      </span>
                    ))}
                  </td>
                  <td className={tdClass}>
                    {ago(l.updated_at)}
                    {l.fetched_at && <span className="block text-xs text-slate-500">opgehaald {ago(l.fetched_at)}</span>}
                  </td>
                  <td className={tdClass}>
                    <div className="flex flex-wrap justify-end gap-1">
                      <Button variant="ghost" onClick={() => setOpen(l)}>
                        Bekijken
                      </Button>
                      {isAdmin && l.kind !== "public" && (
                        <Button variant="ghost" onClick={() => setAdding(l)}>
                          Toevoegen
                        </Button>
                      )}
                      {isAdmin && l.kind === "public" && (
                        <Button variant="ghost" disabled={refresh.isPending} onClick={() => refresh.mutate(l.id)}>
                          Nu ophalen
                        </Button>
                      )}
                      {isAdmin && (
                        <Button variant="ghost" disabled={update.isPending} onClick={() => update.mutate({ id: l.id, body: { enabled: !l.enabled } })}>
                          {l.enabled ? "Uitzetten" : "Aanzetten"}
                        </Button>
                      )}
                      {isAdmin && l.kind === "public" && (
                        <Button variant="ghost" onClick={() => setRemoving(l)}>
                          Weghalen
                        </Button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
      <AutoRulesCard o={o} isAdmin={isAdmin} />
      {open && <ListDialog list={open} isAdmin={isAdmin} onClose={() => setOpen(null)} />}
      {adding && <AddEntriesDialog list={adding} onClose={() => setAdding(null)} />}
      {addPublic && <PublicDialog catalog={lists.data?.catalog ?? []} existing={items} onClose={() => setAddPublic(false)} />}
      {removing && (
        <Modal title={`${removing.name} weghalen`} onClose={() => setRemoving(null)}>
          <p className="text-sm">
            De {removing.count.toLocaleString("nl-BE")} IP&apos;s en ranges van deze lijst gaan bij de volgende ronde van alle nodes. Uitzetten doet hetzelfde maar
            houdt de lijst en haar versies.
          </p>
          <div className="mt-4 flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setRemoving(null)}>
              Annuleren
            </Button>
            <Button variant="danger" disabled={del.isPending} onClick={() => del.mutate(removing.id, { onSuccess: () => setRemoving(null) })}>
              Weghalen
            </Button>
          </div>
        </Modal>
      )}
    </div>
  );
}

/** Het voorbeeld van een wijziging. */
export function DiffView({ d, verb }: { d: CrowdSecListDiff; verb: string }) {
  const more = (shown: number, total: number) => (total > shown ? ` … en nog ${(total - shown).toLocaleString("nl-BE")}` : "");
  return (
    <div className="space-y-2 rounded-md border border-slate-200 p-3 text-sm dark:border-slate-700">
      <p>
        {d.applied ? "Gedaan" : "Voorbeeld"}: <strong>{d.added_count}</strong> erbij, <strong>{d.removed_count}</strong> weg
        {d.updated_count > 0 && (
          <>
            , <strong>{d.updated_count}</strong> gewijzigd
          </>
        )}
        . Daarna {d.count.toLocaleString("nl-BE")} in de lijst, {verb} op {d.nodes} node(s).
      </p>
      {d.added.length > 0 && (
        <p className="font-mono text-xs text-red-700 dark:text-red-300">
          + {d.added.join(", ")}
          {more(d.added.length, d.added_count)}
        </p>
      )}
      {d.removed.length > 0 && (
        <p className="font-mono text-xs text-emerald-700 dark:text-emerald-300">
          − {d.removed.join(", ")}
          {more(d.removed.length, d.removed_count)}
        </p>
      )}
      {d.refused_count > 0 && (
        <Alert kind="info">
          {d.refused_count} nooit geblokkeerd:{" "}
          {d.refused.slice(0, 8).map((r) => (
            <span key={r.value} className="block font-mono text-xs">
              {r.value}: {r.why}
            </span>
          ))}
          {more(Math.min(8, d.refused.length), d.refused_count)}
        </Alert>
      )}
    </div>
  );
}

const durations = [
  { v: 0, label: "Voor altijd" },
  { v: 24, label: "24 uur" },
  { v: 168, label: "7 dagen" },
  { v: 720, label: "30 dagen" },
  { v: 2160, label: "90 dagen" },
  { v: 8760, label: "1 jaar" },
];

function AddEntriesDialog({ list, onClose }: { list: CrowdSecList; onClose: () => void }) {
  const change = useChangeCrowdSecListEntries();
  const [text, setText] = useState("");
  const [reason, setReason] = useState("");
  const [hours, setHours] = useState(0);
  const [diff, setDiff] = useState<CrowdSecListDiff | null>(null);
  const values = text
    .split(/[\s,;]+/)
    .map((v) => v.trim())
    .filter(Boolean);
  const body = (dry: boolean) => ({
    id: list.id,
    body: { add: values.map((value) => ({ value, reason: reason.trim() || undefined, expires_hours: hours || undefined })), dry_run: dry },
  });
  return (
    <Modal title={`Blokkeren in ${list.name}`} onClose={onClose}>
      <div className="space-y-3">
        <Field label="IP's en ranges" htmlFor="bl-values" hint="Eén per regel (of gescheiden door spaties of komma's), zoals 203.0.113.7 of 198.51.100.0/24.">
          <Textarea
            id="bl-values"
            rows={6}
            className="font-mono"
            value={text}
            autoFocus
            onChange={(e) => {
              setText(e.target.value);
              setDiff(null);
            }}
          />
        </Field>
        <Field label="Reden" htmlFor="bl-reason">
          <Input id="bl-reason" value={reason} onChange={(e) => setReason(e.target.value)} />
        </Field>
        <Field label="Hoe lang" htmlFor="bl-hours">
          <Select id="bl-hours" value={hours} onChange={(e) => setHours(Number(e.target.value))}>
            {durations.map((d) => (
              <option key={d.v} value={d.v}>
                {d.label}
              </option>
            ))}
          </Select>
        </Field>
        {diff && <DiffView d={diff} verb="geblokkeerd" />}
        {change.isError && <Alert>{errText(change.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            {diff?.applied ? "Sluiten" : "Annuleren"}
          </Button>
          {!diff?.applied && (
            <Button variant="secondary" disabled={values.length === 0 || change.isPending} onClick={() => change.mutate(body(true), { onSuccess: setDiff })}>
              Voorbeeld
            </Button>
          )}
          {!diff?.applied && (
            <Button variant="danger" disabled={!diff || diff.added_count + diff.updated_count === 0 || change.isPending} onClick={() => change.mutate(body(false), { onSuccess: setDiff })}>
              Blokkeren op alle nodes
            </Button>
          )}
        </div>
      </div>
    </Modal>
  );
}

function PublicDialog({ catalog, existing, onClose }: { catalog: CrowdSecPublicSource[]; existing: CrowdSecList[]; onClose: () => void }) {
  const preview = usePreviewCrowdSecList();
  const add = useAddCrowdSecList();
  const free = catalog.filter((c) => !existing.some((l) => l.source === c.key));
  const [source, setSource] = useState(free[0]?.key ?? "custom");
  const [url, setUrl] = useState("");
  const [refresh, setRefresh] = useState(0);
  const src = catalog.find((c) => c.key === source);
  const min = src?.min_minutes ?? 60;
  const every = refresh || src?.default_minutes || 1440;
  const body = { source, url: source === "custom" ? url.trim() : undefined, refresh_minutes: every };
  const options = [60, 180, 360, 720, 1440, 2880].filter((m) => m >= min);
  return (
    <Modal title="Publieke lijst toevoegen" onClose={onClose}>
      <div className="space-y-3">
        <Field label="Lijst" htmlFor="pl-source">
          <Select
            id="pl-source"
            value={source}
            onChange={(e) => {
              setSource(e.target.value);
              setRefresh(0);
              preview.reset();
            }}
          >
            {free.map((c) => (
              <option key={c.key} value={c.key}>
                {c.name}
              </option>
            ))}
            <option value="custom">Eigen URL…</option>
          </Select>
        </Field>
        {src && <p className="text-xs text-slate-500">{src.info}</p>}
        {source === "custom" && (
          <Field label="URL" htmlFor="pl-url" hint="https, een tekstbestand met één IP of range per regel; # en ; zijn commentaar. Respecteer de voorwaarden van de bron.">
            <Input id="pl-url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://…" />
          </Field>
        )}
        <Field label="Ophalen" htmlFor="pl-refresh" hint={src ? `De bron laat hoogstens ${everyLabel(min)} toe.` : undefined}>
          <Select id="pl-refresh" value={every} onChange={(e) => setRefresh(Number(e.target.value))}>
            {[...new Set([...options, every])]
              .sort((a, b) => a - b)
              .map((m) => (
                <option key={m} value={m}>
                  {everyLabel(m)}
                </option>
              ))}
          </Select>
        </Field>
        {preview.data && <DiffView d={preview.data} verb="geblokkeerd" />}
        {(preview.isError || add.isError) && <Alert>{errText(preview.error ?? add.error)}</Alert>}
        {add.isSuccess && <Alert kind="success">Aangezet. De lijst gaat binnen een minuut naar de nodes.</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            {add.isSuccess ? "Sluiten" : "Annuleren"}
          </Button>
          {!add.isSuccess && (
            <>
              <Button variant="secondary" disabled={preview.isPending || (source === "custom" && !url.trim())} onClick={() => preview.mutate(body)}>
                {preview.isPending ? "Ophalen…" : "Voorbeeld"}
              </Button>
              <Button disabled={!preview.data || add.isPending} onClick={() => add.mutate(body)}>
                Aanzetten
              </Button>
            </>
          )}
        </div>
      </div>
    </Modal>
  );
}

function ListDialog({ list, isAdmin, onClose }: { list: CrowdSecList; isAdmin: boolean; onClose: () => void }) {
  const [view, setView] = useState<"waarden" | "versies">("waarden");
  return (
    <Modal title={list.name} onClose={onClose} wide>
      <div className="space-y-3">
        {list.info && <p className="text-xs text-slate-500">{list.info}</p>}
        {list.url && <p className="break-all font-mono text-xs text-slate-500">{list.url}</p>}
        <div className="flex gap-2">
          <Button variant={view === "waarden" ? "primary" : "secondary"} onClick={() => setView("waarden")}>
            Waarden
          </Button>
          <Button variant={view === "versies" ? "primary" : "secondary"} onClick={() => setView("versies")}>
            Versies en terugdraaien
          </Button>
        </div>
        {view === "waarden" ? <EntriesView list={list} isAdmin={isAdmin} /> : <VersionsView list={list} isAdmin={isAdmin} />}
      </div>
    </Modal>
  );
}

function EntriesView({ list, isAdmin }: { list: CrowdSecList; isAdmin: boolean }) {
  const [q, setQ] = useState("");
  const entries = useCrowdSecListEntries(list.id, q);
  const change = useChangeCrowdSecListEntries();
  const [diff, setDiff] = useState<CrowdSecListDiff | null>(null);
  const editable = isAdmin && list.kind !== "public";
  return (
    <div className="space-y-2">
      <Input placeholder="Zoek op IP of reden" value={q} onChange={(e) => setQ(e.target.value)} />
      {diff && <DiffView d={diff} verb="vrijgegeven" />}
      {change.isError && <Alert>{errText(change.error)}</Alert>}
      <div className="max-h-[50vh] overflow-auto">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>IP of range</th>
              <th className={thClass}>Reden</th>
              <th className={thClass}>Tot</th>
              <th className={thClass}>Sinds</th>
              {editable && <th className={thClass}></th>}
            </tr>
          </thead>
          <tbody>
            {(entries.data?.items ?? []).map((e) => (
              <tr key={e.value}>
                <td className={`${tdClass} font-mono`}>{e.value}</td>
                <td className={tdClass}>{e.reason || "—"}</td>
                <td className={tdClass}>{e.expires_at ? timeFmt.format(new Date(e.expires_at)) : "altijd"}</td>
                <td className={tdClass}>
                  {ago(e.created_at)}
                  {e.added_by && <span className="block text-xs text-slate-500">{e.added_by}</span>}
                </td>
                {editable && (
                  <td className={tdClass}>
                    <Button
                      variant="ghost"
                      disabled={change.isPending}
                      onClick={() => change.mutate({ id: list.id, body: { remove: [e.value] } }, { onSuccess: setDiff })}
                    >
                      Vrijgeven
                    </Button>
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {entries.data && (
        <p className="text-xs text-slate-500">
          {entries.data.items.length} van {entries.data.total.toLocaleString("nl-BE")} getoond.
          {list.kind === "public" && " Een publieke lijst komt van de bron; zet een uitzondering in de whitelist (Instellingen)."}
        </p>
      )}
    </div>
  );
}

function VersionsView({ list, isAdmin }: { list: CrowdSecList; isAdmin: boolean }) {
  const versions = useCrowdSecListVersions(list.id);
  const rollback = useRollbackCrowdSecList();
  const [picked, setPicked] = useState<number | null>(null);
  const [diff, setDiff] = useState<CrowdSecListDiff | null>(null);
  return (
    <div className="space-y-2">
      <p className="text-xs text-slate-500">
        Elke wijziging bewaart de inhoud. Terugdraaien zet die inhoud terug en brengt ze meteen naar de nodes
        {list.kind === "public" ? "; een publieke lijst wordt dan vastgezet tot je Nu ophalen kiest." : "."}
      </p>
      {diff && <DiffView d={diff} verb="geldig" />}
      {rollback.isError && <Alert>{errText(rollback.error)}</Alert>}
      {picked !== null && diff && !diff.applied && isAdmin && (
        <div className="flex justify-end gap-2">
          <Button
            variant="secondary"
            onClick={() => {
              setPicked(null);
              setDiff(null);
            }}
          >
            Annuleren
          </Button>
          <Button disabled={rollback.isPending} onClick={() => rollback.mutate({ id: list.id, version: picked, dryRun: false }, { onSuccess: setDiff })}>
            Terugdraaien
          </Button>
        </div>
      )}
      <table className={tableClass}>
        <thead>
          <tr>
            <th className={thClass}>Wanneer</th>
            <th className={thClass}>Wat</th>
            <th className={thClass}>Aantal</th>
            <th className={thClass}>Door</th>
            {isAdmin && <th className={thClass}></th>}
          </tr>
        </thead>
        <tbody>
          {(versions.data ?? []).map((v, i) => (
            <tr key={v.id} className={picked === v.id ? "bg-brand-50 dark:bg-slate-800" : ""}>
              <td className={tdClass}>{timeFmt.format(new Date(v.created_at))}</td>
              <td className={tdClass}>{v.note}</td>
              <td className={`${tdClass} tabular-nums`}>{v.count.toLocaleString("nl-BE")}</td>
              <td className={tdClass}>{v.actor || "ClusterForge"}</td>
              {isAdmin && (
                <td className={tdClass}>
                  {i > 0 && (
                    <Button
                      variant="ghost"
                      disabled={rollback.isPending}
                      onClick={() => {
                        setPicked(v.id);
                        rollback.mutate({ id: list.id, version: v.id, dryRun: true }, { onSuccess: setDiff });
                      }}
                    >
                      Voorbeeld terugdraaien
                    </Button>
                  )}
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
      {versions.data?.length === 0 && <p className="text-sm text-slate-500">Nog geen versies.</p>}
    </div>
  );
}

function num(v: string) {
  const n = Number(v);
  return Number.isFinite(n) ? Math.round(n) : 0;
}

function AutoRulesCard({ o, isAdmin }: { o: CrowdSecOverview; isAdmin: boolean }) {
  const save = useCrowdSecAutoRules();
  const [r, setR] = useState<CrowdSecAutoRules>(o.settings.auto_rules);
  const set = (k: keyof CrowdSecAutoRules) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setR({ ...r, [k]: e.target.type === "checkbox" ? e.target.checked : num(e.target.value) });
  return (
    <Card title="Automatisch blokkeren over alle nodes">
      <div className="max-w-2xl space-y-3 text-sm">
        <p className="text-xs text-slate-500">
          Naast de korte bans van CrowdSec zelf zet ClusterForge een aanvaller in de lijst &quot;Automatisch geblokkeerd&quot;, en daarmee op elke node, ook op nodes die
          later bijkomen. Vrijgeven of Deblokkeren telt als beslissing: oudere alerts van dat IP tellen dan niet meer mee.
        </p>
        <label className="flex items-center gap-2">
          <input type="checkbox" checked={r.enabled} disabled={!isAdmin} onChange={set("enabled")} />
          <span className="font-medium">Aan</span>
        </label>
        <div className="flex flex-wrap items-center gap-2">
          Een IP met alerts op
          <Input type="number" className="w-20" value={r.multi_nodes} disabled={!isAdmin} onChange={set("multi_nodes")} />
          verschillende nodes binnen
          <Input type="number" className="w-24" value={r.multi_window_minutes} disabled={!isAdmin} onChange={set("multi_window_minutes")} />
          minuten (0 nodes is uit).
        </div>
        <div className="flex flex-wrap items-center gap-2">
          Een IP met
          <Input type="number" className="w-20" value={r.repeat_alerts} disabled={!isAdmin} onChange={set("repeat_alerts")} />
          alerts binnen
          <Input type="number" className="w-20" value={r.repeat_window_hours} disabled={!isAdmin} onChange={set("repeat_window_hours")} />
          uur (0 alerts is uit).
        </div>
        <div className="flex flex-wrap items-center gap-2">
          Blokkeren voor
          <Input type="number" className="w-24" value={r.duration_hours} disabled={!isAdmin} onChange={set("duration_hours")} />
          uur.
        </div>
        <label className="flex flex-wrap items-center gap-2">
          <input type="checkbox" checked={r.escalate} disabled={!isAdmin} onChange={set("escalate")} />
          Bij elke herhaling twee keer zo lang, tot
          <Input type="number" className="w-24" value={r.max_hours} disabled={!isAdmin || !r.escalate} onChange={set("max_hours")} />
          uur.
        </label>
        {save.isError && <Alert>{errText(save.error)}</Alert>}
        {save.isSuccess && <Alert kind="success">Bewaard. De regels gelden vanaf de volgende minuut.</Alert>}
        {isAdmin && (
          <Button disabled={save.isPending} onClick={() => save.mutate(r)}>
            Bewaren
          </Button>
        )}
      </div>
    </Card>
  );
}
