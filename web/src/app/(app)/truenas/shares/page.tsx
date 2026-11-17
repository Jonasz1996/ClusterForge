"use client";

import { Suspense, useState } from "react";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { TrueNASGate, useTrueNASRunner } from "@/components/truenas-bits";
import { Alert, Badge, Button, Card, Input, PageHeader } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { serviceLabel, useTrueNASGet, type TNNfsShare, type TNService, type TNSmbShare, type TrueNASServer } from "@/lib/truenas";

export default function TrueNASSharesPage() {
  return (
    <div className="space-y-6">
      <PageHeader title="Shares en diensten" description="De SMB- en NFS-shares van TrueNAS en de diensten erachter. Zet een share aan of uit en start, stop of herstart een dienst." />
      <Suspense>
        <TrueNASGate>{(s) => <SharesView key={s.node_id} srv={s} />}</TrueNASGate>
      </Suspense>
    </div>
  );
}

function SharesView({ srv }: { srv: TrueNASServer }) {
  const id = srv.node_id;
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const runner = useTrueNASRunner(id);
  const smb = useTrueNASGet<TNSmbShare[]>(id, "sharing.smb.query");
  const nfs = useTrueNASGet<TNNfsShare[]>(id, "sharing.nfs.query");
  const svc = useTrueNASGet<TNService[]>(id, "service.query", undefined, { refetch: 15_000 });
  const [stopSSH, setStopSSH] = useState("");
  const toggle = (method: string, s: { id: number; enabled: boolean }) =>
    admin && (
      <Button variant="ghost" disabled={runner.pending} onClick={() => runner.run({ method, args: [s.id, { enabled: !s.enabled }] })}>
        {s.enabled ? "Uitzetten" : "Aanzetten"}
      </Button>
    );
  return (
    <div className="space-y-6">
      {runner.banner}
      <Card title="SMB-shares">
        <QueryState q={smb}>
          {(smb.data ?? []).length === 0 ? (
            <Empty>Geen SMB-shares.</Empty>
          ) : (
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Naam</th>
                  <th className={thClass}>Pad</th>
                  <th className={thClass}>Toestand</th>
                  <th className={thClass} />
                </tr>
              </thead>
              <tbody>
                {(smb.data ?? []).map((s) => (
                  <tr key={s.id}>
                    <td className={tdClass}>
                      <span className="font-medium">{s.name}</span>
                      {s.comment && <div className="text-xs text-slate-500">{s.comment}</div>}
                    </td>
                    <td className={tdClass}>
                      <span className="font-mono text-xs">{s.path}</span>
                    </td>
                    <td className={tdClass}>
                      <Badge tone={s.enabled ? "green" : "slate"}>{s.enabled ? "aan" : "uit"}</Badge> {s.ro && <Badge>alleen lezen</Badge>}
                    </td>
                    <td className={tdClass}>{toggle("sharing.smb.update", s)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </QueryState>
      </Card>
      <Card title="NFS-shares">
        <QueryState q={nfs}>
          {(nfs.data ?? []).length === 0 ? (
            <Empty>Geen NFS-shares.</Empty>
          ) : (
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Pad</th>
                  <th className={thClass}>Wie</th>
                  <th className={thClass}>Toestand</th>
                  <th className={thClass} />
                </tr>
              </thead>
              <tbody>
                {(nfs.data ?? []).map((s) => (
                  <tr key={s.id}>
                    <td className={tdClass}>
                      <span className="font-mono text-xs">{s.path}</span>
                      {s.comment && <div className="text-xs text-slate-500">{s.comment}</div>}
                    </td>
                    <td className={tdClass}>{[...(s.networks ?? []), ...(s.hosts ?? [])].join(", ") || "iedereen"}</td>
                    <td className={tdClass}>
                      <Badge tone={s.enabled ? "green" : "slate"}>{s.enabled ? "aan" : "uit"}</Badge> {s.ro && <Badge>alleen lezen</Badge>}
                    </td>
                    <td className={tdClass}>{toggle("sharing.nfs.update", s)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </QueryState>
      </Card>
      <Card title="Diensten">
        <QueryState q={svc}>
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Dienst</th>
                <th className={thClass}>Toestand</th>
                <th className={thClass}>Bij opstarten</th>
                {admin && <th className={thClass} />}
              </tr>
            </thead>
            <tbody>
              {[...(svc.data ?? [])]
                .sort((a, b) => Number(b.state === "RUNNING") - Number(a.state === "RUNNING") || a.service.localeCompare(b.service))
                .map((s) => {
                  const running = s.state === "RUNNING";
                  const ctl = (verb: string, confirm?: string) => runner.run({ method: "service.control", args: [verb, s.service, {}], confirm });
                  return (
                    <tr key={s.id}>
                      <td className={tdClass}>{serviceLabel[s.service] ?? s.service}</td>
                      <td className={tdClass}>
                        <Badge tone={running ? "green" : "slate"}>{running ? "draait" : "gestopt"}</Badge>
                      </td>
                      <td className={tdClass}>{s.enable ? "start automatisch" : "–"}</td>
                      {admin && (
                        <td className={tdClass}>
                          <div className="flex flex-wrap gap-1">
                            {running ? (
                              <>
                                <Button variant="ghost" disabled={runner.pending} onClick={() => ctl("RESTART")}>
                                  Herstarten
                                </Button>
                                <Button variant="ghost" disabled={runner.pending} onClick={() => (s.service === "ssh" ? setStopSSH("ssh?") : ctl("STOP"))}>
                                  Stoppen
                                </Button>
                              </>
                            ) : (
                              <Button variant="ghost" disabled={runner.pending} onClick={() => ctl("START")}>
                                Starten
                              </Button>
                            )}
                            <Button variant="ghost" disabled={runner.pending} onClick={() => runner.run({ method: "service.update", args: [s.id, { enable: !s.enable }] })}>
                              {s.enable ? "Niet automatisch" : "Automatisch starten"}
                            </Button>
                          </div>
                          {s.service === "ssh" && stopSSH && (
                            <div className="mt-2 space-y-2">
                              <Alert kind="error">Zonder SSH kom je alleen nog via de webinterface van TrueNAS of ClusterForge binnen. Tik ssh over.</Alert>
                              <div className="flex gap-2">
                                <Input value={stopSSH === "ssh?" ? "" : stopSSH} onChange={(e) => setStopSSH(e.target.value)} aria-label="ssh overtikken" />
                                <Button variant="danger" disabled={stopSSH !== "ssh"} onClick={() => void ctl("STOP", "ssh").then(() => setStopSSH(""))}>
                                  Stoppen
                                </Button>
                              </div>
                            </div>
                          )}
                        </td>
                      )}
                    </tr>
                  );
                })}
            </tbody>
          </table>
        </QueryState>
      </Card>
    </div>
  );
}
