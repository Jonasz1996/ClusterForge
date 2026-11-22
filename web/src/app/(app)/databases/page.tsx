"use client";

import Link from "next/link";
import { HealPanel } from "@/components/databases/Heal";
import { RetireCards } from "@/components/databases/Retire";
import { upgradeCommand } from "@/components/drift/Drift";
import { ago, Empty, EnvBadge, formatBytes, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { CopyBlock } from "@/components/inventory/InstallAgent";
import { Badge, Card, PageHeader } from "@/components/ui";
import {
  engineLabel,
  healthInfo,
  kindLabel,
  useDatabases,
  type DatabaseGroup,
  type DatabaseInfo,
  type DatabaseServer,
  type DatabaseUser,
} from "@/lib/databases";
import { useHeal, type HealReport } from "@/lib/heal";

export default function DatabasesPage() {
  const groups = useDatabases();
  const heal = useHeal();
  const oldAgents = [
    ...new Set((groups.data ?? []).flatMap((g) => g.servers.filter((s) => s.error_kind === "old_agent").map((s) => s.hostname))),
  ];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Databases"
        description="De MariaDB- en PostgreSQL-servers op je nodes, per cluster: een Galera-cluster, een primary met replica's of een losse server."
      />

      {oldAgents.length > 0 && (
        <Card title="Agent bijwerken">
          <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
            Op {oldAgents.join(", ")} draait een agent die de databases nog niet kan lezen. Werk hem bij met deze regel op elke
            node; de databases verschijnen binnen een minuut.
          </p>
          <CopyBlock text={upgradeCommand()} />
        </Card>
      )}

      <RetireCards />

      <QueryState q={groups}>
        {groups.data?.length === 0 ? (
          <Empty>
            Nog geen databaseserver gevonden. ClusterForge ziet MariaDB en PostgreSQL op elke node met cf-agent; de lijst vult
            zich binnen een minuut nadat de agent verbonden is.
          </Empty>
        ) : (
          <div className="space-y-6">
            {(groups.data ?? []).map((g) => (
              <GroupCard key={g.key} g={g} heal={heal.data?.find((r) => r.key === g.key)} />
            ))}
          </div>
        )}
      </QueryState>
    </div>
  );
}

// movable zegt of ClusterForge de databases van g kan verhuizen.
function movable(g: DatabaseGroup) {
  return g.engine === "mariadb" || g.engine === "postgresql";
}

function GroupCard({ g, heal }: { g: DatabaseGroup; heal?: HealReport }) {
  const health = healthInfo[g.health];
  const title = (
    <span className="flex flex-wrap items-center gap-2">
      <span>
        {engineLabel[g.engine]}
        {/* Een losse server in een cluster, zoals een MariaDB op de scheidsrechter, heet naar zijn node. */}
        {(g.node_id || !g.cluster_id) && <> op {g.servers[0]?.hostname}</>}
        {g.cluster_id && (
          <>
            {" "}
            {g.node_id ? "in" : "van"}{" "}
            <Link href={`/clusters/detail?id=${g.cluster_id}`} className="text-brand-700 hover:underline dark:text-sky-300">
              {g.cluster_name}
            </Link>
          </>
        )}
      </span>
      <Badge tone={health.tone}>{health.label}</Badge>
      <Badge>{kindLabel[g.kind]}</Badge>
      {g.cluster_environment && <EnvBadge env={g.cluster_environment} />}
      {g.version && <span className="text-sm font-normal text-slate-500">versie {g.version}</span>}
    </span>
  );
  return (
    <Card title={title}>
      <p className="mb-4 text-sm text-slate-700 dark:text-slate-300">{g.summary}</p>
      <HealPanel r={heal} />
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead className="border-b border-slate-200 dark:border-slate-800">
            <tr>
              <th className={thClass}>Node</th>
              <th className={thClass}>Toestand</th>
              <th className={thClass}>Unit</th>
              <th className={`${thClass} text-right`}>Verbindingen</th>
              <th className={thClass}>Gelezen</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {g.servers.map((s) => (
              <ServerRow key={s.node_id} s={s} />
            ))}
          </tbody>
        </table>
      </div>

      <h3 className="mt-6 mb-2 text-sm font-semibold">
        Databases
        {g.databases_from && g.servers.length > 1 && (
          <span className="ml-1 font-normal text-slate-500">volgens {g.databases_from}</span>
        )}
      </h3>
      {g.databases.length === 0 ? (
        <p className="text-sm text-slate-500">
          {g.databases_from ? "Geen databases buiten die van het systeem." : "Geen server antwoordt, dus geen lijst."}
        </p>
      ) : (
        // relative: anders maakt de onzichtbare kop de pagina op een telefoon breder dan het scherm.
        <div className="relative overflow-x-auto">
          <table className={tableClass}>
            <thead className="border-b border-slate-200 dark:border-slate-800">
              <tr>
                <th className={thClass}>Naam</th>
                <th className={`${thClass} text-right`}>Grootte</th>
                <th className={`${thClass} text-right`}>Tabellen</th>
                <th className={thClass}>Gebruikt door</th>
                {movable(g) && (
                  <th className={thClass}>
                    <span className="sr-only">Verhuizen</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {g.databases.map((d) => (
                <tr key={d.name}>
                  <td className={`${tdClass} font-mono text-xs`}>
                    {d.name}
                    {d.shared && (
                      <div className="mt-1 font-sans">
                        <Badge tone="amber">gedeeld</Badge>
                      </div>
                    )}
                  </td>
                  <td className={`${tdClass} text-right tabular-nums`}>{formatBytes(d.size_bytes)}</td>
                  <td className={`${tdClass} text-right tabular-nums`}>{d.tables < 0 ? "–" : d.tables}</td>
                  <td className={`${tdClass} min-w-64`}>
                    <Users d={d} g={g} />
                  </td>
                  {movable(g) && (
                    <td className={`${tdClass} whitespace-nowrap text-right`}>
                      <Link
                        href={`/databases/verhuizen?source=${encodeURIComponent(g.key)}&db=${encodeURIComponent(d.name)}`}
                        className="text-brand-700 hover:underline dark:text-sky-300"
                        title="Toon wat er mee moet om deze database naar de nodes van een app te verhuizen, en doe een proef."
                      >
                        Verhuizen…
                      </Link>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
          {g.databases_total > g.databases.length && (
            <p className="mt-2 text-xs text-slate-500">
              {g.databases.length} van de {g.databases_total} databases getoond.
            </p>
          )}
        </div>
      )}
    </Card>
  );
}

// Users toont per cluster, losse node of adres wie de database de laatste
// dertig dagen gebruikte.
function Users({ d, g }: { d: DatabaseInfo; g: DatabaseGroup }) {
  if (d.users.length === 0) {
    return <span className="text-slate-500">niemand in de laatste 30 dagen</span>;
  }
  return (
    <ul className="space-y-1.5">
      {d.users.map((u) => (
        <li key={u.key}>
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <UserName u={u} />
            {u.kind === "cluster" && u.nodes.length > 0 && <span className="text-slate-500">{u.nodes.join(", ")}</span>}
            <span className="text-xs text-slate-500">
              als {u.db_users.join(", ")} · {u.connections > 0 ? `${u.connections} verbonden` : `laatst ${ago(u.last_seen)}`}
            </span>
            <Dependency u={u} g={g} />
          </span>
        </li>
      ))}
    </ul>
  );
}

function UserName({ u }: { u: DatabaseUser }) {
  const link = "font-medium text-brand-700 hover:underline dark:text-sky-300";
  switch (u.kind) {
    case "cluster":
      return (
        <Link href={`/clusters/detail?id=${u.cluster_id}`} className={link}>
          {u.name}
        </Link>
      );
    case "node":
      return (
        <Link href={`/nodes/detail?id=${u.node_id}`} className={link}>
          {u.name}
        </Link>
      );
    case "local":
      return <span title="via de socket of 127.0.0.1">op de databasenode zelf ({u.nodes.join(", ")})</span>;
  }
  return (
    <span title="Dit adres hoort bij geen enkele node in ClusterForge.">
      onbekend adres <span className="font-mono text-xs">{u.name}</span>
    </span>
  );
}

// Dependency zegt of de afhankelijkheid van dat cluster naar de database
// in de graaf staat.
function Dependency({ u, g }: { u: DatabaseUser; g: DatabaseGroup }) {
  if (u.kind !== "cluster" || !g.cluster_id || u.cluster_id === g.cluster_id) return null;
  const href = "/afhankelijkheden";
  switch (u.dependency) {
    case "confirmed":
      return <Badge tone="green">afhankelijkheid bevestigd</Badge>;
    case "suggested":
      return (
        <Link href={href} title="Bevestig of negeer het voorstel bij Afhankelijkheden.">
          <Badge tone="blue">afhankelijkheid voorgesteld</Badge>
        </Link>
      );
    case "ignored":
      return <Badge>afhankelijkheid genegeerd</Badge>;
  }
  return (
    <Link href={href} title="Voeg de afhankelijkheid toe bij Afhankelijkheden.">
      <Badge tone="amber">niet in de afhankelijkheden</Badge>
    </Link>
  );
}

function ServerRow({ s }: { s: DatabaseServer }) {
  return (
    <tr>
      <td className={tdClass}>
        <Link href={`/nodes/detail?id=${s.node_id}`} className="font-medium text-brand-700 hover:underline dark:text-sky-300">
          {s.hostname}
        </Link>
      </td>
      <td className={tdClass}>
        <span className="flex flex-wrap items-center gap-1" title={s.error || undefined}>
          <Badge tone={s.ok ? "green" : s.error_kind === "old_agent" ? "slate" : "red"}>{s.state}</Badge>
          {s.read_only && !s.error_kind && <Badge>alleen lezen</Badge>}
        </span>
        {s.error && !s.error_kind && <div className="mt-1 text-xs text-red-700 dark:text-red-300">{s.error}</div>}
      </td>
      <td className={`${tdClass} font-mono text-xs`}>{s.unit || "–"}</td>
      <td className={`${tdClass} text-right tabular-nums`}>
        {s.running ? (
          <span title="verbindingen nu / maximum">
            {s.connections} / {s.max_connections}
          </span>
        ) : (
          "–"
        )}
      </td>
      <td className={`${tdClass} whitespace-nowrap text-slate-500`}>
        {!s.error_kind ? ago(s.synced_at) : s.ok_at ? `laatst gelukt ${ago(s.ok_at)}` : "nog nooit"}
      </td>
    </tr>
  );
}
