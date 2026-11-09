import { formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Badge, Card } from "@/components/ui";
import type { components } from "@/lib/api/schema";

type Seaweed = components["schemas"]["SeaweedFacts"];
type Process = Seaweed["processes"][number];

// Lokale filer-stores: de metadata staat dan alleen op deze node.
const localStores = ["leveldb", "leveldb2", "leveldb3", "sqlite", "rocksdb", "badger"];

const replicationHelp: Record<string, string> = {
  "000": "één kopie",
  "001": "2 kopieën op verschillende servers in hetzelfde rack",
  "002": "3 kopieën op verschillende servers in hetzelfde rack",
  "010": "2 kopieën in verschillende racks",
  "100": "2 kopieën in verschillende datacenters",
};

/** Wat er aan SeaweedFS op deze node beter kan, voor een cluster. */
export function seaweedWarnings(s: Seaweed): string[] {
  const out: string[] = [];
  const masters = s.processes.filter((p) => p.role === "master" || p.role === "server");
  const filers = s.processes.filter((p) => p.role === "filer" || (p.role === "server" && p.ports?.filer));
  const replications = new Set<string>();
  masters.forEach((p) => p.default_replication && replications.add(p.default_replication));
  s.master?.layouts?.forEach((l) => replications.add(l.replication));
  Object.keys(s.volume?.replications ?? {}).forEach((r) => replications.add(r));
  if (replications.has("000")) {
    out.push(
      "Replicatie 000: elk bestand staat maar op één volume server. Valt die weg, dan is die data weg. Voor 3 nodes in één rack: 002 (of 001 voor 2 kopieën).",
    );
  }
  const peers = Math.max(s.master?.peers?.length ?? 0, ...masters.map((p) => p.peers?.length ?? 0));
  if (masters.length > 0 && peers < 3) {
    out.push("Eén master zonder raft-peers: valt deze node weg, dan kan niemand nog schrijven. Voor HA 3 masters met -peers.");
  }
  const dataNodes = s.master?.data_nodes?.length ?? 0;
  if (s.master && dataNodes > 0 && dataNodes < 3) {
    out.push(`De master kent ${dataNodes} volume server${dataNodes === 1 ? "" : "s"}; replicatie 002 vraagt er minstens 3.`);
  }
  const filerCfg = s.configs?.find((c) => c.kind === "filer");
  const store = filerCfg?.enabled?.[0];
  if (filers.length > 0 && (!store || localStores.includes(store))) {
    out.push(
      `De filer bewaart zijn metadata lokaal (${store ?? "leveldb2, de standaard"}). Met meer filers is een gedeelde store nodig, zoals mysql2 op Galera of postgres2 op Patroni.`,
    );
  }
  for (const p of s.processes) {
    for (const d of p.disks ?? []) {
      if (d.total > 0 && d.free / d.total < 0.1) out.push(`${d.dir} heeft nog maar ${formatBytes(d.free)} vrij.`);
    }
  }
  return out;
}

function owner(p: Process) {
  if (p.container) return `container ${p.container}`;
  if (p.unit) return p.unit;
  return `pid ${p.pid}`;
}

export function SeaweedCard({ seaweed: s }: { seaweed: Seaweed }) {
  const warnings = seaweedWarnings(s);
  return (
    <Card title="SeaweedFS">
      <div className="space-y-4 text-sm">
        <p className="text-slate-600 dark:text-slate-300">
          {s.version ? <>Versie <span className="font-mono">{s.version}</span>. </> : null}
          {s.master && (
            <>
              Master: {s.master.is_leader ? <Badge tone="green">leider</Badge> : <Badge tone="slate">volger</Badge>}
              {s.master.leader && <span className="text-slate-500"> · leider {s.master.leader}</span>}
              {(s.master.peers?.length ?? 0) > 0 && <span className="text-slate-500"> · peers {s.master.peers!.join(", ")}</span>}
              {". "}
            </>
          )}
          {s.volume && (
            <>
              Volume server: {s.volume.volumes} volumes, {formatBytes(s.volume.bytes)}, {s.volume.files.toLocaleString("nl-BE")} bestanden
              {(s.volume.read_only ?? 0) > 0 && <> ({s.volume.read_only} alleen-lezen)</>}
              {Object.keys(s.volume.replications ?? {}).length > 0 && (
                <>
                  {" "}· replicatie{" "}
                  {Object.entries(s.volume.replications!)
                    .map(([r, n]) => `${r} (${n})`)
                    .join(", ")}
                </>
              )}
              {(s.volume.collections?.length ?? 0) > 0 && <> · collecties {s.volume.collections!.join(", ")}</>}.
            </>
          )}
        </p>

        {warnings.length > 0 && (
          <ul className="space-y-1 rounded-md bg-amber-50 px-3 py-2 text-amber-900 dark:bg-amber-950 dark:text-amber-200">
            {warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        )}

        <div className="overflow-x-auto">
          <table className={tableClass}>
            <thead className="border-b border-slate-200 dark:border-slate-800">
              <tr>
                <th className={thClass}>Rol</th>
                <th className={thClass}>Poorten</th>
                <th className={thClass}>Instellingen</th>
                <th className={thClass}>Mappen</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {s.processes.map((p) => (
                <tr key={`${p.role}-${p.pid}`}>
                  <td className={tdClass}>
                    {p.role}
                    <div className="text-[11px] text-slate-400">{owner(p)}</div>
                  </td>
                  <td className={`${tdClass} tabular-nums`}>
                    {Object.entries(p.ports ?? {})
                      .map(([k, v]) => `${k} ${v}`)
                      .join(", ") || "–"}
                  </td>
                  <td className={tdClass}>
                    <div className="space-y-0.5 text-xs">
                      {p.default_replication && (
                        <div>
                          replicatie <span className="font-mono">{p.default_replication}</span>
                          {replicationHelp[p.default_replication] && (
                            <span className="text-slate-500"> ({replicationHelp[p.default_replication]})</span>
                          )}
                        </div>
                      )}
                      {(p.peers?.length ?? 0) > 0 && <div>peers {p.peers!.join(", ")}</div>}
                      {(p.masters?.length ?? 0) > 0 && <div>masters {p.masters!.join(", ")}</div>}
                      {p.ip && <div>ip {p.ip}</div>}
                      {(p.data_center || p.rack) && (
                        <div>
                          {p.data_center && `dc ${p.data_center}`} {p.rack && `rack ${p.rack}`}
                        </div>
                      )}
                      <details>
                        <summary className="cursor-pointer text-slate-500">commandoregel</summary>
                        <code className="block break-all text-[11px]">weed {p.args.join(" ")}</code>
                      </details>
                    </div>
                  </td>
                  <td className={tdClass}>
                    {[...(p.dirs ?? []), ...(p.meta_dir ? [p.meta_dir] : [])].map((d) => {
                      const disk = p.disks?.find((x) => x.dir === d);
                      return (
                        <div key={d} className="text-xs">
                          <span className="font-mono">{d}</span>
                          {disk && (
                            <span className="text-slate-500">
                              {" "}
                              · {formatBytes(disk.free)} vrij van {formatBytes(disk.total)}
                            </span>
                          )}
                        </div>
                      );
                    })}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        {s.master && (s.master.data_nodes?.length ?? 0) > 0 && (
          <div>
            <h4 className="mb-1 text-xs font-semibold uppercase text-slate-500">Volume servers volgens de master</h4>
            <ul className="space-y-0.5 text-xs">
              {s.master.data_nodes!.map((n) => (
                <li key={n.url}>
                  <span className="font-mono">{n.url}</span>{" "}
                  <span className="text-slate-500">
                    · {n.volumes}/{n.max} volumes{n.data_center ? ` · ${n.data_center}` : ""}
                    {n.rack ? ` / ${n.rack}` : ""}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}

        {(s.configs?.length ?? 0) > 0 && (
          <div>
            <h4 className="mb-1 text-xs font-semibold uppercase text-slate-500">Configuratie</h4>
            <ul className="space-y-0.5 text-xs">
              {s.configs!.map((c) => (
                <li key={`${c.container ?? ""}${c.path}`}>
                  <span className="font-mono">{c.path}</span>
                  {c.container && <span className="text-slate-500"> (container {c.container})</span>}
                  {(c.enabled?.length ?? 0) > 0 && <span> · aan: {c.enabled!.join(", ")}</span>}
                  {c.kind === "security" && <span> · JWT {c.jwt ? "aan" : "uit"}</span>}
                </li>
              ))}
            </ul>
          </div>
        )}
        <p className="text-xs text-slate-500">Wachtwoorden en sleutels uit de vlaggen en toml-bestanden verlaten de node nooit.</p>
      </div>
    </Card>
  );
}
