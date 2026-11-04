"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { Alert, Badge, Button, Card, PageHeader, Select, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useIsAdmin } from "@/lib/inventory";
import {
  healthLabel,
  needLabel,
  stepKinds,
  useDeleteRecipeVersion,
  useRecipeCheck,
  useRecipeExamples,
  useRecipeVersion,
  useRecipes,
  useSaveRecipe,
  type RecipeCheck,
  type RecipeParsed,
} from "@/lib/recipes";

export default function RecipesPage() {
  return (
    <Suspense>
      <Recipes />
    </Suspense>
  );
}

function Recipes() {
  const search = useSearchParams();
  const router = useRouter();
  const name = search.get("naam");
  const version = search.get("versie");
  const recipes = useRecipes();
  const examples = useRecipeExamples();
  const current = useRecipeVersion(name, version);
  const isAdmin = useIsAdmin();
  const save = useSaveRecipe();
  const remove = useDeleteRecipeVersion();
  const [source, setSource] = useState("");
  const [loadedFor, setLoadedFor] = useState<string | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const check = useRecipeCheck(source);

  // Een gekozen versie in de editor zetten, één keer per keuze.
  const key = name && version ? `${name}@${version}` : null;
  // (state bijwerken tijdens het renderen, zoals React aanraadt voor een
  // waarde die van een andere afhangt)
  if (key && current.data && current.data.name === name && current.data.version === version && loadedFor !== key) {
    setSource(current.data.source);
    setLoadedFor(key);
  }

  const open = (n: string, v: string) => {
    setSaved(null);
    save.reset();
    router.push(`/recepten?naam=${encodeURIComponent(n)}&versie=${encodeURIComponent(v)}`);
  };
  const startNew = (text: string) => {
    setSaved(null);
    save.reset();
    setLoadedFor(null);
    router.push("/recepten");
    setSource(text);
  };
  const onSave = () => {
    setSaved(null);
    save.mutate(source, {
      onSuccess: (v) => {
        setSaved(`Bewaard als ${v.name} ${v.version}.`);
        setLoadedFor(`${v.name}@${v.version}`);
        router.push(`/recepten?naam=${encodeURIComponent(v.name)}&versie=${encodeURIComponent(v.version)}`);
      },
    });
  };
  const versions = recipes.data?.find((r) => r.name === name)?.versions ?? [];
  const dirty = !!current.data && key === loadedFor && source !== current.data.source;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Recepten"
        description="Hoe software op de nodes van een cluster komt, in een tekst die op een Dockerfile lijkt. ClusterForge controleert het recept terwijl je typt en toont wat de agent per node zal doen. Geheimen staan er nooit in, alleen {{ secret.naam }}."
      />
      <div className="grid gap-6 lg:grid-cols-[16rem_1fr]">
        <div className="space-y-4">
          <Card title="Jouw recepten">
            {recipes.data?.length === 0 && <p className="text-sm text-slate-500">Nog geen recepten bewaard.</p>}
            <ul className="space-y-1 text-sm">
              {recipes.data?.map((r) => (
                <li key={r.name}>
                  <button
                    type="button"
                    onClick={() => open(r.name, r.versions[0])}
                    className={cx(
                      "w-full rounded px-2 py-1 text-left hover:bg-slate-100 dark:hover:bg-slate-800",
                      r.name === name && "bg-slate-100 font-medium dark:bg-slate-800",
                    )}
                  >
                    {r.name} <span className="text-xs text-slate-500">{r.versions[0]}</span>
                  </button>
                </li>
              ))}
            </ul>
          </Card>
          <Card title="Beginnen">
            <div className="flex flex-col gap-2">
              <Button variant="secondary" onClick={() => startNew("RECIPE mijn-app 1.0\nFROM debian:13\n\nAPT nginx\nSERVICE nginx ENABLE START\n\nHEALTH http http://localhost/ 200\n")}>
                Leeg recept
              </Button>
              {examples.data?.map((e) => (
                <Button key={e.name} variant="secondary" onClick={() => startNew(e.source)}>
                  Voorbeeld: {e.title}
                </Button>
              ))}
            </div>
          </Card>
        </div>

        <div className="space-y-4">
          <Card
            title={
              <span className="flex flex-wrap items-center justify-between gap-2">
                <span className="flex items-center gap-2">
                  {name ?? "Nieuw recept"}
                  {versions.length > 0 && (
                    <Select aria-label="Versie" value={version ?? ""} onChange={(e) => open(name!, e.target.value)} className="w-auto py-1">
                      {versions.map((v) => (
                        <option key={v} value={v}>
                          {v}
                        </option>
                      ))}
                    </Select>
                  )}
                  {dirty && <Badge tone="amber">gewijzigd</Badge>}
                </span>
                {isAdmin && (
                  <span className="flex gap-2">
                    {name && version && !dirty && (
                      <Button
                        variant="secondary-danger"
                        disabled={remove.isPending}
                        onClick={() => {
                          if (confirm(`Versie ${version} van ${name} verwijderen?`)) {
                            remove.mutate({ name, version }, { onSuccess: () => startNew("") });
                          }
                        }}
                      >
                        Versie verwijderen
                      </Button>
                    )}
                    <Button onClick={onSave} disabled={save.isPending || !check.data?.ok || source.trim() === ""}>
                      Bewaren
                    </Button>
                  </span>
                )}
              </span>
            }
          >
            {dirty && (
              <p className="mb-2 text-xs text-slate-500">
                Een bewaarde versie verandert niet meer: verhoog de versie op de RECIPE-regel voor je bewaart.
              </p>
            )}
            <textarea
              aria-label="Recept"
              value={source}
              onChange={(e) => setSource(e.target.value)}
              spellCheck={false}
              readOnly={!isAdmin}
              rows={Math.max(16, Math.min(40, source.split("\n").length + 2))}
              className="w-full rounded-md border border-slate-300 bg-white p-3 font-mono text-xs leading-5 dark:border-slate-700 dark:bg-slate-950"
              placeholder="RECIPE mijn-app 1.0"
            />
            {save.error && (
              <div className="mt-2">
                <Alert>{save.error instanceof ApiError ? save.error.message : "Bewaren mislukte"}</Alert>
              </div>
            )}
            {saved && (
              <div className="mt-2">
                <Alert kind="success">{saved}</Alert>
              </div>
            )}
          </Card>
          {source.trim() !== "" && check.data && <CheckView check={check.data} />}
        </div>
      </div>
    </div>
  );
}

function CheckView({ check }: { check: RecipeCheck }) {
  return (
    <>
      {check.diagnostics.length > 0 && (
        <Card title={check.ok ? "Waarschuwingen" : "Fouten"}>
          <ul className="space-y-1 text-sm">
            {check.diagnostics.map((d, i) => (
              <li key={i} className="flex gap-2">
                <Badge tone={d.warning ? "amber" : "red"}>regel {d.line}</Badge>
                <span>{d.message}</span>
              </li>
            ))}
          </ul>
        </Card>
      )}
      {check.recipe && <Plan recipe={check.recipe} />}
    </>
  );
}

function Plan({ recipe }: { recipe: RecipeParsed }) {
  return (
    <Card title="Wat de agent doet">
      <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-[10rem_1fr]">
        <dt className="text-slate-500">Recept</dt>
        <dd>
          {recipe.name} {recipe.version}, op {recipe.from}
        </dd>
        <dt className="text-slate-500">Clusterservices</dt>
        <dd>{recipe.needs.length ? recipe.needs.map(needLabel).join(", ") : "geen"}</dd>
        {recipe.db_config.length > 0 && (
          <>
            <dt className="text-slate-500">Database-instellingen</dt>
            <dd>
              {recipe.db_config.map((d) => (
                <span key={d.key} className="mr-3">
                  <code>
                    {d.key} = {d.value}
                  </code>
                  {d.min && <span className="text-slate-500"> (minstens)</span>}
                </span>
              ))}
            </dd>
          </>
        )}
        {recipe.params.length > 0 && (
          <>
            <dt className="text-slate-500">Vraagt</dt>
            <dd>
              {recipe.params.map((p) => (
                <span key={p.name} className="mr-2">
                  <code>{p.name}</code> <span className="text-slate-500">({p.type === "secret" ? "geheim, ClusterForge maakt het" : p.type})</span>
                </span>
              ))}
            </dd>
          </>
        )}
      </dl>
      {recipe.roles.map((role) => (
        <div key={role.name} className="mt-4">
          {recipe.roles.length > 1 || role.name ? (
            <h3 className="mb-2 text-sm font-medium">Rol {role.name || "(waar je het recept aan koppelt)"}</h3>
          ) : null}
          <ol className="space-y-1 text-sm">
            {role.steps.map((s, i) => (
              <li key={i} className="flex flex-wrap items-baseline gap-2">
                <span className="w-6 text-right text-xs text-slate-500">{i + 1}.</span>
                <Badge>{stepKinds[s.kind] ?? s.kind}</Badge>
                <span>{s.title}</span>
                {s.once ? <Badge tone="blue">één node</Badge> : <Badge tone="slate">elke node</Badge>}
                {s.secret && <Badge tone="amber">geheim</Badge>}
                {notifies(s.spec).length > 0 && (
                  <span className="text-xs text-slate-500">herstart bij wijziging: {notifies(s.spec).join(", ")}</span>
                )}
                <span className="text-xs text-slate-400">regel {s.line}</span>
              </li>
            ))}
          </ol>
          {role.health.length > 0 && (
            <p className="mt-2 text-sm text-slate-600 dark:text-slate-400">
              Gezond als: {role.health.map(healthLabel).join("; ")}
            </p>
          )}
        </div>
      ))}
    </Card>
  );
}

// notifies geeft de diensten die herstarten als de stap iets veranderde.
function notifies(spec: Record<string, unknown>): string[] {
  for (const v of Object.values(spec)) {
    const n = (v as { notify?: string[] } | null)?.notify;
    if (Array.isArray(n)) return n.map((x) => x.split(":")[1]);
  }
  return [];
}
