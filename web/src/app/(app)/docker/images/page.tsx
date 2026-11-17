"use client";

import { Suspense, useMemo, useState } from "react";
import { Modal } from "@/components/Modal";
import { errText, FilterChips, inScope, NodeSections, rowsFrom, ScopeBar, useDockerAll, useDockerRun, useScope } from "@/components/docker/common";
import { ago, formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Field, Input, PageHeader, cx } from "@/components/ui";
import { containerName, dockerGet, shortId, useDockerHosts, type ContainerSummary, type DockerCall, type DockerHost, type ImageSummary } from "@/lib/docker";
import { useIsAdmin } from "@/lib/inventory";

export default function Page() {
  return (
    <Suspense>
      <Images />
    </Suspense>
  );
}

type Row = { host: DockerHost; im: ImageSummary; tag: string; used: string[] };

// Een image met meer tags wordt één rij per tag; zonder tag één rij <none>.
function rowsOf(host: DockerHost, list: ImageSummary[], containers: ContainerSummary[]): Row[] {
  const out: Row[] = [];
  for (const im of list) {
    const used = containers.filter((c) => c.ImageID === im.Id).map(containerName);
    const tags = (im.RepoTags ?? []).filter((t) => t !== "<none>:<none>");
    if (tags.length === 0) out.push({ host, im, tag: "", used });
    for (const t of tags) out.push({ host, im, tag: t, used });
  }
  return out;
}

const filters = [
  { key: "", label: "Alle" },
  { key: "used", label: "In gebruik" },
  { key: "unused", label: "Ongebruikt" },
  { key: "dangling", label: "Zonder tag" },
  { key: "update", label: "Nieuwer beschikbaar" },
] as const;
type FilterKey = (typeof filters)[number]["key"];

function Images() {
  const isAdmin = useIsAdmin();
  const hostsQ = useDockerHosts();
  const imagesQ = useDockerAll<ImageSummary[]>("/images/json", undefined, 30_000);
  const containersQ = useDockerAll<ContainerSummary[]>("/containers/json", { all: "1" }, 30_000);
  const [scope, setScope] = useScope();
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState<FilterKey>("");
  // updates: per node/tag de uitkomst van de controle bij de registry.
  const [updates, setUpdates] = useState<Record<string, "nieuw" | "actueel" | string>>({});
  const [checking, setChecking] = useState<string | null>(null);
  const [pullFor, setPullFor] = useState<DockerHost | null>(null);
  const { run, modal } = useDockerRun();
  const need = hostsQ.data?.protocol ?? 46;
  const hosts = useMemo(() => (hostsQ.data?.hosts ?? []).filter((h) => inScope(h, scope)), [hostsQ.data, scope]);

  const containersBy = new Map((containersQ.data ?? []).map((r) => [r.node_id, r.data ?? []]));
  const { rows, errors } = rowsFrom(imagesQ.data, hosts, (h, list) => rowsOf(h, list, containersBy.get(h.node_id) ?? []));
  const key = (r: Row) => `${r.host.node_id}/${r.tag || r.im.Id}`;
  const q = search.trim().toLowerCase();
  const test = (r: Row, f: FilterKey) =>
    f === "" ||
    (f === "used" && r.used.length > 0) ||
    (f === "unused" && r.used.length === 0) ||
    (f === "dangling" && !r.tag) ||
    (f === "update" && updates[key(r)] === "nieuw");
  const shown = rows.filter((r) => test(r, filter) && (!q || `${r.tag} ${r.im.Id} ${r.host.hostname} ${r.used.join(" ")}`.toLowerCase().includes(q)));

  // check vraagt per tag aan de registry (via Docker op de node) welke
  // digest de tag nu heeft, en vergelijkt met wat de node heeft.
  async function check(h: DockerHost) {
    setChecking(h.node_id);
    const mine = rows.filter((r) => r.host.node_id === h.node_id && r.tag);
    for (const r of mine) {
      let res: string;
      try {
        const d = await dockerGet<{ Descriptor: { digest: string } }>(h.node_id, `/distribution/${r.tag}/json`);
        const have = (r.im.RepoDigests ?? []).some((x) => x.endsWith("@" + d.Descriptor.digest));
        res = have ? "actueel" : "nieuw";
      } catch (e) {
        res = errText(e);
      }
      const k = key(r);
      setUpdates((u) => ({ ...u, [k]: res }));
    }
    setChecking(null);
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title="Images"
        description="De images op elke node, met welke containers ze gebruiken. Controleren op updates vraagt per tag de registry om de nieuwste digest; een nieuw image halen verandert een draaiende container nog niet (dat doet Docker → Stacks of Updates)."
      />
      <ScopeBar hosts={hostsQ.data?.hosts ?? []} scope={scope} setScope={setScope} search={search} setSearch={setSearch}>
        <FilterChips options={filters.map((f) => ({ ...f, count: rows.filter((r) => test(r, f.key)).length }))} value={filter} onChange={setFilter} />
      </ScopeBar>
      {(imagesQ.isError || containersQ.isError) && <Alert>{errText(imagesQ.error ?? containersQ.error)}</Alert>}
      {imagesQ.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      <NodeSections
        hosts={hosts}
        rows={shown}
        errors={errors}
        need={need}
        filtered={!!q || !!filter}
        header={(h) =>
          isAdmin && (
            <>
              <Button variant="secondary" onClick={() => void check(h)} disabled={checking !== null}>
                {checking === h.node_id ? "Controleren…" : "Op updates controleren"}
              </Button>
              <Button variant="secondary" onClick={() => setPullFor(h)}>
                Image halen
              </Button>
              {rows.some((r) => r.host.node_id === h.node_id && r.used.length === 0) && (
                <Button
                  variant="secondary-danger"
                  onClick={() =>
                    void run(h.node_id, h.hostname, [{ method: "POST", path: "/images/prune", query: { filters: JSON.stringify({ dangling: ["false"] }) } }], {
                      what: `Alle images die geen container gebruikt van ${h.hostname} verwijderen.`,
                    })
                  }
                >
                  Ongebruikte weg
                </Button>
              )}
            </>
          )
        }
      >
        {(h, rs) => (
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Image</th>
                <th className={cx(thClass, "hidden sm:table-cell")}>Id</th>
                <th className={cx(thClass, "text-right")}>Grootte</th>
                <th className={cx(thClass, "hidden md:table-cell")}>Gemaakt</th>
                <th className={thClass}>Gebruikt door</th>
                {isAdmin && <th className={thClass} />}
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {rs
                .sort((a, b) => (a.tag || "~").localeCompare(b.tag || "~"))
                .map((r) => {
                  const u = updates[key(r)];
                  return (
                    <tr key={key(r)}>
                      <td className={cx(tdClass, "font-mono text-xs break-all")}>
                        {r.tag || <span className="text-slate-500">&lt;geen tag&gt;</span>}
                        {u === "nieuw" && (
                          <span className="ml-2">
                            <Badge tone="amber">nieuwer beschikbaar</Badge>
                          </span>
                        )}
                        {u === "actueel" && (
                          <span className="ml-2">
                            <Badge tone="green">actueel</Badge>
                          </span>
                        )}
                        {u && u !== "nieuw" && u !== "actueel" && <div className="mt-0.5 font-sans text-amber-700 dark:text-amber-400">{u}</div>}
                      </td>
                      <td className={cx(tdClass, "hidden font-mono text-xs sm:table-cell")}>{shortId(r.im.Id)}</td>
                      <td className={cx(tdClass, "text-right text-xs whitespace-nowrap tabular-nums")}>{formatBytes(r.im.Size)}</td>
                      <td className={cx(tdClass, "hidden text-xs md:table-cell")}>{ago(new Date(r.im.Created * 1000).toISOString())}</td>
                      <td className={cx(tdClass, "text-xs")}>{r.used.length ? r.used.join(", ") : <Badge>ongebruikt</Badge>}</td>
                      {isAdmin && (
                        <td className={cx(tdClass, "text-right whitespace-nowrap")}>
                          {r.tag && (
                            <button
                              type="button"
                              className="rounded px-1.5 py-0.5 text-xs text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
                              onClick={() => void run(h.node_id, h.hostname, [pullCall(r.tag)])}
                            >
                              Opnieuw halen
                            </button>
                          )}
                          {r.used.length === 0 && (
                            <button
                              type="button"
                              className="rounded px-1.5 py-0.5 text-xs text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-slate-800"
                              onClick={() =>
                                void run(h.node_id, h.hostname, [{ method: "DELETE", path: `/images/${r.tag || r.im.Id}` }], {
                                  what: `Image ${r.tag || shortId(r.im.Id)} verwijderen van ${h.hostname}.`,
                                })
                              }
                            >
                              Verwijderen
                            </button>
                          )}
                        </td>
                      )}
                    </tr>
                  );
                })}
            </tbody>
          </table>
        )}
      </NodeSections>
      {pullFor && <PullModal host={pullFor} onClose={() => setPullFor(null)} run={run} />}
      {modal}
    </div>
  );
}

// pullCall haalt ref (naam[:tag] of naam@digest) met docker pull.
function pullCall(ref: string): DockerCall {
  if (ref.indexOf("@") > 0) return { method: "POST", path: "/images/create", query: { fromImage: ref } };
  const slash = ref.lastIndexOf("/");
  const colon = ref.lastIndexOf(":");
  if (colon > slash) return { method: "POST", path: "/images/create", query: { fromImage: ref.slice(0, colon), tag: ref.slice(colon + 1) } };
  return { method: "POST", path: "/images/create", query: { fromImage: ref, tag: "latest" } };
}

function PullModal({ host, onClose, run }: { host: DockerHost; onClose: () => void; run: ReturnType<typeof useDockerRun>["run"] }) {
  const [ref, setRef] = useState("");
  const valid = /^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,254}$/.test(ref.trim());
  return (
    <Modal title={`Image halen op ${host.hostname}`} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={async (e) => {
          e.preventDefault();
          if (valid && (await run(host.node_id, host.hostname, [pullCall(ref.trim())]))) onClose();
        }}
      >
        <Field label="Image" htmlFor="ref" hint="Zoals nginx:1.29, ghcr.io/eigenaar/app:2.1 of mariadb@sha256:…; zonder tag is het latest. Een privé-registry werkt als de node er met docker login aangemeld is.">
          <Input id="ref" autoFocus value={ref} onChange={(e) => setRef(e.target.value)} placeholder="nginx:1.29" />
        </Field>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" disabled={!valid}>
            Halen
          </Button>
        </div>
      </form>
    </Modal>
  );
}
