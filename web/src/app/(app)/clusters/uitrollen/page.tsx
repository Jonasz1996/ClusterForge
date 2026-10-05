"use client";

import { keepPreviousData, useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { slugify } from "@/components/inventory/ClusterForm";
import { CopyBlock } from "@/components/inventory/InstallAgent";
import { QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Button, Card, cx, Input, Label, PageHeader, Select, Textarea } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { mib, planDeployment, useDeploy, useTemplates, type DeployInput, type Template, type TemplateParam } from "@/lib/deploy";
import { environments, useIsAdmin, type Environment } from "@/lib/inventory";
import { useProxmoxConnections, useProxmoxResources } from "@/lib/proxmox";

export default function DeployPage() {
  return (
    <Suspense>
      <DeployInner />
    </Suspense>
  );
}

type Form = {
  template: string;
  name: string;
  slug: string;
  slugTouched: boolean;
  environment: Environment;
  description: string;
  params: Record<string, string>;
  proxmoxId: string;
  imageVmid: string;
  storage: string;
  bridge: string;
  vlan: string;
  network: "dhcp" | "static";
  firstIp: string;
  gateway: string;
  dns: string;
  sshKeys: string;
  // null: het adres van deze server.
  serverUrl: string | null;
};

const emptyForm: Form = {
  template: "",
  name: "",
  slug: "",
  slugTouched: false,
  environment: "lab",
  description: "",
  params: {},
  proxmoxId: "",
  imageVmid: "",
  storage: "",
  bridge: "vmbr0",
  vlan: "",
  network: "static",
  firstIp: "",
  gateway: "",
  dns: "",
  sshKeys: "",
  serverUrl: null,
};

// paramValue is de ingevulde waarde, of de standaard van de template.
function paramValue(f: Form, p: TemplateParam) {
  return f.params[p.name] ?? p.default ?? (p.type === "bool" ? "false" : "");
}

// toInput zet het formulier om naar de aanvraag; lege optionele waarden
// blijven weg, zodat de server ze kiest.
function toInput(f: Form, t: Template | undefined): DeployInput {
  const params: Record<string, unknown> = {};
  for (const p of t?.params ?? []) {
    const v = paramValue(f, p).trim();
    if (p.type === "bool") params[p.name] = v === "true";
    else if (v === "") continue;
    else if (p.type === "int") params[p.name] = /^-?\d+$/.test(v) ? Number(v) : v;
    else params[p.name] = v;
  }
  return {
    template: f.template,
    cluster: { name: f.name.trim(), slug: f.slug.trim(), environment: f.environment, description: f.description.trim() },
    params,
    target: {
      proxmox_id: f.proxmoxId,
      image_vmid: Number(f.imageVmid) || 0,
      storage: f.storage,
      bridge: f.bridge.trim(),
      vlan: f.vlan.trim() === "" ? null : Number(f.vlan),
      network: f.network,
      first_ip: f.network === "static" ? f.firstIp.trim() : "",
      gateway: f.network === "static" ? f.gateway.trim() : "",
      dns: f.network === "static" ? f.dns.split(/[\s,]+/).filter(Boolean) : [],
      ssh_keys: f.sshKeys,
    },
    server_url: (f.serverUrl ?? "").trim(),
  };
}

function useDebounced<T>(value: T, ms: number) {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return v;
}

// fieldId is het id van het invoerveld bij een veld uit een foutmelding.
const fieldId = (field: string) => `f-${field}`;

function DeployInner() {
  const search = useSearchParams();
  const router = useRouter();
  const isAdmin = useIsAdmin();
  const templates = useTemplates();
  const conns = useProxmoxConnections();
  const [form, setForm] = useState<Form>(emptyForm);
  const [error, setError] = useState<ApiError | null>(null);
  const deploy = useDeploy();

  // Wat niet gekozen is, krijgt een startwaarde: de template uit de URL, de
  // eerste Proxmox-koppeling, de eerste golden image en het adres van deze
  // server.
  const template =
    templates.data?.find((t) => t.name === (form.template || search.get("template"))) ?? templates.data?.[0];
  const proxmoxId = form.proxmoxId || conns.data?.items[0]?.id || "";
  const resources = useProxmoxResources(proxmoxId);
  const images = useMemo(
    () => (resources.data?.guests ?? []).filter((g) => g.type === "qemu" && g.template).sort((a, b) => a.vmid - b.vmid),
    [resources.data],
  );
  const storages = useMemo(() => {
    const seen = new Map<string, boolean>();
    for (const s of resources.data?.storages ?? []) {
      if (s.content === "" || s.content.split(",").includes("images")) seen.set(s.name, s.shared);
    }
    return [...seen.entries()].sort(([a], [b]) => a.localeCompare(b));
  }, [resources.data]);

  const imageVmid = form.imageVmid || (images[0] ? String(images[0].vmid) : "");
  const origin = typeof window === "undefined" ? "https://clusterforge.example" : window.location.origin;
  const eff: Form = { ...form, template: template?.name ?? "", proxmoxId, imageVmid, serverUrl: form.serverUrl ?? origin };

  const set = <K extends keyof Form>(k: K, v: Form[K]) => setForm((f) => ({ ...f, [k]: v }));
  const setParam = (name: string, v: string) => setForm((f) => ({ ...f, params: { ...f.params, [name]: v } }));

  const input = toInput(eff, template);
  const ready = template !== undefined && form.name.trim() !== "" && form.slug !== "" && proxmoxId !== "" && imageVmid !== "";
  const planInput = useDebounced(ready ? JSON.stringify(input) : "", 600);
  const plan = useQuery({
    queryKey: ["deploy-plan", planInput],
    queryFn: async () => planDeployment(JSON.parse(planInput) as DeployInput),
    enabled: isAdmin && planInput !== "",
    retry: false,
    staleTime: 10_000,
    placeholderData: keepPreviousData,
  });
  const planError = plan.error instanceof ApiError ? plan.error : null;
  // Een fout van het voorbeeld markeert het veld al tijdens het invullen; een
  // fout bij het uitrollen gaat voor.
  const fieldError = error?.field ? error : planError?.field ? planError : null;

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      const res = await deploy.mutateAsync(input);
      router.push(`/taken/detail?id=${res.job.id}`);
    } catch (err) {
      const ae = err instanceof ApiError ? err : new ApiError(0, "unknown", "Uitrollen mislukt");
      setError(ae);
      const el = ae.field ? document.getElementById(fieldId(ae.field)) : null;
      if (el) {
        el.scrollIntoView({ block: "center", behavior: "smooth" });
        el.focus({ preventScroll: true });
      } else {
        window.scrollTo({ top: 0, behavior: "smooth" });
      }
    }
  }

  if (!isAdmin) return <Alert>Alleen een beheerder kan clusters uitrollen.</Alert>;

  const bad = (field: string) => (fieldError?.field === field ? "border-red-500 ring-2 ring-red-500/20" : undefined);
  const noConnections = conns.data && conns.data.items.length === 0;

  return (
    <form onSubmit={submit} className="space-y-6">
      <div className="text-sm">
        <Link href="/clusters" className="text-slate-500 hover:underline">
          ← Clusters
        </Link>
      </div>
      <PageHeader
        title="Cluster uitrollen"
        description="ClusterForge kloont de VM's uit een golden image in Proxmox, installeert de software van de template en controleert of het cluster werkt."
      />
      {error && !error.field && <Alert>{error.message}</Alert>}
      {error?.field && <Alert>Controleer het gemarkeerde veld: {error.message}</Alert>}

      <QueryState q={templates}>
        <Card title="Template">
          <div className="grid gap-4 sm:grid-cols-2">
            <FieldRow error={fieldError} field="template" label="Template">
              <Select
                id={fieldId("template")}
                value={eff.template}
                onChange={(e) => setForm((f) => ({ ...f, template: e.target.value, params: {} }))}
              >
                {templates.data?.map((t) => (
                  <option key={t.name} value={t.name}>
                    {t.title} ({t.version})
                  </option>
                ))}
              </Select>
            </FieldRow>
          </div>
          {template && <p className="mt-3 text-sm text-slate-600 dark:text-slate-400">{template.description}</p>}
        </Card>
      </QueryState>

      <Card title="Cluster">
        <div className="grid gap-4 sm:grid-cols-2">
          <FieldRow error={fieldError} field="cluster.name" label="Naam">
            <Input
              id={fieldId("cluster.name")}
              className={bad("cluster.name")}
              required
              value={form.name}
              placeholder="Webservers"
              onChange={(e) => {
                const name = e.target.value;
                setForm((f) => ({ ...f, name, slug: f.slugTouched ? f.slug : slugify(name).slice(0, 40) }));
              }}
            />
          </FieldRow>
          <FieldRow error={fieldError} field="cluster.slug" label="Slug" hint={`De nodes heten ${form.slug || "<slug>"}-01, ${form.slug || "<slug>"}-02, …`}>
            <Input
              id={fieldId("cluster.slug")}
              className={bad("cluster.slug")}
              required
              value={form.slug}
              maxLength={40}
              onChange={(e) => setForm((f) => ({ ...f, slug: e.target.value, slugTouched: true }))}
            />
          </FieldRow>
          <FieldRow error={fieldError} field="cluster.environment" label="Omgeving">
            <Select id={fieldId("cluster.environment")} value={form.environment} onChange={(e) => set("environment", e.target.value as Environment)}>
              {environments.map((x) => (
                <option key={x.value} value={x.value}>
                  {x.label}
                </option>
              ))}
            </Select>
          </FieldRow>
          <FieldRow error={fieldError} field="cluster.description" label="Beschrijving">
            <Input id={fieldId("cluster.description")} value={form.description} onChange={(e) => set("description", e.target.value)} />
          </FieldRow>
        </div>
      </Card>

      {template && template.params.length > 0 && (
        <Card title="Instellingen">
          <div className="grid gap-4 sm:grid-cols-2">
            {template.params.map((p) => (
              <FieldRow error={fieldError} key={p.name} field={`params.${p.name}`} label={p.label} hint={paramHint(p)}>
                <ParamInput p={p} value={paramValue(form, p)} onChange={(v) => setParam(p.name, v)} className={bad(`params.${p.name}`)} />
              </FieldRow>
            ))}
          </div>
        </Card>
      )}

      <Card title="Proxmox">
        {noConnections ? (
          <Alert kind="info">
            Er is nog geen Proxmox gekoppeld.{" "}
            <Link href="/proxmox" className="font-medium underline">
              Koppel Proxmox
            </Link>{" "}
            en kom dan terug.
          </Alert>
        ) : (
          <div className="space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <FieldRow error={fieldError} field="target.proxmox_id" label="Proxmox">
                <Select
                  id={fieldId("target.proxmox_id")}
                  value={proxmoxId}
                  onChange={(e) => setForm((f) => ({ ...f, proxmoxId: e.target.value, imageVmid: "", storage: "" }))}
                >
                  {conns.data?.items.map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name}
                    </option>
                  ))}
                </Select>
              </FieldRow>
              <FieldRow error={fieldError} field="target.image_vmid" label="Golden image" hint="Een VM-template met cloud-init, qemu-guest-agent en cf-agent.">
                <Select
                  id={fieldId("target.image_vmid")}
                  className={bad("target.image_vmid")}
                  value={imageVmid}
                  onChange={(e) => set("imageVmid", e.target.value)}
                >
                  {images.length === 0 && <option value="">Geen VM-templates gevonden</option>}
                  {images.map((g) => (
                    <option key={g.vmid} value={g.vmid}>
                      {g.name} (VM {g.vmid} op {g.host})
                    </option>
                  ))}
                </Select>
              </FieldRow>
              <FieldRow error={fieldError} field="target.storage" label="Storage" hint="Gedeelde storage laat ClusterForge de VM's over de hosts spreiden.">
                <Select id={fieldId("target.storage")} className={bad("target.storage")} value={form.storage} onChange={(e) => set("storage", e.target.value)}>
                  <option value="">Zoals de golden image</option>
                  {storages.map(([name, shared]) => (
                    <option key={name} value={name}>
                      {name}
                      {shared ? " (gedeeld)" : ""}
                    </option>
                  ))}
                </Select>
              </FieldRow>
              <div className="grid grid-cols-2 gap-4">
                <FieldRow error={fieldError} field="target.bridge" label="Bridge">
                  <Input id={fieldId("target.bridge")} className={bad("target.bridge")} value={form.bridge} onChange={(e) => set("bridge", e.target.value)} />
                </FieldRow>
                <FieldRow error={fieldError} field="target.vlan" label="VLAN">
                  <Input
                    id={fieldId("target.vlan")}
                    className={bad("target.vlan")}
                    inputMode="numeric"
                    placeholder="geen"
                    value={form.vlan}
                    onChange={(e) => set("vlan", e.target.value)}
                  />
                </FieldRow>
              </div>
            </div>
            {resources.data && images.length === 0 && (
              <div className="space-y-2 rounded-md border border-slate-200 p-4 text-sm dark:border-slate-800">
                <p>
                  Er staat nog geen golden image in deze Proxmox. Maak er een door dit als root op een Proxmox-host te draaien; daarna
                  verschijnt hij hier na de volgende synchronisatie.
                </p>
                <CopyBlock text={`curl -fsSL ${origin}/install/golden-image.sh | bash -s -- --server ${origin} --storage local-lvm`} />
              </div>
            )}
          </div>
        )}
      </Card>

      <Card title="Netwerk en toegang">
        <div className="space-y-4">
          <fieldset>
            <legend className="mb-1 text-sm font-medium text-slate-700 dark:text-slate-300">Adressen van de nodes</legend>
            <div className="flex flex-wrap gap-4 text-sm">
              <label className="inline-flex items-center gap-2">
                <input type="radio" id={fieldId("target.network")} checked={form.network === "static"} onChange={() => set("network", "static")} />
                Vaste adressen
              </label>
              <label className="inline-flex items-center gap-2">
                <input type="radio" checked={form.network === "dhcp"} onChange={() => set("network", "dhcp")} />
                DHCP
              </label>
            </div>
          </fieldset>
          {form.network === "static" ? (
            <div className="grid gap-4 sm:grid-cols-3">
              <FieldRow error={fieldError} field="target.first_ip" label="Eerste adres" hint="Met prefix; de volgende nodes krijgen de adressen erna.">
                <Input
                  id={fieldId("target.first_ip")}
                  className={bad("target.first_ip")}
                  required
                  placeholder="10.0.20.11/24"
                  value={form.firstIp}
                  onChange={(e) => set("firstIp", e.target.value)}
                />
              </FieldRow>
              <FieldRow error={fieldError} field="target.gateway" label="Gateway">
                <Input
                  id={fieldId("target.gateway")}
                  className={bad("target.gateway")}
                  required
                  placeholder="10.0.20.1"
                  value={form.gateway}
                  onChange={(e) => set("gateway", e.target.value)}
                />
              </FieldRow>
              <FieldRow error={fieldError} field="target.dns" label="DNS-servers" hint="Gescheiden door komma's.">
                <Input id={fieldId("target.dns")} className={bad("target.dns")} placeholder="10.0.20.1" value={form.dns} onChange={(e) => set("dns", e.target.value)} />
              </FieldRow>
            </div>
          ) : (
            <p className="text-sm text-slate-600 dark:text-slate-400">
              De nodes krijgen hun adres van de DHCP-server. Zorg dat het VIP buiten de DHCP-pool valt.
            </p>
          )}
          <FieldRow
            error={fieldError}
            field="target.ssh_keys"
            label="Publieke SSH-sleutels"
            hint="Eén per regel, voor de gebruiker debian. Zonder sleutel kom je alleen via de console van Proxmox binnen."
          >
            <Textarea
              id={fieldId("target.ssh_keys")}
              className={cx("font-mono text-xs", bad("target.ssh_keys"))}
              rows={3}
              placeholder="ssh-ed25519 AAAA… jij@laptop"
              value={form.sshKeys}
              onChange={(e) => set("sshKeys", e.target.value)}
            />
          </FieldRow>
          <FieldRow error={fieldError} field="server_url" label="Adres van ClusterForge" hint="Hiermee melden de nieuwe VM's zich aan; ze moeten het kunnen bereiken, en NATS op poort 4222.">
            <Input id={fieldId("server_url")} className={bad("server_url")} value={eff.serverUrl ?? ""} onChange={(e) => set("serverUrl", e.target.value)} />
          </FieldRow>
        </div>
      </Card>

      <Card title="Voorbeeld">
        <Preview ready={ready} loading={plan.isFetching} plan={plan.data} error={plan.error} />
      </Card>

      <div className="flex flex-wrap items-center justify-end gap-2">
        <Button type="button" variant="secondary" onClick={() => router.push("/clusters")}>
          Annuleren
        </Button>
        <Button type="submit" disabled={deploy.isPending || !ready}>
          {deploy.isPending ? "Bezig…" : "Uitrollen"}
        </Button>
      </div>
    </form>
  );
}

function FieldRow({
  field,
  label,
  hint,
  error,
  children,
}: {
  field: string;
  label: string;
  hint?: ReactNode;
  error: ApiError | null;
  children: ReactNode;
}) {
  return (
    <div>
      <Label htmlFor={fieldId(field)}>{label}</Label>
      {children}
      {error?.field === field ? (
        <p className="mt-1 text-xs text-red-700 dark:text-red-300">{error.message}</p>
      ) : (
        hint && <p className="mt-1 text-xs text-slate-500">{hint}</p>
      )}
    </div>
  );
}

function paramHint(p: TemplateParam) {
  const range = p.min && p.max ? `Van ${p.min} tot ${p.max}.` : p.min ? `Minstens ${p.min}.` : p.max ? `Hoogstens ${p.max}.` : "";
  return [p.help, range].filter(Boolean).join(" ") || undefined;
}

function ParamInput({ p, value, onChange, className }: { p: TemplateParam; value: string; onChange: (v: string) => void; className?: string }) {
  const id = fieldId(`params.${p.name}`);
  switch (p.type) {
    case "bool":
      return (
        <label className="inline-flex items-center gap-2 text-sm">
          <input id={id} type="checkbox" checked={value === "true"} onChange={(e) => onChange(e.target.checked ? "true" : "false")} />
          Aan
        </label>
      );
    case "int":
      return (
        <Input
          id={id}
          className={className}
          type="number"
          min={p.min ?? undefined}
          max={p.max ?? undefined}
          required={!p.optional}
          placeholder={p.optional ? "automatisch" : undefined}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    case "secret":
      return (
        <Input
          id={id}
          className={cx("font-mono", className)}
          autoComplete="off"
          placeholder="wordt gegenereerd"
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    default:
      return (
        <Input
          id={id}
          className={className}
          required={!p.optional && p.default === null}
          placeholder={p.type === "ipv4" ? "10.0.20.100" : p.type === "size" ? "2G" : p.type === "cidr" ? "10.0.20.0/24" : undefined}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      );
  }
}

function Preview({
  ready,
  loading,
  plan,
  error,
}: {
  ready: boolean;
  loading: boolean;
  plan: Awaited<ReturnType<typeof planDeployment>> | undefined;
  error: unknown;
}) {
  if (!ready) return <p className="text-sm text-slate-500">Vul een naam in en kies Proxmox en een golden image; dan verschijnt hier wat er gemaakt wordt.</p>;
  if (error) return <Alert>{error instanceof Error ? error.message : "Controleren mislukt"}</Alert>;
  if (!plan) return <p className="text-sm text-slate-500">{loading ? "Controleren…" : ""}</p>;
  return (
    <div className={cx("space-y-3", loading && "opacity-60")}>
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead className="border-b border-slate-200 dark:border-slate-800">
            <tr>
              <th className={thClass}>Node</th>
              <th className={thClass}>Adres</th>
              <th className={thClass}>Proxmox-host</th>
              <th className={`${thClass} text-right`}>vCPU</th>
              <th className={`${thClass} text-right`}>Geheugen</th>
              <th className={`${thClass} text-right`}>Schijf</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {plan.nodes.map((n) => (
              <tr key={n.hostname}>
                <td className={`${tdClass} font-medium`}>{n.hostname}</td>
                <td className={tdClass}>{n.address || <span className="text-slate-500">DHCP</span>}</td>
                <td className={tdClass}>{n.host}</td>
                <td className={`${tdClass} text-right tabular-nums`}>{n.cpu}</td>
                <td className={`${tdClass} text-right tabular-nums`}>{mib(n.memory_mib)}</td>
                <td className={`${tdClass} text-right tabular-nums`}>{n.disk_gib} GB</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {plan.vip && (
        <p className="text-sm text-slate-700 dark:text-slate-300">
          VIP <strong>{plan.vip}</strong>
          {plan.vrid !== null && <> met VRRP-id {plan.vrid}</>}. Na het uitrollen controleert ClusterForge dat een node het VIP heeft.
        </p>
      )}
    </div>
  );
}
