"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useMemo, useState } from "react";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Modal } from "@/components/Modal";
import { toast } from "@/components/Toast";
import { Alert, Button, Card, Input, Select, cx } from "@/components/ui";
import { AvailDot, HostLabel, SeverityBadge, ZabbixPage } from "@/components/zabbix/Zabbix";
import { ApiError } from "@/lib/api/client";
import { useIsAdmin, useNodes } from "@/lib/inventory";
import { availLabel, sourceLabel, useZabbixHostLink, zabbixLinks, type ZabbixHost, type ZabbixOverview } from "@/lib/zabbixMonitoring";

export default function ZabbixHostsPage() {
  return (
    <Suspense>
      <ZabbixPage
        title="Zabbix-hosts"
        description="Elke host in Zabbix met zijn bereikbaarheid, open problemen en de ClusterForge-node waar hij bij hoort. ClusterForge koppelt vanzelf (via Zabbix-agents, de naam of het IP-adres); je kunt het ook met de hand doen."
      >
        {(o) => <Hosts o={o} />}
      </ZabbixPage>
    </Suspense>
  );
}

const filters = [
  { key: "", label: "Alle" },
  { key: "down", label: "Onbereikbaar" },
  { key: "problems", label: "Met problemen" },
  { key: "nodes", label: "ClusterForge-nodes" },
  { key: "other", label: "Andere hosts" },
  { key: "maintenance", label: "In onderhoud" },
  { key: "disabled", label: "Uitgeschakeld" },
] as const;

function keep(h: ZabbixHost, f: string) {
  switch (f) {
    case "down":
      return h.enabled && h.available === "down";
    case "problems":
      return h.problems > 0;
    case "nodes":
      return !!h.node;
    case "other":
      return h.enabled && !h.node;
    case "maintenance":
      return h.maintenance;
    case "disabled":
      return !h.enabled;
  }
  return true;
}

function Hosts({ o }: { o: ZabbixOverview }) {
  const params = useSearchParams();
  const router = useRouter();
  const isAdmin = useIsAdmin();
  const f = params.get("filter") ?? "";
  const [search, setSearch] = useState("");
  const [edit, setEdit] = useState<ZabbixHost | null>(null);
  const links = zabbixLinks(o.frontend_url);
  const q = search.trim().toLowerCase();
  const list = o.hosts.filter(
    (h) =>
      keep(h, f) &&
      (!q ||
        [h.host, h.name, h.node?.hostname ?? "", h.node?.cluster_name ?? "", h.proxy, ...h.groups, ...h.interfaces.map((i) => i.address)]
          .join(" ")
          .toLowerCase()
          .includes(q)),
  );
  const setFilter = (k: string) => router.replace(k ? `/zabbix/hosts?filter=${k}` : "/zabbix/hosts", { scroll: false });

  return (
    <Card>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <div className="flex flex-wrap gap-1" role="group" aria-label="Filter">
          {filters.map((x) => (
            <button
              key={x.key}
              onClick={() => setFilter(x.key)}
              aria-pressed={f === x.key}
              className={cx(
                "rounded-md px-2.5 py-1 text-sm",
                f === x.key ? "bg-brand-600 text-white" : "text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800",
              )}
            >
              {x.label}
              <span className="ml-1 tabular-nums opacity-70">{o.hosts.filter((h) => keep(h, x.key)).length}</span>
            </button>
          ))}
        </div>
        <Input type="search" placeholder="Zoeken…" value={search} onChange={(e) => setSearch(e.target.value)} aria-label="Zoeken" className="sm:ml-auto sm:w-64" />
      </div>
      {list.length === 0 ? (
        <p className="text-sm text-slate-500">Geen hosts met dit filter.</p>
      ) : (
        <div className="-mx-5 overflow-x-auto">
          <table className={tableClass}>
            <thead>
              <tr className="border-b border-slate-200 dark:border-slate-800">
                <th className={cx(thClass, "pl-5")}>Host</th>
                <th className={thClass}>Bereikbaar</th>
                <th className={thClass}>Problemen</th>
                <th className={thClass}>Via</th>
                <th className={cx(thClass, "pr-5")}>Koppeling</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {list.map((h) => (
                <tr key={h.hostid} className={cx(!h.enabled && "opacity-60")}>
                  <td className={cx(tdClass, "pl-5")}>
                    <div className="text-sm">
                      <HostLabel name={h.name} node={h.node} />
                    </div>
                    <div className="text-xs text-slate-500">
                      {[
                        h.host !== h.name && h.host.toLowerCase() !== h.node?.hostname.toLowerCase() && (
                          <span key="h" className="font-mono">
                            {h.host}
                          </span>
                        ),
                        h.groups.length > 0 && <span key="g">{h.groups.join(", ")}</span>,
                        h.maintenance && (
                          <span key="m" className="text-violet-600 dark:text-violet-300">
                            in onderhoud
                          </span>
                        ),
                      ]
                        .filter(Boolean)
                        .map((x, i) => (
                          <span key={i}>
                            {i > 0 && " · "}
                            {x}
                          </span>
                        ))}
                    </div>
                  </td>
                  <td className={tdClass}>
                    {!h.enabled ? (
                      <span className="text-xs text-slate-500">uitgeschakeld</span>
                    ) : (
                      <div className="space-y-0.5">
                        {h.interfaces.map((i, n) => (
                          <div key={n} className="flex items-center gap-1.5 text-xs" title={i.error || undefined}>
                            <AvailDot a={i.available} />
                            <span className="uppercase text-slate-500">{i.type}</span>
                            <span className="font-mono">{i.address}</span>
                          </div>
                        ))}
                        {h.active !== "unknown" && h.active !== "disabled" && (
                          <div className="flex items-center gap-1.5 text-xs">
                            <AvailDot a={h.active} />
                            <span className="text-slate-500">actieve checks</span>
                          </div>
                        )}
                        {h.interfaces.length === 0 && h.active === "unknown" && <span className="text-xs text-slate-500">{availLabel[h.available]}</span>}
                        {h.interfaces.some((i) => i.error) && (
                          <div className="max-w-xs text-xs break-words text-red-600 dark:text-red-400">{h.interfaces.find((i) => i.error)?.error}</div>
                        )}
                      </div>
                    )}
                  </td>
                  <td className={tdClass}>
                    {h.problems > 0 ? (
                      <a
                        href={links?.hostProblems(h.hostid)}
                        target="_blank"
                        rel="noreferrer"
                        className="inline-flex items-center gap-1.5 text-sm hover:underline"
                        title="Problemen in Zabbix"
                      >
                        <SeverityBadge s={h.worst} />
                        <span className="tabular-nums">{h.problems}</span>
                      </a>
                    ) : (
                      <span className="text-slate-400">—</span>
                    )}
                  </td>
                  <td className={cx(tdClass, "text-xs whitespace-nowrap")}>{h.proxy || <span className="text-slate-500">server</span>}</td>
                  <td className={cx(tdClass, "pr-5 text-xs")}>
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-slate-500">{h.node ? sourceLabel[h.node.source] ?? h.node.source : "geen node"}</span>
                      {isAdmin && (
                        <button onClick={() => setEdit(h)} className="font-medium text-brand-700 hover:underline dark:text-sky-300">
                          Wijzigen
                        </button>
                      )}
                      {links && (
                        <a href={links.host(h.hostid)} target="_blank" rel="noreferrer" className="text-slate-500 hover:underline">
                          Zabbix ↗
                        </a>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {edit && <LinkDialog host={edit} o={o} onClose={() => setEdit(null)} />}
    </Card>
  );
}

function LinkDialog({ host, o, onClose }: { host: ZabbixHost; o: ZabbixOverview; onClose: () => void }) {
  const nodes = useNodes();
  const save = useZabbixHostLink();
  const [choice, setChoice] = useState<string>(host.node?.source === "manual" ? host.node.node_id : "auto");
  const takenBy = useMemo(() => {
    const m = new Map<string, string>();
    for (const h of o.hosts) if (h.node && h.hostid !== host.hostid) m.set(h.node.node_id, h.name);
    return m;
  }, [o.hosts, host.hostid]);
  const sorted = [...(nodes.data ?? [])]
    .filter((n) => n.lifecycle !== "decommissioned")
    .sort((a, b) => (a.cluster_name ?? "~").localeCompare(b.cluster_name ?? "~", "nl") || a.hostname.localeCompare(b.hostname, "nl"));
  const submit = () => {
    const v = choice === "auto" ? { hostid: host.hostid, reset: true as const } : { hostid: host.hostid, node_id: choice === "none" ? null : choice };
    save.mutate(v, {
      onSuccess: () => {
        toast("Koppeling bewaard; het overzicht wordt bijgewerkt.");
        onClose();
      },
      onError: (e) => {
        // Geen handmatige koppeling om weg te halen: dan geldt al de automatische.
        if ("reset" in v && e instanceof ApiError && e.status === 404) onClose();
      },
    });
  };
  return (
    <Modal title={`Koppeling van ${host.name}`} onClose={onClose}>
      <div className="space-y-4">
        <p className="text-sm text-slate-600 dark:text-slate-400">
          Nu: {host.node ? `${host.node.hostname} (${sourceLabel[host.node.source] ?? host.node.source})` : "geen ClusterForge-node"}. Een node hoort bij hoogstens
          één host; kies je een node die al bij een andere host hoort, dan verhuist hij.
        </p>
        <Select value={choice} onChange={(e) => setChoice(e.target.value)} aria-label="Node">
          <option value="auto">Automatisch (via Zabbix-agents, naam of IP-adres)</option>
          <option value="none">Hoort bij geen enkele node</option>
          {sorted.map((n) => (
            <option key={n.id} value={n.id}>
              {n.hostname}
              {n.cluster_name ? ` (${n.cluster_name})` : ""}
              {takenBy.has(n.id) ? ` · nu bij ${takenBy.get(n.id)}` : ""}
            </option>
          ))}
        </Select>
        {save.isError && !(save.error instanceof ApiError && save.error.status === 404) && (
          <Alert>{save.error instanceof ApiError ? save.error.message : "Bewaren mislukte."}</Alert>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button onClick={submit} disabled={save.isPending}>
            Bewaren
          </Button>
        </div>
      </div>
    </Modal>
  );
}
