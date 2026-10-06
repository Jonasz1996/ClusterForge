"use client";

import Link from "next/link";
import { QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Badge, Card, PageHeader } from "@/components/ui";
import { paramTypes, useTemplates, type Template } from "@/lib/deploy";
import { kindLabel } from "@/lib/deps";
import { typeLabel, useIsAdmin } from "@/lib/inventory";

export default function TemplatesPage() {
  const templates = useTemplates();
  const isAdmin = useIsAdmin();

  return (
    <div className="space-y-6">
      <PageHeader
        title="Templates"
        description="Recepten voor een volledig cluster: de VM's, de software erop en de controles achteraf. Ze zitten in de server ingebouwd."
      />
      <QueryState q={templates}>
        {templates.data?.map((t) => (
          <TemplateCard key={t.name} t={t} isAdmin={isAdmin} />
        ))}
      </QueryState>
    </div>
  );
}

function TemplateCard({ t, isAdmin }: { t: Template; isAdmin: boolean }) {
  const roles = t.roles
    .map((r) => {
      const count = r.count ?? (r.count_param ? t.params.find((p) => p.name === r.count_param)?.default : null);
      return `${r.name}${count ? ` (standaard ${count} nodes)` : ""}`;
    })
    .join(", ");
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span className="flex flex-wrap items-center gap-2">
            {t.title}
            <Badge>{t.version}</Badge>
            <code className="text-xs font-normal text-slate-500">{t.name}</code>
          </span>
          {isAdmin && (
            <Link
              href={`/clusters/uitrollen?template=${encodeURIComponent(t.name)}`}
              className="rounded-md bg-brand-600 px-3.5 py-2 text-sm font-medium text-white hover:bg-brand-700"
            >
              Uitrollen
            </Link>
          )}
        </span>
      }
    >
      <p className="text-sm text-slate-700 dark:text-slate-300">{t.description}</p>
      <dl className="mt-4 grid gap-x-6 gap-y-2 text-sm sm:grid-cols-[10rem_1fr]">
        <dt className="text-slate-500">Clustertype</dt>
        <dd>{typeLabel(t.cluster_type)}</dd>
        <dt className="text-slate-500">Rollen</dt>
        <dd>{roles}</dd>
        {t.services.length > 0 && (
          <>
            <dt className="text-slate-500">Diensten</dt>
            <dd>
              <Services t={t} />
            </dd>
          </>
        )}
      </dl>
      <div className="mt-4 overflow-x-auto">
        <table className={tableClass}>
          <thead className="border-b border-slate-200 dark:border-slate-800">
            <tr>
              <th className={thClass}>Parameter</th>
              <th className={thClass}>Soort</th>
              <th className={thClass}>Standaard</th>
              <th className={thClass}>Uitleg</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {t.params.map((p) => (
              <tr key={p.name}>
                <td className={tdClass}>
                  {p.label}
                  <div className="text-xs text-slate-500">
                    <code>{p.name}</code>
                  </div>
                </td>
                <td className={tdClass}>
                  {paramTypes[p.type]}
                  {(p.min || p.max) && (
                    <div className="text-xs text-slate-500">
                      {p.min && `min ${p.min}`}
                      {p.min && p.max && ", "}
                      {p.max && `max ${p.max}`}
                    </div>
                  )}
                </td>
                <td className={tdClass}>
                  {p.default ?? <span className="text-slate-400">{p.type === "secret" ? "gegenereerd" : p.optional ? "leeg" : "verplicht"}</span>}
                </td>
                <td className={`${tdClass} text-slate-600 dark:text-slate-400`}>{p.help}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

// Services toont de diensten die een uitrol in de afhankelijkheidsgraaf zet,
// met unit, poort en waarvan ze afhangen.
function Services({ t }: { t: Template }) {
  return (
    <ul className="space-y-1">
      {t.services.map((s) => {
        const param = s.port_param ? t.params.find((p) => p.name === s.port_param) : undefined;
        const port = s.port ? `poort ${s.port}` : param ? `instelbare poort${param.default ? `, standaard ${param.default}` : ""}` : null;
        return (
          <li key={s.name}>
            <span className="font-medium">{s.name}</span>
            <span className="text-slate-500">
              {" · "}
              {kindLabel(s.kind)}
              {s.unit && (
                <>
                  {" · "}
                  <code className="text-xs">{s.unit}</code>
                </>
              )}
              {port && ` · ${port}`}
              {s.depends_on.length > 0 && ` · hangt af van ${s.depends_on.join(" en ")}`}
            </span>
          </li>
        );
      })}
    </ul>
  );
}
