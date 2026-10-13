"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useEffect, useState, type ReactNode } from "react";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { Diff } from "@/components/gitops/bits";
import { Findings } from "@/components/databases/Findings";
import { ParamInput, paramHint } from "@/components/inventory/ParamInput";
import { formatBytes, LifecycleBadge, QueryState } from "@/components/inventory/bits";
import { Alert, Button, Card, cx, Field, Input, PageHeader, Select } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import {
  roleLabel,
  useAddComponent,
  useClusterComponents,
  useComponentTemplates,
  usePlanComponent,
  type ComponentFinding,
  type ComponentInput,
  type ComponentPlan,
  type ComponentPlanNode,
  type ComponentTemplate,
} from "@/lib/components";
import type { TemplateParam } from "@/lib/deploy";
import { useCluster, useIsAdmin, type ClusterDetail } from "@/lib/inventory";

export default function AddDatabasePage() {
  return (
    <Suspense>
      <AddDatabaseInner />
    </Suspense>
  );
}

function AddDatabaseInner() {
  const id = useSearchParams().get("id") ?? "";
  const cluster = useCluster(id);
  const templates = useComponentTemplates();
  const comps = useClusterComponents(id);
  const isAdmin = useIsAdmin();
  if (id === "") return <Alert>Geen cluster opgegeven.</Alert>;
  return (
    <QueryState q={cluster}>
      <QueryState q={templates}>
        {cluster.data && templates.data && comps.data && (
          <div className="space-y-6">
            <PageHeader
              title="Database toevoegen"
              description={
                <>
                  Op{" "}
                  <Link href={`/clusters/detail?id=${id}`} className="underline">
                    {cluster.data.name}
                  </Link>
                  : de database komt op de nodes van het cluster zelf, zodat de app haar naast zich heeft. ClusterForge toont
                  eerst wat het zou doen, per node.
                </>
              }
            />
            {!isAdmin ? (
              <Alert>Alleen een beheerder kan een database toevoegen.</Alert>
            ) : (
              <AddForm c={cluster.data} templates={templates.data} taken={comps.data.map((x) => x.engine)} />
            )}
          </div>
        )}
      </QueryState>
    </QueryState>
  );
}

// vip is het id van het VIP van de app, own voor een eigen VIP (met
// vipAddress) of leeg voor geen. PostgreSQL heeft altijd een eigen VIP.
type Form = { engine: string; nodes: string[]; arbiter: string; vip: string; vipAddress: string; params: Record<string, string> };

const isPG = (engine: string) => engine === "postgresql";

// defaultNodes zijn de actieve nodes van het cluster, hoogstens negen.
function defaultNodes(c: ClusterDetail) {
  return c.nodes
    .filter((n) => n.lifecycle === "active")
    .map((n) => n.id)
    .slice(0, 9);
}

function toInput(f: Form, t: ComponentTemplate | undefined): ComponentInput {
  const settings: Record<string, unknown> = {};
  for (const p of t?.params ?? []) {
    if (p.type === "secret") continue;
    const v = (f.params[p.name] ?? "").trim();
    if (v === "") continue;
    if (p.type === "bool") settings[p.name] = v === "true";
    else if (p.type === "int") settings[p.name] = /^-?\d+$/.test(v) ? Number(v) : v;
    else settings[p.name] = v;
  }
  const arbiter = f.nodes.length === 2 && f.arbiter !== "" ? f.arbiter : undefined;
  if (isPG(f.engine) || f.vip === "own") {
    const vip_address = f.vipAddress.trim();
    return { engine: f.engine, nodes: f.nodes, arbiter, vip_address: vip_address === "" ? undefined : vip_address, settings };
  }
  return { engine: f.engine, nodes: f.nodes, arbiter, vip_id: f.vip === "" ? undefined : f.vip, settings };
}

function paramValue(f: Form, p: TemplateParam) {
  return f.params[p.name] ?? (p.default === null ? "" : String(p.default));
}

function AddForm({ c, templates, taken }: { c: ClusterDetail; templates: ComponentTemplate[]; taken: string[] }) {
  const router = useRouter();
  const plan = usePlanComponent(c.id);
  const add = useAddComponent(c.id);
  const totp = useProdTotp();
  const [form, setForm] = useState<Form>(() => ({
    // De eerste soort die het cluster nog niet heeft.
    engine: (templates.find((x) => !taken.includes(x.engine)) ?? templates[0])?.engine ?? "mariadb",
    nodes: defaultNodes(c),
    arbiter: "",
    // Heeft het cluster één VIP, dan is dat het VIP van de app.
    vip: c.vips.length === 1 ? c.vips[0].id : "",
    vipAddress: "",
    params: {},
  }));
  const [confirm, setConfirm] = useState("");
  // Het plan hoort bij de invoer waarvoor het gemaakt is.
  const [planned, setPlanned] = useState<{ key: string; plan: ComponentPlan } | null>(null);
  const t = templates.find((x) => x.engine === form.engine);
  const prod = c.environment === "prod";

  // Elke wijziging maakt het plan oud; het nieuwe komt meteen.
  const input = toInput(form, t);
  const key = JSON.stringify(input);
  const { mutate } = plan;
  const replan = (body: ComponentInput) => mutate(body, { onSuccess: (p) => setPlanned({ key: JSON.stringify(body), plan: p }) });
  useEffect(() => {
    const body = JSON.parse(key) as ComponentInput;
    if (body.nodes.length === 0) return;
    const timer = setTimeout(() => mutate(body, { onSuccess: (p) => setPlanned({ key, plan: p }) }), 400);
    return () => clearTimeout(timer);
  }, [key, mutate]);
  const shown = planned?.key === key ? planned.plan : null;

  const setParam = (name: string, v: string) => setForm((f) => ({ ...f, params: { ...f.params, [name]: v } }));
  const toggle = (nodeId: string) =>
    setForm((f) => ({
      ...f,
      nodes: f.nodes.includes(nodeId) ? f.nodes.filter((n) => n !== nodeId) : [...f.nodes, nodeId],
      arbiter: f.arbiter === nodeId ? "" : f.arbiter,
    }));
  const planErr = plan.error instanceof ApiError && JSON.stringify(plan.variables) === key ? plan.error : null;
  const addErr = add.error instanceof ApiError ? add.error : null;
  const others = c.nodes.filter((n) => !form.nodes.includes(n.id));
  const pg = isPG(form.engine);
  const own = pg || form.vip === "own";

  return (
    <div className="space-y-6">
      <Card title="Database">
        <div className="space-y-4 text-sm">
          <Field label="Soort" htmlFor="engine">
            <Select
              id="engine"
              value={form.engine}
              onChange={(e) => setForm((f) => ({ ...f, engine: e.target.value, params: {} }))}
            >
              {templates.map((x) => (
                <option key={x.engine} value={x.engine}>
                  {x.title}
                </option>
              ))}
            </Select>
          </Field>
          {t && <p className="text-slate-600 dark:text-slate-400">{t.description}</p>}
          {pg && (
            <p className="text-slate-500">
              Daarna maak je databases, gebruikers en dumps bij het cluster, en verhuis je een database van een andere PostgreSQL
              bij Databases.
            </p>
          )}
        </div>
      </Card>

      <Card title="Nodes">
        <div className="space-y-4 text-sm">
          <p className="text-slate-600 dark:text-slate-400">
            {pg
              ? "Elke gekozen node krijgt alle data: één is de primary en schrijft, de andere zijn replica's die alles meteen krijgen. Kies er drie, liefst een oneven aantal. Met twee nodes komt er een scheidsrechter bij: een derde node die alleen in etcd meestemt."
              : "Elke gekozen node krijgt alle data en kan schrijven. Kies er drie of meer, liefst een oneven aantal. Met twee nodes komt er een scheidsrechter bij: een derde node die meestemt zonder data."}
          </p>
          <ul className="space-y-1">
            {c.nodes.map((n) => (
              <li key={n.id}>
                <label className="inline-flex items-center gap-2">
                  <input type="checkbox" checked={form.nodes.includes(n.id)} onChange={() => toggle(n.id)} />
                  <span className="font-medium">{n.hostname}</span>
                  {n.primary_ip && <span className="font-mono text-slate-500">{n.primary_ip}</span>}
                  {n.lifecycle !== "active" && <LifecycleBadge lifecycle={n.lifecycle} />}
                </label>
              </li>
            ))}
          </ul>
          {form.nodes.length === 2 && (
            <Field
              label="Scheidsrechter"
              htmlFor="arbiter"
              hint={`Een node van dit cluster die niet bij de twee hoort; hij krijgt ${pg ? "alleen etcd" : "garbd"}.`}
            >
              <Select id="arbiter" value={form.arbiter} onChange={(e) => setForm((f) => ({ ...f, arbiter: e.target.value }))}>
                <option value="">Geen</option>
                {others.map((n) => (
                  <option key={n.id} value={n.id}>
                    {n.hostname}
                  </option>
                ))}
              </Select>
            </Field>
          )}
        </div>
      </Card>

      <Card title="VIP">
        <div className="space-y-4 text-sm">
          {pg ? (
            <p className="text-slate-600 dark:text-slate-400">
              PostgreSQL krijgt een eigen VIP: een vrij adres in het netwerk van de nodes. keepalived zet het altijd op de
              primary. Valt die uit, dan maakt Patroni een replica primary en gaat het VIP mee, binnen een halve minuut. De app
              verbindt met dit VIP.
            </p>
          ) : (
            <p className="text-slate-600 dark:text-slate-400">
              De app verbindt met het VIP, zodat ze altijd naar één node schrijft. keepalived zet het VIP dan alleen op een node
              waarvan de database mag schrijven: valt MariaDB daar uit of loopt hij achter, dan gaat het VIP naar een andere node.
              Kies het VIP van de app, zodat app en database één adres delen, of een eigen VIP voor de database.
            </p>
          )}
          {!pg && (
            <Field label="VIP" htmlFor="vip">
              <Select id="vip" value={form.vip} onChange={(e) => setForm((f) => ({ ...f, vip: e.target.value }))}>
                <option value="">Geen VIP</option>
                {c.vips.map((v) => (
                  <option key={v.id} value={v.id}>
                    {v.address} (VIP van de app{v.owner_hostname && `, nu op ${v.owner_hostname}`})
                  </option>
                ))}
                <option value="own">Een eigen VIP voor de database</option>
              </Select>
            </Field>
          )}
          {own && (
            <Field
              label="Eigen VIP"
              htmlFor="vip-address"
              hint="Een vrij IPv4-adres in het netwerk van de nodes, zoals 10.0.20.190."
            >
              <Input
                id="vip-address"
                className="font-mono"
                value={form.vipAddress}
                onChange={(e) => setForm((f) => ({ ...f, vipAddress: e.target.value }))}
                placeholder="10.0.20.190"
              />
            </Field>
          )}
        </div>
      </Card>

      {t && t.params.some((p) => p.type !== "secret") && (
        <Card title="Instellingen">
          <div className="grid gap-4 sm:grid-cols-2">
            {t.params
              .filter((p) => p.type !== "secret")
              .map((p) => (
                <Field key={p.name} label={p.label || p.name} htmlFor={`f-params.${p.name}`} hint={paramHint(p)}>
                  <ParamInput p={p} value={paramValue(form, p)} onChange={(v) => setParam(p.name, v)} />
                </Field>
              ))}
          </div>
        </Card>
      )}

      <Card title="Plan">
        {form.nodes.length === 0 ? (
          <p className="text-sm text-slate-500">Kies de nodes.</p>
        ) : planErr ? (
          <Alert>{planErr.message}</Alert>
        ) : !shown ? (
          <p className="text-sm text-slate-500">ClusterForge vraagt de nodes wat er nu staat…</p>
        ) : (
          <PlanView p={shown} />
        )}
      </Card>

      {shown && (
        <Card>
          <div className="space-y-4 text-sm">
            {shown.blocked ? (
              <Alert>Dit kan nog niet: los eerst op wat hierboven rood staat.</Alert>
            ) : (
              <p>
                {isPG(shown.engine) ? (
                  <>
                    ClusterForge zet eerst etcd op alle {shown.nodes.length} nodes, en dan PostgreSQL met Patroni op de datanodes,
                    een voor een: de eerste wordt de primary, de andere halen een kopie en worden replica. Daarna komt het eigen
                    VIP in keepalived op elke datanode, en ClusterForge kijkt of het op de primary staat.
                  </>
                ) : (
                  <>
                    ClusterForge zet {shown.title} op {shown.nodes.length} nodes, een voor een. Een node die al draait, blijft
                    draaien tot zijn beurt; dan herstart MariaDB er een keer.
                    {shown.vip?.own &&
                      " Daarna komt het eigen VIP in keepalived op elke datanode, en ClusterForge kijkt of het op een node met de database staat."}
                    {shown.vip &&
                      !shown.vip.own &&
                      ` Daarna komt de controle van de database in keepalived, node voor node, met ${shown.vip.owner || "de node met het VIP"} laatst; het VIP verhuist daarbij niet.`}
                  </>
                )}{" "}
                De app verbindt nog met de oude database tot je haar verhuist (een latere stap).
              </p>
            )}
            {prod && !shown.blocked && <ProdConfirm slug={c.slug} value={confirm} onChange={setConfirm} />}
            {addErr && (
              <Alert>
                {addErr.message}
                {addErr.code === "plan_stale" && " Het plan hierboven is bijgewerkt."}
              </Alert>
            )}
            <div className="flex justify-end gap-2">
              <Link href={`/clusters/detail?id=${c.id}`}>
                <Button variant="secondary">Annuleren</Button>
              </Link>
              <Button
                disabled={shown.blocked || add.isPending || (prod && !prodConfirmed(c.slug, confirm, totp))}
                onClick={async () => {
                  const j = await add
                    .mutateAsync({ ...input, hash: shown.hash, confirm: prod ? confirm : undefined })
                    .catch((e) => {
                      if (e instanceof ApiError && e.code === "plan_stale") replan(input);
                      return null;
                    });
                  if (j) router.push(`/taken/detail?id=${j.id}`);
                }}
              >
                {add.isPending ? "Bezig…" : "Database toevoegen"}
              </Button>
            </div>
          </div>
        </Card>
      )}
    </div>
  );
}

const pct = (n: number | null) => (n === null ? "" : `${Math.round(n)}%`);

function PlanView({ p }: { p: ComponentPlan }) {
  const general = p.findings.filter((f) => !f.node_id);
  return (
    <div className="space-y-4 text-sm">
      {general.length > 0 && <Findings list={general} />}
      {p.vip && p.vip.own && (
        <p>
          Het eigen VIP <span className="font-mono">{p.vip.address}</span> (VRRP-id {p.vip.vrid}) komt op{" "}
          {p.nodes
            .filter((n) => n.role === "data")
            .map((n) => n.hostname)
            .join(", ")}
          . ClusterForge zet er een vrrp_instance voor in <span className="font-mono">/etc/keepalived/conf.d</span>;
          keepalived.conf krijgt alleen een include van die map, of een nieuw bestand als er nog geen is.
        </p>
      )}
      {p.vip && !p.vip.own && (
        <p>
          Het VIP <span className="font-mono">{p.vip.address}</span>
          {p.vip.owner ? ` staat nu op ${p.vip.owner} en` : ""} volgt de database op{" "}
          {p.nodes
            .filter((n) => n.vip_instance !== "")
            .map((n) => n.hostname)
            .join(", ")}
          . ClusterForge zet een track_script <span className="font-mono">cf_db</span> in keepalived.conf; de rest van het bestand
          blijft zoals het is.
        </p>
      )}
      {p.nodes.map((n) => (
        <NodeBlock
          key={n.node_id}
          n={n}
          server={isPG(p.engine) ? "PostgreSQL" : "MariaDB"}
          findings={p.findings.filter((f) => f.node_id === n.node_id)}
        />
      ))}
      <p className="text-slate-500">
        {isPG(p.engine)
          ? `${p.slug} wordt de naam van het Patroni-cluster. De eerste node maakt het; de andere halen een kopie bij de primary.`
          : `${p.slug} wordt de naam van het Galera-cluster. De eerste node begint het; de andere halen alle data bij een node die al meedoet.`}{" "}
        Instellingen:{" "}
        {Object.entries(p.params)
          .filter(([k]) => k !== "vip")
          .map(([k, v]) => `${k} ${String(v)}`)
          .join(", ")}
        .
      </p>
    </div>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt className="text-xs font-medium tracking-wide text-slate-500 uppercase">{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

// NodeBlock is één node van het plan: wat er nu is, wat de database vraagt
// en wat er gebeurt.
function NodeBlock({ n, server, findings }: { n: ComponentPlanNode; server: string; findings: ComponentFinding[] }) {
  const [open, setOpen] = useState(false);
  const [diff, setDiff] = useState(false);
  const tight = n.free_after_pct !== null && n.free_after_pct < 15;
  return (
    <div className="space-y-3 rounded-md border border-slate-200 p-3 dark:border-slate-800">
      <p>
        <span className="font-medium">{n.hostname}</span>{" "}
        <span className="text-slate-500">
          {roleLabel[n.role] ?? n.role}
          {n.role === "data" && ` ${n.index}`} · <span className="font-mono">{n.address}</span>
        </span>
      </p>
      <dl className="grid grid-cols-2 gap-x-4 gap-y-2 lg:grid-cols-4">
        <Fact label="Systeem">{n.os || "onbekend"}</Fact>
        <Fact label="Geheugen">
          {n.memory_bytes > 0 ? (
            <>
              {formatBytes(n.memory_bytes)}
              {n.memory_used_pct !== null && `, ${pct(n.memory_used_pct)} in gebruik`}
              {n.need_bytes > 0 && (
                <span className={cx("block text-slate-500", tight && "text-amber-700 dark:text-amber-300")}>
                  + {formatBytes(n.need_bytes)} voor {server}
                  {n.free_after_pct !== null && `, daarna ${pct(n.free_after_pct)} vrij`}
                </span>
              )}
            </>
          ) : (
            "onbekend"
          )}
        </Fact>
        <Fact label="Schijf">
          {n.disk_free_bytes !== null ? (
            <>
              {formatBytes(n.disk_free_bytes)} vrij <span className="text-slate-500">op {n.disk_mount}</span>
            </>
          ) : n.role === "data" ? (
            "onbekend"
          ) : (
            "niet nodig"
          )}
        </Fact>
        <Fact label="Proxmox">
          {n.pve_node ? (
            <>
              {n.pve_node}, {n.pve_type === "lxc" ? "CT" : "VM"} {n.vmid}
            </>
          ) : (
            <span className="text-slate-500">niet gekoppeld</span>
          )}
        </Fact>
      </dl>
      {n.existing && <p className="text-slate-600 dark:text-slate-400">Nu: {n.existing}</p>}
      {findings.length > 0 && <Findings list={findings} />}
      <div className="flex flex-wrap gap-x-4 gap-y-1">
        <button type="button" className="text-brand-700 hover:underline dark:text-sky-300" onClick={() => setOpen((o) => !o)}>
          {open ? "Stappen verbergen" : `${n.steps.length} stappen`}
        </button>
        {n.vip_diff && (
          <button type="button" className="text-brand-700 hover:underline dark:text-sky-300" onClick={() => setDiff((o) => !o)}>
            {diff ? "keepalived.conf verbergen" : `keepalived.conf (${n.vip_instance})`}
          </button>
        )}
      </div>
      {diff && n.vip_diff && <Diff text={n.vip_diff} />}
      {open && (
        <ol className="list-decimal space-y-0.5 pl-5 text-slate-600 dark:text-slate-400">
          {n.steps.map((s, i) => (
            <li key={i}>{s}</li>
          ))}
        </ol>
      )}
    </div>
  );
}
