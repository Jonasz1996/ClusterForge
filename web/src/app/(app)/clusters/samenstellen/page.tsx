"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { Alert, Button, Card, Input, PageHeader, Select, Textarea, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useIsAdmin } from "@/lib/inventory";
import {
  needLabel,
  useBlueprints,
  useComposeBlueprint,
  useRecipeVersion,
  useRecipes,
  type Composition,
  type RecipeSummary,
} from "@/lib/recipes";

type RoleForm = { name: string; recipe: string; recipe_version: string; recipe_role: string; count: string };

type Form = {
  name: string;
  title: string;
  roles: RoleForm[];
  vip: boolean;
  vipRole: string;
  db: boolean;
  dbRole: string;
  bufferPool: string;
  maxConnections: string;
  extra: string;
};

const emptyForm: Form = {
  name: "",
  title: "",
  roles: [{ name: "server", recipe: "", recipe_version: "", recipe_role: "", count: "3" }],
  vip: true,
  vipRole: "server",
  db: false,
  dbRole: "server",
  bufferPool: "",
  maxConnections: "",
  extra: "",
};

function fromComposition(c: Composition): Form {
  return {
    name: c.name,
    title: c.title,
    roles: c.roles.map((r) => ({
      name: r.name,
      recipe: r.recipe,
      recipe_version: r.recipe_version,
      recipe_role: r.recipe_role ?? "",
      count: String(r.count),
    })),
    vip: !!c.vip,
    vipRole: c.vip?.role ?? c.roles[0]?.name ?? "",
    db: !!c.database,
    dbRole: c.database?.role ?? c.roles[0]?.name ?? "",
    bufferPool: c.database?.buffer_pool ?? "",
    maxConnections: c.database?.max_connections ? String(c.database.max_connections) : "",
    extra: c.database?.extra ?? "",
  };
}

function toComposition(f: Form): Composition {
  return {
    name: f.name.trim(),
    title: f.title.trim(),
    roles: f.roles.map((r) => ({
      name: r.name.trim(),
      recipe: r.recipe,
      recipe_version: r.recipe_version,
      recipe_role: r.recipe_role || undefined,
      count: Number(r.count) || 0,
    })),
    vip: f.vip ? { role: f.vipRole } : undefined,
    database: f.db
      ? {
          role: f.dbRole,
          buffer_pool: f.bufferPool.trim() || undefined,
          max_connections: Number(f.maxConnections) || undefined,
          extra: f.extra.trim() || undefined,
        }
      : undefined,
  };
}

export default function ComposePage() {
  const router = useRouter();
  const isAdmin = useIsAdmin();
  const recipes = useRecipes();
  const blueprints = useBlueprints();
  const compose = useComposeBlueprint();
  const [form, setForm] = useState<Form>(emptyForm);
  const [editing, setEditing] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const set = <K extends keyof Form>(k: K, v: Form[K]) => setForm((f) => ({ ...f, [k]: v }));
  const setRole = (i: number, r: Partial<RoleForm>) =>
    setForm((f) => ({ ...f, roles: f.roles.map((o, j) => (j === i ? { ...o, ...r } : o)) }));
  const roleNames = form.roles.map((r) => r.name).filter((n) => n !== "");
  // Een rol die weg is of hernoemd, is geen keuze meer voor VIP en database.
  const vipRole = roleNames.includes(form.vipRole) ? form.vipRole : (roleNames[0] ?? "");
  const dbRole = roleNames.includes(form.dbRole) ? form.dbRole : (roleNames[0] ?? "");
  const err = (field: string) => (error?.field === field || error?.field?.startsWith(field + ".") ? error.message : undefined);

  const onCompose = () => {
    setError(null);
    compose.mutate(toComposition({ ...form, vipRole, dbRole }), {
      onSuccess: (res) => router.push(`/clusters/uitrollen?template=${encodeURIComponent(res.template)}`),
      onError: (e) => setError(e instanceof ApiError ? e : new ApiError(0, "unknown", "Samenstellen mislukt")),
    });
  };

  return (
    <div className="space-y-6">
      <div className="text-sm">
        <Link href="/clusters/uitrollen" className="text-slate-500 hover:underline">
          ← Cluster uitrollen
        </Link>
      </div>
      <PageHeader
        title="Zelf samenstellen"
        description="Kies per rol een recept en welke clusterservices erbij komen. ClusterForge maakt er een template van; daarna rol je uit zoals altijd, ook op je bestaande CT's."
      />
      {error && <Alert>{error.message}</Alert>}

      {(blueprints.data?.length ?? 0) > 0 && (
        <Card title="Eerder samengesteld">
          <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
            {blueprints.data!.map((b) => (
              <li key={b.template} className="flex flex-wrap items-center justify-between gap-2 py-2">
                <span>
                  <span className="font-medium">{b.title}</span>{" "}
                  <span className="text-slate-500">
                    {b.template} {b.version}
                  </span>
                </span>
                <span className="flex gap-2">
                  <Button
                    variant="secondary"
                    onClick={() => {
                      setForm(fromComposition(b.composition));
                      setEditing(true);
                      setError(null);
                    }}
                  >
                    Aanpassen
                  </Button>
                  <Link
                    href={`/clusters/uitrollen?template=${encodeURIComponent(b.template)}`}
                    className="rounded-md border border-slate-300 px-3 py-1.5 text-sm hover:bg-slate-50 dark:border-slate-700 dark:hover:bg-slate-800"
                  >
                    Uitrollen
                  </Link>
                </span>
              </li>
            ))}
          </ul>
        </Card>
      )}

      <Card title="Naam">
        <div className="grid gap-4 sm:grid-cols-2">
          <label className="block text-sm">
            <span className="mb-1 block font-medium">Titel</span>
            <Input value={form.title} onChange={(e) => set("title", e.target.value)} placeholder="Zabbix met HA" />
            {err("title") && <span className="text-xs text-red-600">{err("title")}</span>}
          </label>
          <label className="block text-sm">
            <span className="mb-1 block font-medium">Korte naam</span>
            <Input
              value={form.name}
              disabled={editing}
              onChange={(e) => set("name", e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-"))}
              placeholder="zabbix"
            />
            <span className="text-xs text-slate-500">
              De template heet eigen-{form.name || "…"}. Opnieuw samenstellen met dezelfde naam maakt een nieuwe versie.
            </span>
            {err("name") && <span className="block text-xs text-red-600">{err("name")}</span>}
          </label>
        </div>
      </Card>

      <Card title="Rollen">
        {recipes.data?.length === 0 && (
          <Alert kind="info">
            Je hebt nog geen recepten. Maak er eerst een onder <Link href="/recepten">Recepten</Link>, bijvoorbeeld uit het
            Zabbix-voorbeeld.
          </Alert>
        )}
        <div className="space-y-4">
          {form.roles.map((r, i) => (
            <RoleRow
              key={i}
              role={r}
              recipes={recipes.data ?? []}
              db={form.db}
              vip={form.vip}
              error={err(`roles.${i}`)}
              onChange={(v) => setRole(i, v)}
              onRemove={form.roles.length > 1 ? () => setForm((f) => ({ ...f, roles: f.roles.filter((_, j) => j !== i) })) : undefined}
            />
          ))}
          {err("roles") && <p className="text-sm text-red-600">{err("roles")}</p>}
          <Button
            variant="secondary"
            onClick={() =>
              setForm((f) => ({
                ...f,
                roles: [...f.roles, { name: `rol${f.roles.length + 1}`, recipe: "", recipe_version: "", recipe_role: "", count: "1" }],
              }))
            }
          >
            Rol toevoegen
          </Button>
        </div>
      </Card>

      <Card title="Clusterservices">
        <div className="space-y-6">
          <div>
            <label className="flex items-center gap-2 text-sm font-medium">
              <input type="checkbox" checked={form.vip} onChange={(e) => set("vip", e.target.checked)} />
              VIP met keepalived
            </label>
            <p className="mt-1 text-xs text-slate-500">
              Het VIP staat op een node waar de HEALTH-regels van het recept slagen; met de database erbij alleen op een node waarvan
              MariaDB mag schrijven. Het adres kies je bij het uitrollen.
            </p>
            {form.vip && (
              <label className="mt-2 block max-w-xs text-sm">
                <span className="mb-1 block">Op de rol</span>
                <Select value={vipRole} onChange={(e) => set("vipRole", e.target.value)}>
                  {roleNames.map((n) => (
                    <option key={n}>{n}</option>
                  ))}
                </Select>
              </label>
            )}
            {err("vip") && <p className="text-xs text-red-600">{err("vip")}</p>}
          </div>

          <div>
            <label className="flex items-center gap-2 text-sm font-medium">
              <input type="checkbox" checked={form.db} onChange={(e) => set("db", e.target.checked)} />
              Database: MariaDB Galera
            </label>
            <p className="mt-1 text-xs text-slate-500">
              Komt na de uitrol op de nodes van één rol; daarna maakt ClusterForge de databases en gebruikers uit de recepten en laadt het
              schema. Met twee nodes komt er een scheidsrechter bij.
            </p>
            {form.db && (
              <div className="mt-2 grid gap-4 sm:grid-cols-3">
                <label className="block text-sm">
                  <span className="mb-1 block">Op de rol</span>
                  <Select value={dbRole} onChange={(e) => set("dbRole", e.target.value)}>
                    {roleNames.map((n) => (
                      <option key={n}>{n}</option>
                    ))}
                  </Select>
                </label>
                <label className="block text-sm">
                  <span className="mb-1 block">Buffer pool per node</span>
                  <Input value={form.bufferPool} onChange={(e) => set("bufferPool", e.target.value)} placeholder="256M (standaard)" />
                  {err("database.buffer_pool") && <span className="text-xs text-red-600">{err("database.buffer_pool")}</span>}
                </label>
                <label className="block text-sm">
                  <span className="mb-1 block">Max. verbindingen</span>
                  <Input
                    inputMode="numeric"
                    value={form.maxConnections}
                    onChange={(e) => set("maxConnections", e.target.value)}
                    placeholder="200 (standaard)"
                  />
                  {err("database.max_connections") && <span className="text-xs text-red-600">{err("database.max_connections")}</span>}
                </label>
                <label className="block text-sm sm:col-span-3">
                  <span className="mb-1 block">Extra instellingen voor [mysqld]</span>
                  <Textarea
                    rows={3}
                    className="font-mono text-xs"
                    value={form.extra}
                    onChange={(e) => set("extra", e.target.value)}
                    placeholder={"max_allowed_packet = 64M\ninnodb_log_file_size = 512M"}
                  />
                  <span className="text-xs text-slate-500">
                    Eén per regel. Ze gaan voor de DBCONFIG van de recepten; Galera, poort en adres beheert ClusterForge zelf.
                  </span>
                  {err("database.extra") && <span className="block text-xs text-red-600">{err("database.extra")}</span>}
                </label>
              </div>
            )}
            {err("database") && <p className="text-xs text-red-600">{err("database")}</p>}
          </div>

          <p className="text-xs text-slate-500">
            Bestanden synchroniseren en cronjobs zet je na de uitrol aan op de pagina van het cluster.
          </p>
        </div>
      </Card>

      <div className="flex justify-end">
        <Button
          onClick={onCompose}
          disabled={!isAdmin || compose.isPending || form.title.trim() === "" || form.name === "" || form.roles.some((r) => !r.recipe)}
        >
          {compose.isPending ? "Bezig…" : "Template maken en uitrollen"}
        </Button>
      </div>
      {!isAdmin && <p className="text-right text-xs text-slate-500">Alleen een beheerder kan samenstellen.</p>}
    </div>
  );
}

function RoleRow({
  role,
  recipes,
  db,
  vip,
  error,
  onChange,
  onRemove,
}: {
  role: RoleForm;
  recipes: RecipeSummary[];
  db: boolean;
  vip: boolean;
  error?: string;
  onChange: (r: Partial<RoleForm>) => void;
  onRemove?: () => void;
}) {
  const summary = recipes.find((r) => r.name === role.recipe);
  const detail = useRecipeVersion(role.recipe || null, role.recipe_version || null);
  const parsed = detail.data?.check.recipe ?? undefined;
  const recipeRoles = parsed?.roles.map((r) => r.name) ?? [];
  const needs = parsed?.needs ?? [];
  const missing = needs.filter((n) => (n.service === "database" && !db) || (n.service === "vip" && !vip));

  return (
    <div className={cx("rounded-md border p-3", error ? "border-red-400" : "border-slate-200 dark:border-slate-700")}>
      <div className="grid gap-3 sm:grid-cols-5">
        <label className="block text-sm">
          <span className="mb-1 block">Rol</span>
          <Input value={role.name} onChange={(e) => onChange({ name: e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-") })} />
        </label>
        <label className="block text-sm sm:col-span-2">
          <span className="mb-1 block">Recept</span>
          <Select
            value={role.recipe}
            onChange={(e) => {
              const s = recipes.find((r) => r.name === e.target.value);
              onChange({ recipe: e.target.value, recipe_version: s?.versions[0] ?? "", recipe_role: "" });
            }}
          >
            <option value="">Kies een recept</option>
            {recipes.map((r) => (
              <option key={r.name}>{r.name}</option>
            ))}
          </Select>
        </label>
        <label className="block text-sm">
          <span className="mb-1 block">Versie</span>
          <Select value={role.recipe_version} onChange={(e) => onChange({ recipe_version: e.target.value })} disabled={!summary}>
            {summary?.versions.map((v) => (
              <option key={v}>{v}</option>
            ))}
          </Select>
        </label>
        <label className="block text-sm">
          <span className="mb-1 block">Nodes</span>
          <Input inputMode="numeric" value={role.count} onChange={(e) => onChange({ count: e.target.value })} />
        </label>
      </div>
      {recipeRoles.length > 1 || (recipeRoles.length === 1 && recipeRoles[0] !== "") ? (
        <label className="mt-3 block max-w-xs text-sm">
          <span className="mb-1 block">ROLE uit het recept</span>
          <Select value={role.recipe_role} onChange={(e) => onChange({ recipe_role: e.target.value })}>
            <option value="">Kies</option>
            {recipeRoles.map((n) => (
              <option key={n}>{n}</option>
            ))}
          </Select>
        </label>
      ) : null}
      <div className="mt-2 flex flex-wrap items-center gap-3 text-xs text-slate-500">
        {parsed && parsed.params.length > 0 && (
          <span>Vraagt bij het uitrollen: {parsed.params.map((p) => p.name).join(", ")}</span>
        )}
        {needs.length > 0 && <span>Nodig: {needs.map((n) => needLabel(n)).join(", ")}</span>}
        {onRemove && (
          <button type="button" className="ml-auto text-red-600 hover:underline" onClick={onRemove}>
            Rol weghalen
          </button>
        )}
      </div>
      {missing.length > 0 && (
        <p className="mt-1 text-xs text-amber-600">
          {role.recipe} vraagt {missing.map((n) => needLabel(n)).join(" en ")}: zet het aan onder Clusterservices.
        </p>
      )}
      {error && <p className="mt-1 text-xs text-red-600">{error}</p>}
    </div>
  );
}
