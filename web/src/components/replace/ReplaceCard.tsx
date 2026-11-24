"use client";

import Link from "next/link";
import { useState } from "react";
import { Modal } from "@/components/Modal";
import { ActionDialog } from "@/components/opnsense/bits";
import { ActionModal } from "@/components/publish/ActionModal";
import { Alert, Badge, Button, Card, Field, Input, Select } from "@/components/ui";
import { useNodes, type ClusterDetail } from "@/lib/inventory";
import { useOpnAdviceApply } from "@/lib/opnsense";
import { usePublishSettings, type PublishAction } from "@/lib/publish";
import type { Job } from "@/lib/proxmox";
import {
  useCleanupReplacement,
  useCreateReplacement,
  useKeepReplacement,
  useReplacements,
  useVIPAdvice,
  type Replacement,
  type VIPAdvice,
} from "@/lib/replace";

const reasonText: Record<Replacement["reason"], string> = {
  overname: "overgenomen",
  clusterforge: "ClusterForge verhuisd",
  vervangen: "vervangen",
};

const stateTone: Record<Replacement["state"], "green" | "amber" | "slate" | "blue"> = {
  actief: "blue",
  opruimen: "amber",
  opgeruimd: "slate",
  gehouden: "green",
};

function date(iso: string) {
  return new Date(iso).toLocaleDateString("nl-BE", { day: "numeric", month: "short" });
}

function JobLink({ job }: { job: Job | null }) {
  if (!job) return null;
  return (
    <Alert kind="success">
      Taak gestart:{" "}
      <Link href={`/taken/detail?id=${job.id}`} className="underline">
        bekijken
      </Link>
    </Alert>
  );
}

// ReplaceCard (fase 48) toont bij een cluster met een VIP: wat het vervangt
// (losse installatie of de oude ClusterForge-server), de voorstellen naar het
// VIP (firewall, port forwards, proxy hosts) met voorbeeld en één klik, en na
// de termijn het opruimen van de oude installatie (eerst een kopie).
export function ReplaceCard({ c, isAdmin }: { c: ClusterDetail; isAdmin: boolean }) {
  const reps = useReplacements(c.id);
  const advice = useVIPAdvice(c.id, c.vips.length > 0);
  const [start, setStart] = useState(false);
  const [job, setJob] = useState<Job | null>(null);
  if (c.vips.length === 0) return null;
  const list = reps.data?.items ?? [];
  const a = advice.data;
  const proposals = (a?.firewall.reduce((n, f) => n + f.items.length, 0) ?? 0) + (a?.hosts.length ?? 0);
  return (
    <div id="vervangen">
      <Card title="Vervangen en VIP">
        <div className="space-y-4 text-sm">
          <p className="text-slate-600 dark:text-slate-400">
            Vervangt dit cluster een losse installatie? Dan neemt ClusterForge de diensten over (het overnamescript van de template: oude
            diensten stil, sleutel en data mee), zet het proxy hosts die naar het oude adres wijzen op het VIP, en stelt het regels en port
            forwards voor. Na een paar dagen zonder problemen vraagt het of de oude installatie weg mag (eerst een kopie).
          </p>
          <JobLink job={job} />
          {list.length > 0 && (
            <ul className="divide-y divide-slate-100 dark:divide-slate-800">
              {list.map((r) => (
                <ReplacementRow key={r.id} r={r} isAdmin={isAdmin} onJob={setJob} />
              ))}
            </ul>
          )}
          {isAdmin && (
            <Button variant="secondary" onClick={() => setStart(true)}>
              Losse installatie vervangen…
            </Button>
          )}
          <Proposals c={c} a={a} isAdmin={isAdmin} loading={advice.isLoading} count={proposals} onJob={setJob} />
        </div>
      </Card>
      {start && <StartDialog c={c} onClose={() => setStart(false)} onJob={setJob} />}
    </div>
  );
}

function ReplacementRow({ r, isAdmin, onJob }: { r: Replacement; isAdmin: boolean; onJob: (j: Job) => void }) {
  const keep = useKeepReplacement();
  const [cleanup, setCleanup] = useState(false);
  const name = r.old_hostname || r.old_address;
  return (
    <li className="flex flex-wrap items-start justify-between gap-2 py-2">
      <div>
        <div>
          <Badge tone={stateTone[r.state]}>{r.state}</Badge> <span className="font-medium">{name}</span>{" "}
          <span className="text-slate-500">
            ({r.old_address} → {r.vip}) · {reasonText[r.reason]} op {date(r.created_at)}
          </span>
        </div>
        {r.state === "actief" && (
          <div className="text-xs text-slate-500">
            {r.due ? "Klaar om op te ruimen." : `Opruimen voorgesteld vanaf ${date(r.due_at)} (na ${r.keep_days} dagen zonder problemen).`}
          </div>
        )}
        {r.note && <div className="text-xs text-red-600 dark:text-red-400">{r.note}</div>}
        {r.cleanup_job && (
          <Link href={`/taken/detail?id=${r.cleanup_job}`} className="text-xs underline">
            taak opruimen
          </Link>
        )}
      </div>
      {isAdmin && (r.state === "actief" || r.state === "gehouden") && (
        <div className="flex flex-wrap items-center gap-3 text-xs">
          {r.old_node_id && (
            <button type="button" className="text-brand-700 underline dark:text-sky-300" onClick={() => setCleanup(true)}>
              opruimen…
            </button>
          )}
          {r.state === "actief" ? (
            <button type="button" className="underline" onClick={() => keep.mutate({ id: r.id, keep_days: r.keep_days, keep: true })}>
              houden (niet meer vragen)
            </button>
          ) : (
            <button type="button" className="underline" onClick={() => keep.mutate({ id: r.id, keep_days: r.keep_days, keep: false })}>
              toch opruimen voorstellen
            </button>
          )}
          {r.state === "actief" && (
            <label className="flex items-center gap-1">
              na
              <Select
                value={String(r.keep_days)}
                onChange={(e) => keep.mutate({ id: r.id, keep_days: Number(e.target.value), keep: false })}
                aria-label="Dagen"
                className="py-0 text-xs"
              >
                {[...new Set([3, 7, 14, 30, 60, r.keep_days])]
                  .sort((x, y) => x - y)
                  .map((d) => (
                    <option key={d} value={d}>
                      {d} dagen
                    </option>
                  ))}
              </Select>
            </label>
          )}
        </div>
      )}
      {cleanup && <CleanupDialog r={r} onClose={() => setCleanup(false)} onJob={onJob} />}
    </li>
  );
}

function CleanupDialog({ r, onClose, onJob }: { r: Replacement; onClose: () => void; onJob: (j: Job) => void }) {
  const run = useCleanupReplacement();
  const [typed, setTyped] = useState("");
  const [storage, setStorage] = useState("");
  const name = r.old_hostname || r.old_address;
  return (
    <Modal title={`${name} opruimen`} onClose={onClose}>
      <form
        className="space-y-4 text-sm"
        onSubmit={async (e) => {
          e.preventDefault();
          try {
            const j = await run.mutateAsync({ id: r.id, confirm: typed.trim(), storage: storage.trim() || undefined });
            onJob(j);
            onClose();
          } catch {
            /* de fout staat hieronder */
          }
        }}
      >
        <ol className="list-decimal space-y-1 pl-5">
          <li>
            Kopie vooraf:{" "}
            {r.guest ? "een vzdump van de gast (snapshot, zstd)." : "een tar van /etc, /opt, /srv, /root en de Docker-volumes in /var/backups op de machine."}
          </li>
          <li>
            {r.guest
              ? "De gast afsluiten en niet meer automatisch laten starten. Hij blijft staan; weggooien doe je zelf bij Proxmox → Gasten."
              : "Een losse machine zet ClusterForge niet zelf uit; dat doe je wanneer het past."}
          </li>
          <li>De node uit dienst zetten bij Nodes.</li>
        </ol>
        {!r.guest && !r.old_online && <Alert>De agent van {name} is offline: zonder kopie ruimt ClusterForge niets op.</Alert>}
        {r.guest && (
          <Field label="Storage voor de kopie" htmlFor="rc-storage" hint="Leeg: de standaard van de Proxmox-host (vzdump.conf, meestal local).">
            <Input id="rc-storage" value={storage} onChange={(e) => setStorage(e.target.value)} placeholder="pbs of local" />
          </Field>
        )}
        <Field label={`Tik ${name} over`} htmlFor="rc-confirm">
          <Input id="rc-confirm" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />
        </Field>
        {run.isError && <Alert>{(run.error as Error).message}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" variant="danger" disabled={run.isPending || (typed.trim() !== name && typed.trim() !== r.old_address)}>
            Kopie maken en opruimen
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function StartDialog({ c, onClose, onJob }: { c: ClusterDetail; onClose: () => void; onJob: (j: Job) => void }) {
  const nodes = useNodes();
  const create = useCreateReplacement();
  const [node, setNode] = useState("");
  const [addr, setAddr] = useState("");
  const [days, setDays] = useState("14");
  const [takeover, setTakeover] = useState(true);
  const loose = (nodes.data ?? []).filter((n) => n.cluster_id !== c.id && n.lifecycle !== "decommissioned");
  return (
    <Modal title={`Losse installatie vervangen door ${c.name}`} onClose={onClose}>
      <form
        className="space-y-4 text-sm"
        onSubmit={async (e) => {
          e.preventDefault();
          try {
            const res = await create.mutateAsync({
              cluster_id: c.id,
              old_node_id: node || undefined,
              old_address: node ? undefined : addr.trim(),
              keep_days: Number(days),
              takeover,
            });
            if (res.job) onJob(res.job);
            onClose();
          } catch {
            /* de fout staat hieronder */
          }
        }}
      >
        <Field label="Oude installatie" htmlFor="rs-node" hint="De node met een agent waarop de dienst nu los draait. Zonder agent: geef het IP.">
          <Select id="rs-node" value={node} onChange={(e) => setNode(e.target.value)}>
            <option value="">Geen node, ik geef het IP</option>
            {loose.map((n) => (
              <option key={n.id} value={n.id}>
                {n.hostname}
                {n.primary_ip ? ` (${n.primary_ip})` : ""}
              </option>
            ))}
          </Select>
        </Field>
        {!node && (
          <Field label="IP van de oude installatie" htmlFor="rs-addr">
            <Input id="rs-addr" value={addr} onChange={(e) => setAddr(e.target.value)} placeholder="192.168.10.20" />
          </Field>
        )}
        <label className="flex items-start gap-2">
          <input type="checkbox" className="mt-1" checked={takeover} onChange={(e) => setTakeover(e.target.checked)} />
          <span>
            Nu overnemen: op de VIP-node het overnamescript van de template draaien (oude diensten stil, sleutel en data mee), daarna de
            proxy hosts van het oude adres naar het VIP {c.vips[0]?.address}. Uit: alleen vastleggen (je deed de overname al).
          </span>
        </label>
        <Field label="Opruimen voorstellen na" htmlFor="rs-days">
          <Select id="rs-days" value={days} onChange={(e) => setDays(e.target.value)}>
            {[3, 7, 14, 30, 60].map((d) => (
              <option key={d} value={d}>
                {d} dagen zonder problemen
              </option>
            ))}
          </Select>
        </Field>
        {c.environment === "prod" && takeover && <Alert kind="info">Productie: de overname stopt de oude dienst. Doe het op een rustig moment.</Alert>}
        {create.isError && <Alert>{(create.error as Error).message}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" disabled={create.isPending || (!node && !addr.trim())}>
            {takeover ? "Overnemen" : "Vastleggen"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function Proposals({
  c,
  a,
  isAdmin,
  loading,
  count,
  onJob,
}: {
  c: ClusterDetail;
  a?: VIPAdvice;
  isAdmin: boolean;
  loading: boolean;
  count: number;
  onJob: (j: Job) => void;
}) {
  const settings = usePublishSettings();
  const [modal, setModal] = useState<{ title: string; action: PublishAction } | null>(null);
  const vip = c.vips[0]?.address ?? "";
  const npmLinked = !!settings.data && (!!settings.data.npm_cluster_id || !!settings.data.npm_node_id);
  // ClusterForge zelf: een proxy host voor de webinterface op het VIP.
  const selfHost = c.template_name === "clusterforge" && npmLinked && !!a && (a.hosts_to_vip ?? 0) === 0;
  if (loading) return <p className="text-slate-500">Voorstellen naar het VIP laden…</p>;
  if (!a) return null;
  return (
    <div className="space-y-3">
      <h3 className="font-medium">Voorstellen naar het VIP {vip}</h3>
      {count === 0 && !selfHost && <p className="text-slate-500">Niets wijst nog naar een oud adres of naar één node.</p>}
      {a.firewall_error && <Alert kind="info">Firewall niet gelezen: {a.firewall_error}</Alert>}
      {a.firewall.map((f) => (
        <FirewallProposals key={f.firewall_id} f={f} isAdmin={isAdmin} onJob={onJob} />
      ))}
      {a.hosts.map((h) => (
        <div key={h.from} className="rounded-md border border-slate-200 p-2 dark:border-slate-700">
          <div>
            Proxy hosts naar {h.from} ({h.why}):
          </div>
          <ul className="ml-4 list-disc text-xs text-slate-500">
            {h.hosts.map((x) => (
              <li key={x}>{x}</li>
            ))}
          </ul>
          {isAdmin && (
            <Button
              variant="secondary"
              className="mt-2"
              onClick={() => setModal({ title: `Proxy hosts naar het VIP ${h.to}`, action: { action: "move_address", old_domain: h.from, new_domain: h.to } })}
            >
              Voorbeeld en naar het VIP…
            </Button>
          )}
        </div>
      ))}
      {selfHost && isAdmin && (
        <div className="rounded-md border border-slate-200 p-2 dark:border-slate-700">
          Geen proxy host wijst naar ClusterForge op het VIP. Voorstel: clusterforge.{settings.data?.domain} → http://{vip}:8080, zonder Authentik
          (ClusterForge heeft een eigen login).
          <div>
            <Button
              variant="secondary"
              className="mt-2"
              onClick={() =>
                setModal({
                  title: "ClusterForge publiceren",
                  action: { action: "publish", label: "clusterforge", forward_scheme: "http", forward_host: vip, forward_port: 8080, authentik: false },
                })
              }
            >
              Voorbeeld en publiceren…
            </Button>
          </div>
        </div>
      )}
      {modal && <ActionModal title={modal.title} action={modal.action} onClose={() => setModal(null)} />}
    </div>
  );
}

function FirewallProposals({ f, isAdmin, onJob }: { f: VIPAdvice["firewall"][number]; isAdmin: boolean; onJob: (j: Job) => void }) {
  const apply = useOpnAdviceApply(f.firewall_id);
  const [open, setOpen] = useState(false);
  const [picked, setPicked] = useState<string[]>(f.items.map((i) => i.key));
  return (
    <div className="rounded-md border border-slate-200 p-2 dark:border-slate-700">
      <div className="font-medium">Firewall {f.cluster}</div>
      <ul className="mt-1 space-y-1">
        {f.items.map((it) => (
          <li key={it.key} className="flex items-start gap-2">
            {isAdmin && (
              <input
                type="checkbox"
                className="mt-1"
                aria-label={`Kies ${it.title}`}
                checked={picked.includes(it.key)}
                onChange={(e) => setPicked((p) => (e.target.checked ? [...p, it.key] : p.filter((k) => k !== it.key)))}
              />
            )}
            <div>
              <div>{it.title}</div>
              {it.detail && <div className="text-xs text-slate-500">{it.detail}</div>}
            </div>
          </li>
        ))}
      </ul>
      {isAdmin && (
        <Button variant="secondary" className="mt-2" disabled={picked.length === 0} onClick={() => setOpen(true)}>
          Gekozen toepassen ({picked.length})…
        </Button>
      )}
      {open && (
        <ActionDialog
          title="Voorstellen naar het VIP toepassen"
          confirm={f.confirm}
          prod={f.prod}
          label="Toepassen"
          start={(b) => apply.mutateAsync({ keys: picked, ...b })}
          onClose={() => setOpen(false)}
          onDone={(j) => {
            setOpen(false);
            onJob(j);
          }}
        >
          <p>Eén wijziging op de firewall, met herstelpunt, terugdraaitimer en proeven:</p>
          <ul className="list-disc pl-5">
            {f.items
              .filter((i) => picked.includes(i.key))
              .map((i) => (
                <li key={i.key}>{i.title}</li>
              ))}
          </ul>
        </ActionDialog>
      )}
    </div>
  );
}
