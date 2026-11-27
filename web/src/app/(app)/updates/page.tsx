"use client";

import { Tabs } from "@/components/Tabs";
import { useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useEffect, useState } from "react";
import { Answer, LiveAnswer, useStream } from "@/components/assistant/parts";
import { Modal } from "@/components/Modal";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { Alert, Badge, Button, Card, Input, PageHeader, Select, Textarea } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { proposeDockerFix, useAssistantSettings } from "@/lib/assistant";
import { useIsAdmin } from "@/lib/inventory";
import {
  proposalFile,
  resultLabels,
  useReadCompose,
  useSetClusterPolicy,
  useSetEnvPolicy,
  useStartUpdates,
  useUpdateAll,
  useUpdatePolicies,
  useUpdates,
  useWriteCompose,
  type ClusterPolicy,
  type ComposeFile,
  type ComposeProject,
  type DockerReport,
  type UpdateNode,
  type UpdatePolicy,
} from "@/lib/updates";

type Target = {
  title: string;
  nodes: UpdateNode[];
  clusterId?: string;
  prodSlug?: string;
  tags?: { project: string; from: string; to: string }[];
};
type ProjectRef = { node: UpdateNode; project: ComposeProject; prodSlug?: string };
type EditorOpen = ProjectRef & { proposal?: { path: string; content: string } };

function errText(e: unknown, fallback: string) {
  return e instanceof ApiError ? e.message : fallback;
}

export default function UpdatesPage() {
  const [tab, setTab] = useState<"now" | "auto">("now");
  return (
    <div className="space-y-6">
      <PageHeader
        title="Updates"
        description="Pakketten (apt) en Docker Compose-projecten bijwerken via de agents. Per cluster eerst de back-upnodes, dan de VIP naar een bijgewerkte node en als laatste de master; na elke node wacht ClusterForge tot alles weer draait, en bij een fout stopt het."
      />
      <Tabs
        tabs={[
          { key: "now", label: "Bijwerken" },
          { key: "auto", label: "Automatisch" },
        ]}
        value={tab}
        onChange={setTab}
      />
      {tab === "now" ? <NowTab /> : <AutoTab />}
    </div>
  );
}

// ---------------------------------------------------------------- Bijwerken

function NowTab() {
  const updates = useUpdates();
  const isAdmin = useIsAdmin();
  const [target, setTarget] = useState<Target | null>(null);
  const [project, setProject] = useState<ProjectRef | null>(null);
  const [editor, setEditor] = useState<EditorOpen | null>(null);

  const groups: { key: string; name: string; env: string; slug: string; clusterId?: string; nodes: UpdateNode[] }[] = [];
  for (const n of updates.data?.nodes ?? []) {
    const key = n.cluster_id ?? "";
    let g = groups.find((x) => x.key === key);
    if (!g) {
      g = { key, name: n.cluster_name || "Zonder cluster", env: n.cluster_environment, slug: n.cluster_slug, clusterId: n.cluster_id ?? undefined, nodes: [] };
      groups.push(g);
    }
    g.nodes.push(n);
  }
  const smart = updates.data?.smart_protocol ?? 29;

  const [all, setAll] = useState(false);
  return (
    <>
      {isAdmin && groups.length > 0 && (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-slate-200 bg-white px-4 py-3 dark:border-slate-800 dark:bg-slate-900">
          <p className="text-sm text-slate-600 dark:text-slate-400">
            Alles in één keer: elk cluster rollend, het ene na het andere, Proxmox-hosts uitgezonderd.
          </p>
          <Button onClick={() => setAll(true)}>Alles bijwerken</Button>
        </div>
      )}
      {all && <UpdateAllDialog onClose={() => setAll(false)} />}
      {updates.isError && <Alert>Updates laden mislukte.</Alert>}
      {updates.data && groups.length === 0 && <p className="text-sm text-slate-500">Nog geen nodes met een agent.</p>}
      {groups.map((g) => {
        const prod = g.env === "prod";
        const usable = g.nodes.filter((n) => n.supported && n.online);
        return (
          <Card
            key={g.key}
            title={
              <span className="flex flex-wrap items-center justify-between gap-2">
                <span className="flex items-center gap-2">
                  {g.name}
                  {g.env && <Badge tone={prod ? "red" : "slate"}>{g.env}</Badge>}
                  {g.nodes.some((n) => n.has_database) && <Badge tone="blue">database</Badge>}
                </span>
                {isAdmin && g.clusterId && (
                  <Button
                    disabled={usable.length !== g.nodes.length}
                    title={usable.length !== g.nodes.length ? "Niet elke node is verbonden met een agent vanaf protocol " + updates.data?.run_protocol : undefined}
                    onClick={() => setTarget({ title: `${g.name} bijwerken`, nodes: g.nodes, clusterId: g.clusterId, prodSlug: prod ? g.slug : undefined })}
                  >
                    Cluster bijwerken
                  </Button>
                )}
              </span>
            }
          >
            {g.nodes.some((n) => n.has_database) && (
              <p className="mb-3 text-xs text-slate-500">
                De databasepakketten blijven bij een update staan; die werk je bij met Bijwerken op de databasekaart van het cluster.
              </p>
            )}
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead className="text-left text-xs text-slate-500">
                  <tr>
                    <th className="py-1 pr-3 font-medium">Node</th>
                    <th className="py-1 pr-3 font-medium">Pakketten</th>
                    <th className="py-1 pr-3 font-medium">Docker</th>
                    <th className="py-1 pr-3 font-medium">Herstart</th>
                    <th className="py-1" />
                  </tr>
                </thead>
                <tbody>
                  {g.nodes.map((n) => (
                    <NodeRow
                      key={n.node_id}
                      n={n}
                      runProtocol={updates.data?.run_protocol ?? 0}
                      smartProtocol={smart}
                      onProject={(p) => setProject({ node: n, project: p, prodSlug: prod ? g.slug : undefined })}
                      onUpdate={
                        isAdmin
                          ? () => setTarget({ title: `${n.hostname} bijwerken`, nodes: [n], prodSlug: prod ? g.slug : undefined })
                          : undefined
                      }
                    />
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        );
      })}
      {target && <UpdateDialog target={target} onClose={() => setTarget(null)} />}
      {project && (
        <ProjectDialog
          refd={project}
          admin={isAdmin}
          onClose={() => setProject(null)}
          onEdit={(proposal) => {
            setEditor({ ...project, proposal });
            setProject(null);
          }}
          onTag={(nodes) => setTarget(nodes)}
        />
      )}
      {editor && <ComposeEditor open={editor} onClose={() => setEditor(null)} />}
    </>
  );
}

function reportOf(n: UpdateNode, project: string): DockerReport | undefined {
  return n.docker_reports.find((r) => r.project === project);
}

function NodeRow({
  n,
  runProtocol,
  smartProtocol,
  onUpdate,
  onProject,
}: {
  n: UpdateNode;
  runProtocol: number;
  smartProtocol: number;
  onUpdate?: () => void;
  onProject: (p: ComposeProject) => void;
}) {
  const total = n.upgrades_total;
  return (
    <tr className="border-t border-slate-100 align-top dark:border-slate-800">
      <td className="py-2 pr-3">
        <Link href={`/nodes/detail?id=${n.node_id}`} className="font-medium hover:underline">
          {n.hostname}
        </Link>
        <span className="ml-2 inline-flex flex-wrap gap-1">
          {n.vip_holder && <Badge tone="blue">VIP</Badge>}
          {!n.online && <Badge tone="red">offline</Badge>}
          {n.online && !n.supported && <Badge tone="amber">agent te oud (protocol {n.protocol}, nodig {runProtocol})</Badge>}
          {n.online && n.supported && !n.smart && <Badge tone="amber">agent {n.protocol}: werk bij naar {smartProtocol} voor testen en terugzetten</Badge>}
        </span>
      </td>
      <td className="py-2 pr-3">
        {total === null ? (
          <span className="text-slate-500">onbekend</span>
        ) : total === 0 ? (
          <span className="text-green-700 dark:text-green-400">bij</span>
        ) : (
          <details>
            <summary className="cursor-pointer">
              {total} {total === 1 ? "update" : "updates"}
              {n.upgrades_security > 0 && (
                <span className="ml-2">
                  <Badge tone="red">{n.upgrades_security} beveiliging</Badge>
                </span>
              )}
            </summary>
            <p className="mt-1 max-w-md font-mono text-xs break-words text-slate-600 dark:text-slate-400">{n.packages.join(" ")}</p>
          </details>
        )}
        {n.apt_updated_at && <p className="text-xs text-slate-500">lijsten van {new Date(n.apt_updated_at).toLocaleString("nl-BE")}</p>}
      </td>
      <td className="py-2 pr-3">
        {!n.docker_version ? (
          <span className="text-slate-500">geen</span>
        ) : (
          <>
            <span className="text-xs text-slate-500">Docker {n.docker_version}</span>
            {n.compose_projects.length === 0 ? (
              <p className="text-xs text-slate-500">geen Compose-projecten</p>
            ) : (
              <ul className="space-y-0.5 text-xs">
                {n.compose_projects.map((p) => {
                  const rep = reportOf(n, p.name);
                  return (
                    <li key={p.name} className="flex flex-wrap items-center gap-1">
                      <button type="button" className="font-mono text-brand-700 hover:underline dark:text-sky-300" onClick={() => onProject(p)}>
                        {p.name}
                      </button>
                      <span className="text-slate-500">{p.status}</span>
                      {p.missing.length > 0 && <Badge tone="red">compose-bestand weg</Badge>}
                      {rep && rep.result !== "up_to_date" && <Badge tone={resultLabels[rep.result].tone}>{resultLabels[rep.result].text}</Badge>}
                    </li>
                  );
                })}
              </ul>
            )}
          </>
        )}
      </td>
      <td className="py-2 pr-3">{n.reboot_required ? <Badge tone="amber">nodig</Badge> : <span className="text-slate-500">nee</span>}</td>
      <td className="py-2 text-right">
        {onUpdate && (
          <Button variant="secondary" disabled={!n.online || !n.supported} onClick={onUpdate}>
            Bijwerken
          </Button>
        )}
      </td>
    </tr>
  );
}

function UpdateDialog({ target, onClose }: { target: Target; onClose: () => void }) {
  const start = useStartUpdates();
  const totp = useProdTotp();
  const tags = target.tags;
  const hasDocker = target.nodes.some((n) => n.compose_projects.length > 0);
  const smart = target.nodes.every((n) => n.smart);
  const [apt, setApt] = useState(!tags);
  const [full, setFull] = useState(true);
  const [autoremove, setAutoremove] = useState(false);
  const [docker, setDocker] = useState(hasDocker || !!tags);
  const [prune, setPrune] = useState(false);
  const [rollback, setRollback] = useState(true);
  const [always, setAlways] = useState(false);
  const [health, setHealth] = useState("120");
  const [reboot, setReboot] = useState(false);
  const [failover, setFailover] = useState(true);
  const [confirm, setConfirm] = useState("");
  const blocked = (!apt && !docker) || (!!target.prodSlug && !prodConfirmed(target.prodSlug, confirm, totp));
  const backups = target.nodes.filter((n) => !n.vip_holder);
  const masters = target.nodes.filter((n) => n.vip_holder);
  // De standaarden van Veilig; elke afwijking is "Eigen keuzes".
  const safe = { apt: !tags, full: true, autoremove: false, docker: hasDocker || !!tags, prune: false, rollback: true, always: false, health: "120", reboot: false, failover: true };
  const custom =
    apt !== safe.apt ||
    full !== safe.full ||
    autoremove !== safe.autoremove ||
    docker !== safe.docker ||
    prune !== safe.prune ||
    rollback !== safe.rollback ||
    always !== safe.always ||
    health !== safe.health ||
    reboot !== safe.reboot ||
    failover !== safe.failover;
  const reset = () => {
    setApt(safe.apt);
    setFull(safe.full);
    setAutoremove(safe.autoremove);
    setDocker(safe.docker);
    setPrune(safe.prune);
    setRollback(safe.rollback);
    setAlways(safe.always);
    setHealth(safe.health);
    setReboot(safe.reboot);
    setFailover(safe.failover);
  };
  const summary = [
    apt && (full ? "pakketten bijwerken (full-upgrade)" : "pakketten bijwerken"),
    apt && autoremove && "daarna opruimen",
    docker && (smart && rollback ? `Docker-images bijwerken, ${health} s testen en bij een fout terugzetten` : "Docker-images bijwerken"),
    docker && prune && "oude images opruimen",
    target.nodes.length > 1 && "één node tegelijk, de VIP-houder als laatste" + (failover && masters.length > 0 ? " na een failover" : ""),
    "stoppen bij de eerste node die niet gezond terugkomt",
    reboot ? "herstarten als de node erom vraagt" : "niet herstarten",
  ]
    .filter(Boolean)
    .join("; ")
    .replace(/^./, (c) => c.toUpperCase()) + ".";

  const submit = () =>
    start.mutate({
      ...(target.clusterId ? { cluster_id: target.clusterId } : { node_ids: target.nodes.map((n) => n.node_id) }),
      ...(apt ? { apt: { full, autoremove } } : {}),
      ...(docker
        ? {
            docker: {
              prune,
              ...(smart ? { rollback, always, health_seconds: Number(health) || 0 } : {}),
              ...(tags ? { tags, projects: [...new Set(tags.map((t) => t.project))] } : {}),
            },
          }
        : {}),
      reboot,
      failover,
      ...(target.prodSlug ? { confirm } : {}),
    });

  const order =
    backups.map((n) => n.hostname).join(" → ") +
    (masters.length > 0
      ? (backups.length > 0 ? " → " : "") +
        masters.map((n) => (failover && target.nodes.length > 1 ? `VIP weg van ${n.hostname} → ${n.hostname} → keepalived terug` : `${n.hostname} (VIP)`)).join(" → ")
      : "");
  return (
    <Modal title={target.title} onClose={onClose}>
      {start.data ? (
        <div className="space-y-3 text-sm">
          <Alert kind="success">De update is gestart.</Alert>
          {start.data.jobs.map((j) => (
            <p key={j.id}>
              <Link href={`/taken/detail?id=${j.id}`} className="font-medium text-brand-700 underline dark:text-sky-300">
                {j.title}
              </Link>{" "}
              (uitvoer live in de taak)
            </p>
          ))}
          {start.data.skipped.map((s) => (
            <Alert key={s}>{s}</Alert>
          ))}
          <div className="flex justify-end">
            <Button onClick={onClose}>Sluiten</Button>
          </div>
        </div>
      ) : (
        <div className="space-y-4 text-sm">
          <p className="text-slate-600 dark:text-slate-400">Volgorde: {order}</p>
          {tags && (
            <p className="text-slate-600 dark:text-slate-400">
              Nieuwe versie:{" "}
              {tags.map((t) => (
                <span key={t.project + t.from} className="font-mono">
                  {t.from} → {t.to}{" "}
                </span>
              ))}
            </p>
          )}
          {/* Fase 47: één keuze, Veilig, met de standaarden; de vinkjes
              staan onder Geavanceerd. */}
          <div className="rounded-md border border-emerald-200 bg-emerald-50/60 p-3 dark:border-emerald-900 dark:bg-emerald-950/30">
            <p className="font-medium text-emerald-900 dark:text-emerald-200">{custom ? "Eigen keuzes" : "Veilig (aanbevolen)"}</p>
            <p className="mt-1 text-xs text-slate-600 dark:text-slate-400">{summary}</p>
          </div>
          <details className="group rounded-md border border-slate-200 dark:border-slate-800">
            <summary className="cursor-pointer list-none px-3 py-2 text-sm text-slate-600 hover:bg-slate-50 dark:text-slate-400 dark:hover:bg-slate-800/50 [&::-webkit-details-marker]:hidden">
              <span aria-hidden className="mr-1 inline-block text-slate-400 transition group-open:rotate-90">
                ›
              </span>
              Geavanceerd
              {custom && (
                <button
                  type="button"
                  className="ml-3 text-xs text-brand-700 underline dark:text-sky-300"
                  onClick={(e) => {
                    e.preventDefault();
                    reset();
                  }}
                >
                  terug naar Veilig
                </button>
              )}
            </summary>
            <div className="border-t border-slate-200 p-3 dark:border-slate-800">
            <fieldset className="space-y-1">
              <Check checked={apt} onChange={setApt}>
                Pakketten (apt)
              </Check>
              {apt && (
                <div className="ml-6 space-y-1">
                  <Check checked={full} onChange={setFull}>
                    full-upgrade (standaard; pakketten mogen erbij komen of weggaan)
                  </Check>
                  <Check checked={autoremove} onChange={setAutoremove}>
                    daarna autoremove
                  </Check>
                  <p className="text-xs text-slate-500">Nooit een vraag: je eigen configuratiebestanden blijven staan, een slot van een andere apt wacht tot 10 minuten, een onderbroken dpkg wordt eerst hersteld.</p>
                </div>
              )}
              <Check checked={docker} onChange={setDocker}>
                Docker Compose (pull; bij een nieuw image down en up -d)
              </Check>
              {docker && (
                <div className="ml-6 space-y-1">
                  {smart ? (
                    <>
                      <Check checked={rollback} onChange={setRollback}>
                        terugzetten naar de vorige versie als de test mislukt
                      </Check>
                      <Check checked={always} onChange={setAlways}>
                        ook zonder nieuw image opnieuw opstarten
                      </Check>
                      <label className="flex items-center gap-2">
                        test hoogstens
                        <Input className="w-20" inputMode="numeric" value={health} onChange={(e) => setHealth(e.target.value)} /> seconden
                      </label>
                    </>
                  ) : (
                    <p className="text-xs text-amber-700 dark:text-amber-300">Niet elke agent heeft protocol 29: alleen pull en up -d, zonder test en terugzetten.</p>
                  )}
                  <Check checked={prune} onChange={setPrune}>
                    daarna ongebruikte images opruimen
                  </Check>
                </div>
              )}
              <Check checked={reboot} onChange={setReboot}>
                Herstarten als de node erom vraagt
              </Check>
              {target.nodes.length > 1 && masters.length > 0 && (
                <Check checked={failover} onChange={setFailover}>
                  De master eerst failoveren (keepalived uit, VIP naar een bijgewerkte node)
                </Check>
              )}
            </fieldset>
            </div>
          </details>
          {target.prodSlug && <ProdConfirm slug={target.prodSlug} value={confirm} onChange={setConfirm} />}
          {start.error && <Alert>{errText(start.error, "Starten mislukte")}</Alert>}
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={onClose}>
              Annuleren
            </Button>
            <Button disabled={blocked || start.isPending} onClick={submit}>
              Bijwerken
            </Button>
          </div>
        </div>
      )}
    </Modal>
  );
}

// UpdateAllDialog is "Alles bijwerken (rollend, behalve hosts)" (fase 47):
// één taak die de clusters één na één bijwerkt met de keuzes van Veilig.
// Eerst het plan, zodat je ziet wat meedoet en wat niet.
function UpdateAllDialog({ onClose }: { onClose: () => void }) {
  const totp = useProdTotp();
  const plan = useUpdateAll();
  const start = useUpdateAll();
  const [prod, setProd] = useState(false);
  const [confirm, setConfirm] = useState("");
  const [reboot, setReboot] = useState(false);
  const body = { apt: { full: true }, docker: { rollback: true, health_seconds: 120 }, failover: true, reboot, include_prod: prod };
  const planned = plan.data;
  const { mutate } = plan;
  useEffect(() => {
    mutate({ ...body, dry_run: true });
    // Alleen opnieuw plannen als prod aan of uit gaat.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [prod, mutate]);
  const needProd = (planned?.prod.length ?? 0) > 0;
  const blocked = !planned || planned.clusters.length === 0 || start.isPending || (needProd && !prodConfirmed("prod", confirm, totp));
  return (
    <Modal title="Alles bijwerken" onClose={onClose}>
      {start.data?.job ? (
        <div className="space-y-3 text-sm">
          <Alert kind="success">Gestart: {start.data.clusters.length} clusters, één na één.</Alert>
          <p>
            <Link href={`/taken/detail?id=${start.data.job.id}`} className="font-medium text-brand-700 underline dark:text-sky-300">
              {start.data.job.title}
            </Link>{" "}
            (per cluster een eigen taak met de uitvoer)
          </p>
          <div className="flex justify-end">
            <Button onClick={onClose}>Sluiten</Button>
          </div>
        </div>
      ) : (
        <div className="space-y-4 text-sm">
          <div className="rounded-md border border-emerald-200 bg-emerald-50/60 p-3 dark:border-emerald-900 dark:bg-emerald-950/30">
            <p className="font-medium text-emerald-900 dark:text-emerald-200">Veilig (aanbevolen)</p>
            <p className="mt-1 text-xs text-slate-600 dark:text-slate-400">
              Per cluster: pakketten (full-upgrade) en Docker-images, één node tegelijk, de VIP-houder als laatste na een failover; een
              Docker-image dat de test niet haalt, gaat terug. Het volgende cluster begint pas als het vorige gelukt is; bij een fout stopt
              alles en kun je vanaf die node verder gaan.
            </p>
          </div>
          {plan.isPending && <p className="text-slate-500">Plan maken…</p>}
          {plan.error && <Alert>{plan.error.message}</Alert>}
          {planned && (
            <>
              <div>
                <p className="font-medium">Volgorde ({planned.clusters.length})</p>
                {planned.clusters.length === 0 ? (
                  <p className="text-slate-500">Niets om bij te werken.</p>
                ) : (
                  <ol className="mt-1 list-decimal space-y-0.5 pl-5 text-slate-700 dark:text-slate-300">
                    {planned.clusters.map((c) => (
                      <li key={c}>{c}</li>
                    ))}
                  </ol>
                )}
              </div>
              {(planned.skipped.length > 0 || planned.hosts.length > 0) && (
                <div className="text-xs text-slate-500">
                  <p className="font-medium">Doen niet mee</p>
                  <ul className="mt-0.5 list-disc pl-5">
                    {planned.hosts.length > 0 && <li>Proxmox-hosts ({planned.hosts.join(", ")}): via Proxmox → Host-updates</li>}
                    {planned.skipped.map((x) => (
                      <li key={x}>{x}</li>
                    ))}
                  </ul>
                </div>
              )}
            </>
          )}
          <fieldset className="space-y-1">
            <Check checked={prod} onChange={setProd}>
              Ook de prodclusters, als laatste
            </Check>
            <Check checked={reboot} onChange={setReboot}>
              Herstarten als een node erom vraagt
            </Check>
          </fieldset>
          {needProd && <ProdConfirm slug="prod" value={confirm} onChange={setConfirm} />}
          {start.error && <Alert>{errText(start.error, "Starten mislukte")}</Alert>}
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={onClose}>
              Annuleren
            </Button>
            <Button disabled={blocked} onClick={() => start.mutate({ ...body, ...(needProd ? { confirm } : {}) })}>
              Alles bijwerken
            </Button>
          </div>
        </div>
      )}
    </Modal>
  );
}

function Check({ checked, onChange, children }: { checked: boolean; onChange: (v: boolean) => void; children: React.ReactNode }) {
  return (
    <label className="flex items-center gap-2">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} /> {children}
    </label>
  );
}

// ------------------------------------------------------- Project en editor

function splitImage(image: string): { repo: string; tag: string } {
  const at = image.indexOf("@");
  const base = at >= 0 ? image.slice(0, at) : image;
  const colon = base.lastIndexOf(":");
  if (colon > base.lastIndexOf("/")) return { repo: base.slice(0, colon), tag: base.slice(colon + 1) };
  return { repo: base, tag: "latest" };
}

function ProjectDialog({
  refd,
  admin,
  onClose,
  onEdit,
  onTag,
}: {
  refd: ProjectRef;
  admin: boolean;
  onClose: () => void;
  onEdit: (proposal?: { path: string; content: string }) => void;
  onTag: (t: Target) => void;
}) {
  const { node, project } = refd;
  const rep = reportOf(node, project.name);
  const [fix, setFix] = useState(false);
  const [tags, setTags] = useState<Record<string, string>>({});
  const images = [...new Set(project.services.map((s) => s.image))];
  const changes = images
    .filter((img) => tags[img] && tags[img] !== splitImage(img).tag)
    .map((img) => ({ project: project.name, from: img, to: splitImage(img).repo + ":" + tags[img] }));
  return (
    <Modal title={`${project.name} op ${node.hostname}`} onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        <div className="space-y-1 text-xs text-slate-600 dark:text-slate-400">
          <p>
            Werkmap <span className="font-mono">{project.working_dir || "?"}</span>
          </p>
          <p>
            Compose <span className="font-mono">{project.config_files.join(", ")}</span>
            {project.env_files.length > 0 && (
              <>
                {" "}
                · env <span className="font-mono">{project.env_files.join(", ")}</span>
              </>
            )}
          </p>
          {project.missing.length > 0 && <Alert>Het compose-bestand {project.missing.join(", ")} bestaat niet meer; dit project kan ClusterForge niet opnieuw maken.</Alert>}
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead className="text-left text-slate-500">
              <tr>
                <th className="py-1 pr-2 font-medium">Dienst</th>
                <th className="py-1 pr-2 font-medium">Image</th>
                <th className="py-1 pr-2 font-medium">Toestand</th>
                <th className="py-1 font-medium">Gemapt</th>
              </tr>
            </thead>
            <tbody>
              {project.services.map((s) => (
                <tr key={s.container} className="border-t border-slate-100 align-top dark:border-slate-800">
                  <td className="py-1 pr-2 font-mono">{s.service}</td>
                  <td className="py-1 pr-2 font-mono break-all">{s.image}</td>
                  <td className="py-1 pr-2">
                    <Badge tone={s.state === "running" && s.health !== "unhealthy" ? "green" : "red"}>{s.state}</Badge>
                    {s.health && <span className="ml-1 text-slate-500">{s.health}</span>}
                    {s.restarts > 0 && <span className="ml-1 text-amber-700">{s.restarts}× herstart</span>}
                  </td>
                  <td className="py-1 font-mono break-all text-slate-600 dark:text-slate-400">{s.mounts.join(" ")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        {rep && <ReportView rep={rep} />}

        {admin && node.smart && (
          <details className="rounded-md border border-slate-200 p-3 dark:border-slate-800">
            <summary className="cursor-pointer font-medium">Nieuwe versie (image-tag aanpassen)</summary>
            <div className="mt-2 space-y-2">
              {images.map((img) => (
                <label key={img} className="flex flex-wrap items-center gap-2">
                  <span className="font-mono text-xs">{splitImage(img).repo}:</span>
                  <Input className="w-40 font-mono" placeholder={splitImage(img).tag} value={tags[img] ?? ""} onChange={(e) => setTags({ ...tags, [img]: e.target.value.trim() })} />
                </label>
              ))}
              <p className="text-xs text-slate-500">ClusterForge past de image-regel in het compose-bestand aan (of de variabele in het env-bestand), haalt het image op, start opnieuw en test. Mislukt het, dan komt de oude versie terug.</p>
              <Button
                disabled={changes.length === 0}
                onClick={() => onTag({ title: `${project.name} op ${node.hostname}: nieuwe versie`, nodes: [node], prodSlug: refd.prodSlug, tags: changes })}
              >
                Verder
              </Button>
            </div>
          </details>
        )}

        {fix && <FixView node={node} project={project.name} onEdit={onEdit} />}

        <div className="flex flex-wrap justify-end gap-2">
          {admin && node.smart && rep && !fix && <FixButton onClick={() => setFix(true)} />}
          {admin && node.smart && (
            <Button variant="secondary" onClick={() => onEdit()}>
              Compose-bestand
            </Button>
          )}
          <Button variant="secondary" onClick={onClose}>
            Sluiten
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function FixButton({ onClick }: { onClick: () => void }) {
  const settings = useAssistantSettings();
  if (!settings.data?.can_use) return null;
  return <Button onClick={onClick}>Fix voorstellen</Button>;
}

function ReportView({ rep }: { rep: DockerReport }) {
  const label = resultLabels[rep.result];
  return (
    <div className="space-y-2 rounded-md border border-slate-200 p-3 dark:border-slate-800">
      <p className="flex flex-wrap items-center gap-2">
        <span className="font-medium">Laatste update</span>
        <Badge tone={label.tone}>{label.text}</Badge>
        <span className="text-xs text-slate-500">{new Date(rep.created_at).toLocaleString("nl-BE")}</span>
        {rep.job_id && (
          <Link href={`/taken/detail?id=${rep.job_id}`} className="text-xs text-brand-700 hover:underline dark:text-sky-300">
            taak
          </Link>
        )}
        {rep.conversation_id && (
          <Link href={`/assistent?gesprek=${rep.conversation_id}`} className="text-xs text-brand-700 hover:underline dark:text-sky-300">
            voorstel voor een fix
          </Link>
        )}
      </p>
      {rep.problem && <p className="text-red-700 dark:text-red-300">{rep.problem}</p>}
      <ul className="space-y-1 text-xs">
        {rep.services
          .filter((s) => s.changed)
          .map((s) => (
            <li key={s.service}>
              <span className="font-mono">{s.service}</span> {s.image}
              {(s.old_version || s.new_version) && (
                <span className="text-slate-500">
                  {" "}
                  ({s.old_version || "?"} → {s.new_version || "?"})
                </span>
              )}
              {s.env_added.length > 0 && (
                <span className="ml-2">
                  <Badge tone="amber">nieuwe variabelen: {s.env_added.join(", ")}</Badge>
                </span>
              )}
              {s.env_removed.length > 0 && (
                <span className="ml-2">
                  <Badge tone="red">verdwenen: {s.env_removed.join(", ")}</Badge>
                </span>
              )}
              {s.source && (
                <a href={s.source} target="_blank" rel="noreferrer" className="ml-2 text-brand-700 hover:underline dark:text-sky-300">
                  bron
                </a>
              )}
            </li>
          ))}
      </ul>
      {rep.edited.length > 0 && <p className="text-xs text-slate-500">Aangepast: {rep.edited.join(", ")}</p>}
      {rep.logs && (
        <details>
          <summary className="cursor-pointer text-xs">docker compose logs</summary>
          <pre className="mt-1 max-h-64 overflow-auto rounded bg-slate-900 p-2 text-[11px] text-slate-100">{rep.logs}</pre>
        </details>
      )}
    </div>
  );
}

function FixView({ node, project, onEdit }: { node: UpdateNode; project: string; onEdit: (p?: { path: string; content: string }) => void }) {
  const settings = useAssistantSettings();
  const qc = useQueryClient();
  const stream = useStream();
  const [started, setStarted] = useState(false);
  if (!started) {
    setStarted(true);
    void stream
      .run((onEvent, signal) => proposeDockerFix(node.node_id, project, onEvent, signal), "Het voorstel kon niet beginnen.")
      .then((res) => res && qc.invalidateQueries({ queryKey: ["updates"] }));
  }
  const proposal = stream.live?.done ? proposalFile(stream.live.text) : null;
  return (
    <div className="space-y-2">
      <p className="text-xs text-slate-500">ClusterForge haalt de release notes van GitHub of Docker Hub op en geeft ze met de logs en het compose-bestand (zonder wachtwoorden) aan de assistent.</p>
      {stream.live && (
        <Answer>
          <LiveAnswer live={stream.live} catalog={settings.data?.models} />
        </Answer>
      )}
      {stream.error && <Alert>{stream.error}</Alert>}
      {proposal && (
        <div className="flex justify-end">
          <Button onClick={() => onEdit(proposal)}>Voorstel openen in de editor</Button>
        </div>
      )}
    </div>
  );
}

function ComposeEditor({ open, onClose }: { open: EditorOpen; onClose: () => void }) {
  const read = useReadCompose();
  const write = useWriteCompose();
  const totp = useProdTotp();
  const [files, setFiles] = useState<ComposeFile[] | null>(null);
  const [path, setPath] = useState("");
  const [text, setText] = useState<Record<string, string>>({});
  const [confirm, setConfirm] = useState("");
  const [requested, setRequested] = useState(false);
  if (!requested) {
    setRequested(true);
    read.mutate(
      { node_id: open.node.node_id, project: open.project.name },
      {
        onSuccess: (data) => {
          setFiles(data.files);
          const p = open.proposal;
          const target = (p?.path && data.files.find((f) => f.path === p.path)) || data.files[0];
          if (target) {
            setPath(target.path);
            if (p) setText({ [target.path]: p.content });
          }
        },
      },
    );
  }
  const file = files?.find((f) => f.path === path);
  const value = file ? (text[path] ?? file.content) : "";
  const changed = !!file && value !== file.content;
  const blocked = !changed || (!!open.prodSlug && !prodConfirmed(open.prodSlug, confirm, totp));
  return (
    <Modal title={`Compose-bestand: ${open.project.name} op ${open.node.hostname}`} onClose={onClose} wide>
      <div className="space-y-3 text-sm">
        {read.isPending && <p className="text-slate-500">De agent leest de bestanden…</p>}
        {read.error && <Alert>{errText(read.error, "Lezen mislukte")}</Alert>}
        {write.data ? (
          <>
            <Alert kind="success">
              Opgeslagen; het project start opnieuw en wordt getest.{" "}
              <Link href={`/taken/detail?id=${write.data.id}`} className="font-medium underline">
                Volg de taak
              </Link>
              .
            </Alert>
            <div className="flex justify-end">
              <Button onClick={onClose}>Sluiten</Button>
            </div>
          </>
        ) : (
          files && (
            <>
              {files.length > 1 && (
                <Select value={path} onChange={(e) => setPath(e.target.value)}>
                  {files.map((f) => (
                    <option key={f.path} value={f.path}>
                      {f.path}
                      {f.env ? " (env)" : ""}
                    </option>
                  ))}
                </Select>
              )}
              {file && (
                <>
                  <p className="text-xs text-slate-500">
                    <span className="font-mono">{file.path}</span>
                    {file.secrets > 0 && <> · {file.secrets} wachtwoorden staan als «geheim-N»; laat die staan, de agent zet de echte waarden terug.</>}
                    {open.proposal && changed && <> · met het voorstel van de assistent, kijk het na</>}
                  </p>
                  <Textarea
                    className="h-96 font-mono text-xs"
                    spellCheck={false}
                    value={value}
                    onChange={(e) => setText({ ...text, [path]: e.target.value })}
                  />
                </>
              )}
              <p className="text-xs text-slate-500">Opslaan: de agent bewaart een kopie, controleert met docker compose config, haalt de images op, doet down en up -d en test. Lukt de test niet, dan komt het oude bestand terug.</p>
              {open.prodSlug && <ProdConfirm slug={open.prodSlug} value={confirm} onChange={setConfirm} />}
              {write.error && <Alert>{errText(write.error, "Opslaan mislukte")}</Alert>}
              <div className="flex justify-end gap-2">
                <Button variant="secondary" onClick={onClose}>
                  Annuleren
                </Button>
                <Button
                  disabled={blocked || write.isPending}
                  onClick={() =>
                    file &&
                    write.mutate({
                      node_id: open.node.node_id,
                      project: open.project.name,
                      path: file.path,
                      content: value,
                      old_sha256: file.sha256,
                      ...(open.prodSlug ? { confirm } : {}),
                    })
                  }
                >
                  Opslaan en opnieuw opstarten
                </Button>
              </div>
            </>
          )
        )}
      </div>
    </Modal>
  );
}

// ------------------------------------------------------------- Automatisch

const envNames: Record<string, string> = { lab: "Lab", test: "Test", prod: "Prod" };

function policyText(p: UpdatePolicy) {
  if (!p.enabled) return "uit";
  const what = [p.apt ? (p.apt_full ? "apt full-upgrade" : "apt upgrade") : "", p.docker ? "Docker" : ""].filter(Boolean).join(" en ");
  return `${what}, ${p.window}${p.reboot ? ", herstart als nodig" : ""}`;
}

function AutoTab() {
  const pol = useUpdatePolicies();
  const isAdmin = useIsAdmin();
  const [envEdit, setEnvEdit] = useState<{ env: string; policy: UpdatePolicy } | null>(null);
  const [clusterEdit, setClusterEdit] = useState<ClusterPolicy | null>(null);
  if (pol.isError) return <Alert>Het beleid laden mislukte.</Alert>;
  if (!pol.data) return null;
  return (
    <div className="space-y-6">
      <Alert kind="info">
        In het venster werkt ClusterForge elk cluster met een beleid dat aan staat één keer bij, met dezelfde volgorde als met de hand: back-upnodes, failover, master. Een cluster met een node die offline is of een te oude agent slaat het over. Mislukt een Docker-update, dan stelt de assistent vanzelf een fix voor (als die ingesteld is). Tijden in {pol.data.timezone}.
      </Alert>
      <Card title="Per omgeving">
        <div className="grid gap-3 sm:grid-cols-3">
          {pol.data.environments.map((e) => (
            <div key={e.environment} className="space-y-2 rounded-md border border-slate-200 p-3 dark:border-slate-800">
              <p className="flex items-center justify-between font-medium">
                {envNames[e.environment] ?? e.environment}
                <Badge tone={e.policy.enabled ? "green" : "slate"}>{e.policy.enabled ? "aan" : "uit"}</Badge>
              </p>
              <p className="text-xs text-slate-600 dark:text-slate-400">{policyText(e.policy)}</p>
              {e.updated_by && <p className="text-xs text-slate-500">door {e.updated_by}</p>}
              {isAdmin && (
                <Button variant="secondary" onClick={() => setEnvEdit({ env: e.environment, policy: e.policy })}>
                  Wijzigen
                </Button>
              )}
            </div>
          ))}
        </div>
      </Card>
      <Card title="Per cluster">
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-slate-500">
              <tr>
                <th className="py-1 pr-3 font-medium">Cluster</th>
                <th className="py-1 pr-3 font-medium">Beleid</th>
                <th className="py-1 pr-3 font-medium">Volgende venster</th>
                <th className="py-1 pr-3 font-medium">Laatst</th>
                <th className="py-1" />
              </tr>
            </thead>
            <tbody>
              {pol.data.clusters.map((c) => (
                <tr key={c.cluster_id} className="border-t border-slate-100 align-top dark:border-slate-800">
                  <td className="py-2 pr-3">
                    <Link href={`/clusters/detail?id=${c.cluster_id}`} className="font-medium hover:underline">
                      {c.name}
                    </Link>{" "}
                    <Badge tone={c.environment === "prod" ? "red" : "slate"}>{c.environment}</Badge>
                  </td>
                  <td className="py-2 pr-3 text-xs">
                    <span className="font-medium">{c.mode === "inherit" ? "zoals de omgeving" : c.mode === "own" ? "eigen beleid" : "uit"}</span>
                    <br />
                    <span className="text-slate-600 dark:text-slate-400">{policyText(c.effective)}</span>
                  </td>
                  <td className="py-2 pr-3 text-xs">{c.next_start ? new Date(c.next_start).toLocaleString("nl-BE") : "—"}</td>
                  <td className="py-2 pr-3 text-xs">
                    {c.last_run_at ? new Date(c.last_run_at).toLocaleString("nl-BE") : "—"}
                    {c.last_job_id && (
                      <>
                        {" "}
                        <Link href={`/taken/detail?id=${c.last_job_id}`} className="text-brand-700 hover:underline dark:text-sky-300">
                          taak
                        </Link>
                      </>
                    )}
                    {c.last_note && <p className="text-slate-500">{c.last_note}</p>}
                  </td>
                  <td className="py-2 text-right">
                    {isAdmin && (
                      <Button variant="secondary" onClick={() => setClusterEdit(c)}>
                        Wijzigen
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
      {envEdit && <EnvPolicyDialog env={envEdit.env} initial={envEdit.policy} onClose={() => setEnvEdit(null)} />}
      {clusterEdit && <ClusterPolicyDialog c={clusterEdit} onClose={() => setClusterEdit(null)} />}
    </div>
  );
}

function PolicyForm({ p, onChange, showEnabled = true }: { p: UpdatePolicy; onChange: (p: UpdatePolicy) => void; showEnabled?: boolean }) {
  const set = (patch: Partial<UpdatePolicy>) => onChange({ ...p, ...patch });
  return (
    <fieldset className="space-y-1 text-sm">
      {showEnabled && (
        <Check checked={p.enabled} onChange={(v) => set({ enabled: v })}>
          Automatisch bijwerken aan
        </Check>
      )}
      <Check checked={p.apt} onChange={(v) => set({ apt: v })}>
        Pakketten (apt)
      </Check>
      {p.apt && (
        <div className="ml-6 space-y-1">
          <Check checked={p.apt_full} onChange={(v) => set({ apt_full: v })}>
            full-upgrade
          </Check>
          <Check checked={p.apt_autoremove} onChange={(v) => set({ apt_autoremove: v })}>
            autoremove
          </Check>
        </div>
      )}
      <Check checked={p.docker} onChange={(v) => set({ docker: v })}>
        Docker Compose (met test en terugzetten)
      </Check>
      {p.docker && (
        <div className="ml-6">
          <Check checked={p.docker_prune} onChange={(v) => set({ docker_prune: v })}>
            ongebruikte images opruimen
          </Check>
        </div>
      )}
      <Check checked={p.reboot} onChange={(v) => set({ reboot: v })}>
        Herstarten als de node erom vraagt
      </Check>
      <label className="block pt-2">
        <span className="text-xs text-slate-500">Venster, zoals “zo 03:00-05:00”, “za,zo 02:00-04:00” of “dagelijks 03:00-05:00”</span>
        <Input className="font-mono" value={p.window} onChange={(e) => set({ window: e.target.value })} />
      </label>
    </fieldset>
  );
}

function EnvPolicyDialog({ env, initial, onClose }: { env: string; initial: UpdatePolicy; onClose: () => void }) {
  const save = useSetEnvPolicy();
  const totp = useProdTotp();
  const [p, setP] = useState(initial);
  const [confirm, setConfirm] = useState("");
  const prod = env === "prod";
  const blocked = prod && !prodConfirmed("prod", confirm, totp);
  return (
    <Modal title={`Automatisch bijwerken: ${envNames[env] ?? env}`} onClose={onClose}>
      <div className="space-y-4 text-sm">
        <PolicyForm p={p} onChange={setP} />
        {prod && <ProdConfirm slug="prod" value={confirm} onChange={setConfirm} />}
        {save.error && <Alert>{errText(save.error, "Bewaren mislukte")}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            disabled={blocked || save.isPending}
            onClick={() => save.mutate({ env: env as "lab" | "test" | "prod", body: { ...p, ...(prod ? { confirm } : {}) } }, { onSuccess: onClose })}
          >
            Bewaren
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function ClusterPolicyDialog({ c, onClose }: { c: ClusterPolicy; onClose: () => void }) {
  const save = useSetClusterPolicy();
  const totp = useProdTotp();
  const [mode, setMode] = useState(c.mode);
  const [p, setP] = useState<UpdatePolicy>(c.own ?? { ...c.effective, enabled: true });
  const [confirm, setConfirm] = useState("");
  const prod = c.environment === "prod";
  const blocked = prod && !prodConfirmed(c.slug, confirm, totp);
  return (
    <Modal title={`Automatisch bijwerken: ${c.name}`} onClose={onClose}>
      <div className="space-y-4 text-sm">
        <Select value={mode} onChange={(e) => setMode(e.target.value as ClusterPolicy["mode"])}>
          <option value="inherit">Zoals de omgeving ({c.environment})</option>
          <option value="own">Eigen beleid</option>
          <option value="off">Uit (nooit vanzelf)</option>
        </Select>
        {mode === "own" && <PolicyForm p={p} onChange={setP} showEnabled={false} />}
        {prod && <ProdConfirm slug={c.slug} value={confirm} onChange={setConfirm} />}
        {save.error && <Alert>{errText(save.error, "Bewaren mislukte")}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            disabled={blocked || save.isPending}
            onClick={() =>
              save.mutate(
                { id: c.cluster_id, body: { mode, ...(mode === "own" ? { policy: { ...p, enabled: true } } : {}), ...(prod ? { confirm } : {}) } },
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
