"use client";

import { useQueries } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";
import { Modal } from "@/components/Modal";
import { formatBytes, LifecycleBadge, QueryState } from "@/components/inventory/bits";
import { Alert, Badge, Button, Field, Input, Textarea } from "@/components/ui";
import { api, ApiError, unwrap } from "@/lib/api/client";
import {
  useCaptureBaseline,
  useCreateDriftIgnore,
  useDeleteDriftIgnore,
  useDriftIgnores,
  type BaselineItem,
  type BaselineItems,
  type BaselineResult,
  type DriftIgnore,
  type DriftSource,
} from "@/lib/drift";
import { useCluster, type ClusterDetail, type Node } from "@/lib/inventory";

const dayFmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "medium" });

// Wat een baseline standaard vastlegt per soort cluster. Services die de
// agent op de nodes ziet, komen er bij het openen bij.
const typeDefaults: Record<ClusterDetail["type"], BaselineItems> = {
  keepalived: { packages: ["keepalived"], services: ["keepalived"], files: ["/etc/keepalived/keepalived.conf"] },
  nginx: { packages: ["nginx"], services: ["nginx"], files: ["/etc/nginx/nginx.conf"] },
  docker: { packages: [], services: ["docker"], files: ["/etc/docker/daemon.json"] },
  cron: { packages: ["cron"], services: ["cron"], files: ["/etc/crontab"] },
  postgresql_ha: { packages: [], services: ["postgresql"], files: [] },
  mariadb_ha: { packages: [], services: ["mariadb"], files: [] },
  generic: { packages: [], services: [], files: [] },
};

// Waarom een node nu niet vast te leggen is, of null.
function notCapturable(n: Node): string | null {
  if (n.lifecycle !== "active") return "niet actief";
  if (!n.agent) return "geen agent";
  if (n.agent.connection === "offline") return "agent niet verbonden";
  return null;
}

// Een tekstvak groeit mee tot acht regels.
const rows = (s: string) => Math.min(8, Math.max(3, s.split("\n").length + 1));

const lines = (s: string) =>
  s
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);

// BaselineWizard legt in drie stappen vast hoe nodes erbij staan: nodes
// kiezen, kiezen wat, en eerst bekijken wat er vastgelegd wordt. Met node
// legt hij één node opnieuw vast en begint hij meteen bij het voorbeeld.
export function BaselineWizard({
  clusterId,
  source,
  node,
  onClose,
}: {
  clusterId: string;
  source: DriftSource | null;
  node?: string;
  onClose: () => void;
}) {
  const cluster = useCluster(clusterId);
  return (
    <Modal title={node ? "Baseline opnieuw vastleggen" : "Baseline vastleggen"} onClose={onClose} wide>
      <QueryState q={cluster}>
        {cluster.data && <Wizard cluster={cluster.data} source={source} node={node} onClose={onClose} />}
      </QueryState>
    </Modal>
  );
}

function Wizard({
  cluster,
  source,
  node,
  onClose,
}: {
  cluster: ClusterDetail;
  source: DriftSource | null;
  node?: string;
  onClose: () => void;
}) {
  const existing = source?.kind === "baseline" ? source.items : null;
  const start = existing ?? typeDefaults[cluster.type];
  const [step, setStep] = useState<1 | 2 | 3>(node && existing ? 3 : 1);
  const [chosen, setChosen] = useState<string[]>(() =>
    node ? [node] : cluster.nodes.filter((n) => !notCapturable(n)).map((n) => n.id),
  );
  const [packages, setPackages] = useState(start.packages.join("\n"));
  const [services, setServices] = useState(start.services.join("\n"));
  const [files, setFiles] = useState(start.files.join("\n"));
  const [suggested, setSuggested] = useState(existing !== null);
  const capture = useCaptureBaseline(cluster.id);
  const [preview, setPreview] = useState<BaselineResult | null>(null);

  // Welke bekeken services de agents op de nodes zien; cf-agent zelf telt
  // niet mee.
  const facts = useQueries({
    queries: cluster.nodes
      .filter((n) => n.agent)
      .map((n) => ({
        queryKey: ["nodes", n.id, "facts"],
        queryFn: async () => unwrap(await api.GET("/nodes/{nodeId}/facts", { params: { path: { nodeId: n.id } } })),
        retry: false,
      })),
  });
  const seen = [
    ...new Set(
      facts.flatMap((f) =>
        (f.data?.facts?.services ?? [])
          .filter((s) => s.name !== "cf-agent" && (s.enabled === "enabled" || s.active === "active"))
          .map((s) => s.name),
      ),
    ),
  ].sort();

  const input = () => ({ node_ids: chosen, packages: lines(packages), services: lines(services), files: lines(files) });
  const runPreview = () => {
    setPreview(null);
    capture.mutate({ ...input(), preview: true }, { onSuccess: setPreview });
  };
  const toStep2 = () => {
    if (!suggested) {
      setServices([...new Set([...lines(services), ...seen])].join("\n"));
      setSuggested(true);
    }
    setStep(2);
  };
  const toStep3 = () => {
    setStep(3);
    runPreview();
  };
  // Opnieuw vastleggen begint bij het voorbeeld.
  useEffect(() => {
    if (step === 3) capture.mutate({ ...input(), preview: true }, { onSuccess: setPreview });
    // Alleen bij het openen.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    await capture.mutateAsync({ ...input(), preview: false });
    onClose();
  };
  const error = capture.error instanceof Error ? capture.error.message : capture.isError ? "Vastleggen mislukt" : null;

  return (
    <form onSubmit={save} className="space-y-4 text-sm">
      <ol className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-500">
        {["Nodes", "Wat vastleggen", "Bekijken en opslaan"].map((label, i) => (
          <li key={label} className={step === i + 1 ? "font-semibold text-slate-900 dark:text-slate-100" : undefined}>
            {i + 1}. {label}
          </li>
        ))}
      </ol>

      {step === 1 && (
        <div className="space-y-3">
          <p className="text-slate-600 dark:text-slate-400">
            Een baseline onthoudt hoe de gekozen pakketten, services en bestanden er nu bij staan. Daarna meldt ClusterForge
            elke afwijking. Nodes die je niet kiest, houden hun huidige baseline.
          </p>
          {cluster.nodes.length === 0 && <p className="text-slate-500">Dit cluster heeft nog geen nodes.</p>}
          <ul className="divide-y divide-slate-100 rounded-md border border-slate-200 dark:divide-slate-800 dark:border-slate-800">
            {cluster.nodes.map((n) => {
              const why = notCapturable(n);
              return (
                <li key={n.id}>
                  <label className="flex items-center gap-3 px-3 py-2">
                    <input
                      type="checkbox"
                      disabled={why !== null}
                      checked={chosen.includes(n.id)}
                      onChange={(e) => setChosen(e.target.checked ? [...chosen, n.id] : chosen.filter((x) => x !== n.id))}
                    />
                    <span className="font-medium">{n.hostname}</span>
                    {n.lifecycle !== "active" && <LifecycleBadge lifecycle={n.lifecycle} />}
                    {why && <span className="text-xs text-slate-500">{why}</span>}
                  </label>
                </li>
              );
            })}
          </ul>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={onClose}>
              Annuleren
            </Button>
            <Button type="button" disabled={chosen.length === 0} onClick={toStep2}>
              Volgende
            </Button>
          </div>
        </div>
      )}

      {step === 2 && (
        <div className="space-y-4">
          <Field label="Pakketten" htmlFor="bl-packages" hint="Eén per regel. Een nieuwere versie telt niet als afwijking.">
            <Textarea id="bl-packages" rows={rows(packages)} value={packages} onChange={(e) => setPackages(e.target.value)} />
          </Field>
          <Field label="Services" htmlFor="bl-services" hint="Eén per regel. ClusterForge onthoudt of ze ingeschakeld zijn en draaien.">
            <Textarea id="bl-services" rows={rows(services)} value={services} onChange={(e) => setServices(e.target.value)} />
          </Field>
          {seen.some((s) => !lines(services).includes(s)) && (
            <div className="flex flex-wrap items-center gap-1.5 text-xs">
              <span className="text-slate-500">Gezien op de nodes:</span>
              {seen
                .filter((s) => !lines(services).includes(s))
                .map((s) => (
                  <button
                    key={s}
                    type="button"
                    className="rounded border border-slate-300 px-1.5 py-0.5 hover:bg-slate-50 dark:border-slate-700 dark:hover:bg-slate-800"
                    onClick={() => setServices([...lines(services), s].join("\n"))}
                  >
                    + {s}
                  </button>
                ))}
            </div>
          )}
          <Field
            label="Bestanden en mappen"
            htmlFor="bl-files"
            hint="Volledige paden, één per regel. Van een bestand onthoudt ClusterForge rechten, eigenaar en een vingerafdruk van de inhoud, nooit de inhoud zelf."
          >
            <Textarea id="bl-files" rows={rows(files)} className="font-mono text-xs" value={files} onChange={(e) => setFiles(e.target.value)} />
          </Field>
          <div className="flex justify-between gap-2">
            <Button type="button" variant="secondary" onClick={() => setStep(1)}>
              Terug
            </Button>
            <Button
              type="button"
              disabled={lines(packages).length + lines(services).length + lines(files).length === 0}
              onClick={toStep3}
            >
              Bekijken
            </Button>
          </div>
        </div>
      )}

      {step === 3 && (
        <div className="space-y-4">
          {capture.isPending && !preview && <p className="text-slate-500">De agents bekijken de nodes…</p>}
          {error && <Alert>{error}</Alert>}
          {preview && (
            <>
              <p className="text-slate-600 dark:text-slate-400">
                Dit wordt vastgelegd. Er verandert niets op de nodes; een afwijking verschijnt pas na de volgende wijziging.
              </p>
              {preview.nodes.map((n) => (
                <PreviewNode key={n.node_id} hostname={n.hostname} items={n.items} notes={n.notes} />
              ))}
            </>
          )}
          <div className="flex justify-between gap-2">
            <Button type="button" variant="secondary" onClick={() => setStep(2)}>
              Terug
            </Button>
            <Button type="submit" disabled={!preview || capture.isPending}>
              {capture.isPending && preview ? "Vastleggen…" : "Vastleggen"}
            </Button>
          </div>
        </div>
      )}
    </form>
  );
}

function PreviewNode({ hostname, items, notes }: { hostname: string; items: BaselineItem[]; notes: string[] }) {
  return (
    <div className="rounded-md border border-slate-200 dark:border-slate-800">
      <div className="border-b border-slate-100 px-3 py-2 font-medium dark:border-slate-800">{hostname}</div>
      {items.length === 0 ? (
        <p className="px-3 py-2 text-slate-500">Niets om vast te leggen.</p>
      ) : (
        <table className="w-full text-left">
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {items.map((it) => (
              <tr key={it.kind + it.name}>
                <td className="hidden w-20 px-3 py-1.5 text-xs text-slate-500 sm:table-cell">{kindLabel[it.kind]}</td>
                <td className="px-3 py-1.5 text-xs">
                  <span className="text-slate-500 sm:hidden">{kindLabel[it.kind]} </span>
                  <span className="font-mono [overflow-wrap:anywhere]">{it.name}</span>
                  <div className="text-slate-600 sm:hidden dark:text-slate-400">{itemText(it)}</div>
                </td>
                <td className="hidden px-3 py-1.5 text-xs text-slate-600 sm:table-cell dark:text-slate-400">{itemText(it)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {notes.length > 0 && (
        <ul className="space-y-0.5 border-t border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200">
          {notes.map((n) => (
            <li key={n}>{n}</li>
          ))}
        </ul>
      )}
    </div>
  );
}

const kindLabel: Record<BaselineItem["kind"], string> = {
  package: "pakket",
  service: "service",
  file: "bestand",
  directory: "map",
};

function itemText(it: BaselineItem) {
  switch (it.kind) {
    case "package":
      return `versie ${it.version}`;
    case "service":
      return [it.enabled ? "ingeschakeld" : "uitgeschakeld", it.active ? "draait" : "gestopt"].join(", ");
    case "file":
      return `${it.mode} ${it.owner}:${it.group}, ${formatBytes(it.size)}${it.content ? ", met inhoud" : ", zonder inhoud"}`;
    case "directory":
      return `${it.mode} ${it.owner}:${it.group}`;
  }
}

// IgnoreForm maakt een negeerregel voor één stap. De sleutel is aan te
// passen: met een * aan het eind geldt hij voor alles met dat begin.
export function IgnoreForm({
  clusterId,
  nodeId,
  hostname,
  step,
  onDone,
}: {
  clusterId: string;
  nodeId: string;
  hostname: string;
  step: string;
  onDone: () => void;
}) {
  const create = useCreateDriftIgnore(clusterId);
  const [key, setKey] = useState(step);
  const [reason, setReason] = useState("");
  const [everywhere, setEverywhere] = useState(false);
  const [until, setUntil] = useState("");
  const id = `ign-${nodeId}-${step}`;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    await create.mutateAsync({
      key,
      reason,
      node_id: everywhere ? null : nodeId,
      expires_at: until ? new Date(`${until}T23:59:59`).toISOString() : null,
    });
    onDone();
  };
  const field = create.error instanceof ApiError ? create.error.field : undefined;
  return (
    <form
      onSubmit={submit}
      className="mt-2 space-y-3 rounded-md border border-slate-200 bg-slate-50 p-3 dark:border-slate-800 dark:bg-slate-800/40"
    >
      <Field label="Reden" htmlFor={`${id}-reason`} hint="Komt in het logboek en bij de regel te staan.">
        <Input id={`${id}-reason`} value={reason} required minLength={3} maxLength={500} onChange={(e) => setReason(e.target.value)} />
      </Field>
      <fieldset className="space-y-1">
        <legend className="mb-1 text-sm font-medium text-slate-700 dark:text-slate-300">Waar</legend>
        <label className="flex items-center gap-2">
          <input type="radio" checked={!everywhere} onChange={() => setEverywhere(false)} /> Alleen op {hostname}
        </label>
        <label className="flex items-center gap-2">
          <input type="radio" checked={everywhere} onChange={() => setEverywhere(true)} /> Op alle nodes van dit cluster
        </label>
      </fieldset>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Tot en met" htmlFor={`${id}-until`} hint="Leeg: tot je de regel opheft.">
          <Input id={`${id}-until`} type="date" value={until} onChange={(e) => setUntil(e.target.value)} />
        </Field>
        <Field label="Stap" htmlFor={`${id}-key`} hint="Eindig op * voor alles met dit begin.">
          <Input id={`${id}-key`} className="font-mono text-xs" value={key} onChange={(e) => setKey(e.target.value)} />
        </Field>
      </div>
      {create.isError && (
        <Alert>
          {field === "key" ? "Stap: " : field === "expires_at" ? "Einddatum: " : ""}
          {create.error instanceof Error ? create.error.message : "Negeren mislukt"}
        </Alert>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={onDone}>
          Annuleren
        </Button>
        <Button type="submit" disabled={create.isPending}>
          {create.isPending ? "Negeren…" : "Negeren"}
        </Button>
      </div>
    </form>
  );
}

// IgnoredList toont de negeerregels, ingeklapt. Met nodeId alleen de regels
// die voor die node gelden.
export function IgnoredList({ clusterId, nodeId, isAdmin }: { clusterId: string; nodeId?: string; isAdmin: boolean }) {
  const q = useDriftIgnores(clusterId);
  const remove = useDeleteDriftIgnore();
  const rules = (q.data ?? []).filter((r) => !nodeId || r.node_id === null || r.node_id === nodeId);
  if (rules.length === 0) return null;
  return (
    <details className="group rounded-md border border-slate-200 dark:border-slate-800">
      <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2 text-sm hover:bg-slate-50 dark:hover:bg-slate-800/50 [&::-webkit-details-marker]:hidden">
        <span aria-hidden className="text-slate-400 transition group-open:rotate-90">
          ›
        </span>
        Genegeerd ({rules.length})
      </summary>
      <ul className="divide-y divide-slate-100 border-t border-slate-100 dark:divide-slate-800 dark:border-slate-800">
        {rules.map((r) => (
          <IgnoreRow key={r.id} r={r} isAdmin={isAdmin} pending={remove.isPending && remove.variables === r.id} onRemove={() => remove.mutate(r.id)} />
        ))}
      </ul>
      {remove.isError && (
        <div className="px-3 pb-3">
          <Alert>{remove.error instanceof Error ? remove.error.message : "Opheffen mislukt"}</Alert>
        </div>
      )}
    </details>
  );
}

function IgnoreRow({ r, isAdmin, pending, onRemove }: { r: DriftIgnore; isAdmin: boolean; pending: boolean; onRemove: () => void }) {
  return (
    <li className={`flex flex-wrap items-start justify-between gap-2 px-3 py-2 text-sm ${r.expired ? "opacity-60" : ""}`}>
      <div className="min-w-0 space-y-0.5">
        <div className="flex flex-wrap items-center gap-2">
          <code className="text-xs break-all">{r.key}</code>
          <span className="text-xs text-slate-500">{r.hostname ? `op ${r.hostname}` : "op alle nodes"}</span>
          {r.expired && <Badge>verlopen</Badge>}
        </div>
        <div>{r.reason}</div>
        <div className="text-xs text-slate-500">
          {r.created_by ? `door ${r.created_by.name}, ` : ""}
          {dayFmt.format(new Date(r.created_at))}
          {r.expires_at && ` · ${r.expired ? "verlopen op" : "tot en met"} ${dayFmt.format(new Date(r.expires_at))}`}
        </div>
      </div>
      {isAdmin && (
        <Button variant="secondary" className="px-2.5 py-1 text-xs" disabled={pending} onClick={onRemove}>
          Opheffen
        </Button>
      )}
    </li>
  );
}
