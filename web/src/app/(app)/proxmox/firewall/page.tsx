"use client";

import { Suspense, useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ActionMenu, DatacenterGate, TypedConfirm, useCallRunner } from "@/components/pve/bits";
import { Check, FormModal } from "@/components/pve/guest/common";
import { Alert, Badge, Button, Card, Field, Input, PageHeader, Select } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { guestLabel, usePveGet, type Datacenter, type FwAlias, type FwGroup, type FwIpset, type FwIpsetEntry, type FwOptions, type FwRule, type PveResource } from "@/lib/pve";

// De firewall van Proxmox zelf (fase 26 M6): op datacenter-, host- en
// gastniveau. Niet te verwarren met ufw op de nodes (fase 16, pagina
// Firewall onder Beveiliging).

export default function PveFirewallPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Proxmox-firewall"
        description="De firewall die Proxmox op de hosts en voor elke gast draait. Regels van het datacenter gelden voor alle hosts; een gast heeft zijn eigen regels."
      />
      <Suspense>
        <DatacenterGate>{(dc) => <FirewallView key={dc.id} dc={dc} />}</DatacenterGate>
      </Suspense>
    </div>
  );
}

function FirewallView({ dc }: { dc: Datacenter }) {
  const res = usePveGet<PveResource[]>(dc.id, "/cluster/resources", { params: { type: "vm" }, refetch: 60_000 });
  const [scope, setScope] = useState("/cluster");
  const guests = (res.data ?? []).filter((g) => !g.template).sort((a, b) => (a.vmid ?? 0) - (b.vmid ?? 0));
  const label =
    scope === "/cluster"
      ? "het datacenter"
      : scope.split("/").length === 3
        ? scope.split("/")[2]
        : (() => {
            const g = guests.find((x) => scope === `/nodes/${x.node}/${x.type}/${x.vmid}`);
            return g ? `${guestLabel(g.type as "qemu" | "lxc")} ${g.vmid} ${g.name ?? ""}` : scope;
          })();
  return (
    <div className="space-y-4">
      <Field label="Firewall van" htmlFor="fw-scope">
        <Select id="fw-scope" value={scope} onChange={(e) => setScope(e.target.value)} className="max-w-md">
          <option value="/cluster">Datacenter (alle hosts)</option>
          <optgroup label="Host">
            {dc.hosts.map((h) => (
              <option key={h.pve_node} value={`/nodes/${h.pve_node}`}>
                {h.pve_node}
              </option>
            ))}
          </optgroup>
          <optgroup label="Gast">
            {guests.map((g) => (
              <option key={g.id} value={`/nodes/${g.node}/${g.type}/${g.vmid}`}>
                {guestLabel(g.type as "qemu" | "lxc")} {g.vmid} {g.name}
              </option>
            ))}
          </optgroup>
        </Select>
      </Field>
      <Scope key={scope} dc={dc} base={scope} label={label} />
    </div>
  );
}

const macros = ["SSH", "HTTP", "HTTPS", "DNS", "Ping", "SMTP", "IMAP", "IMAPS", "NTP", "MySQL", "PostgreSQL", "Ceph", "SMB", "NFS", "SNMP", "Web", "BitTorrent", "Zabbix"];

function Scope({ dc, base, label }: { dc: Datacenter; base: string; label: string }) {
  const isAdmin = useIsAdmin();
  const isDC = base === "/cluster";
  const isGuest = base.split("/").length > 3;
  const opts = usePveGet<FwOptions>(dc.id, `${base}/firewall/options`, { refetch: 30_000 });
  const rules = usePveGet<FwRule[]>(dc.id, `${base}/firewall/rules`, { refetch: 30_000 });
  const groups = usePveGet<FwGroup[]>(dc.id, "/cluster/firewall/groups", { refetch: 120_000 });
  const { run, pending, banner } = useCallRunner(dc.id);
  const [dialog, setDialog] = useState<{ kind: "rule"; r?: FwRule } | { kind: "enable" } | { kind: "options" } | null>(null);
  const close = () => setDialog(null);
  const enabled = !!opts.data?.enable;
  const list = [...(rules.data ?? [])].sort((a, b) => a.pos - b.pos);

  const toggle = async () => {
    if (!enabled && isDC) return setDialog({ kind: "enable" });
    if (enabled && !(await askConfirm(`Firewall van ${label} uitzetten? Alle regels hieronder stoppen dan met werken.`))) return;
    void run({ method: "set", path: `${base}/firewall/options`, params: { enable: enabled ? "0" : "1" } });
  };
  const move = (r: FwRule, to: number) => void run({ method: "set", path: `${base}/firewall/rules/${r.pos}`, params: { moveto: String(to) } });
  const remove = async (r: FwRule) => {
    if (await askConfirm(`Regel ${r.pos} (${describe(r)}) weghalen?`)) void run({ method: "delete", path: `${base}/firewall/rules/${r.pos}` });
  };

  return (
    <div className="space-y-4">
      {banner}
      <Card
        title={
          <span className="flex items-center justify-between gap-2">
            <span>Firewall van {label}</span>
            {isAdmin && opts.data && (
              <span className="flex gap-2">
                <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "options" })}>
                  Opties
                </Button>
                <Button variant={enabled ? "secondary" : "primary"} disabled={pending} onClick={() => void toggle()}>
                  {enabled ? "Uitzetten" : "Aanzetten"}
                </Button>
              </span>
            )}
          </span>
        }
      >
        <QueryState q={opts}>
          <div className="flex flex-wrap gap-x-6 gap-y-2 text-sm">
            <span>
              Status: <Badge tone={enabled ? "green" : "slate"}>{enabled ? "aan" : "uit"}</Badge>
            </span>
            {"policy_in" in (opts.data ?? {}) || isDC || isGuest ? (
              <>
                <span>
                  Binnenkomend standaard: <strong>{opts.data?.policy_in ?? "DROP"}</strong>
                </span>
                <span>
                  Uitgaand standaard: <strong>{opts.data?.policy_out ?? "ACCEPT"}</strong>
                </span>
              </>
            ) : null}
          </div>
          {isGuest && enabled && (
            <p className="mt-2 text-xs text-slate-500">Een gastfirewall werkt alleen op netwerkkaarten waar &quot;firewall=1&quot; aan staat (tabblad Hardware van de gast).</p>
          )}
          {!isDC && enabled && <p className="mt-2 text-xs text-slate-500">Werkt alleen als ook de firewall van het datacenter aan staat.</p>}
        </QueryState>
      </Card>

      <Card
        title={
          <span className="flex items-center justify-between gap-2">
            <span>Regels</span>
            {isAdmin && (
              <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "rule" })}>
                Regel toevoegen
              </Button>
            )}
          </span>
        }
      >
        <QueryState q={rules}>
          {list.length === 0 ? (
            <Empty>Geen regels. Alleen de standaard van hierboven geldt.</Empty>
          ) : (
            <div className="relative -mx-4 overflow-x-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>#</th>
                    <th className={thClass}>Richting</th>
                    <th className={thClass}>Actie</th>
                    <th className={thClass}>Wat</th>
                    <th className={thClass}>Van</th>
                    <th className={thClass}>Naar</th>
                    <th className={thClass}>Opmerking</th>
                    <th className={thClass}>
                      <span className="sr-only">Acties</span>
                    </th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {list.map((r, i) => (
                    <tr key={r.pos} className={r.enable ? "" : "opacity-50"}>
                      <td className={tdClass + " tabular-nums"}>{r.pos}</td>
                      <td className={tdClass}>{r.type === "in" ? "in" : r.type === "out" ? "uit" : "groep"}</td>
                      <td className={tdClass}>
                        <Badge tone={r.action === "ACCEPT" ? "green" : r.action === "DROP" || r.action === "REJECT" ? "red" : "blue"}>{r.action}</Badge>
                        {!r.enable && <span className="block text-xs text-slate-500">uit</span>}
                      </td>
                      <td className={tdClass + " font-mono text-xs"}>{what(r)}</td>
                      <td className={tdClass + " font-mono text-xs"}>{r.source ?? "overal"}</td>
                      <td className={tdClass + " font-mono text-xs"}>
                        {r.dest ?? "overal"}
                        {r.iface ? <span className="block text-slate-500">op {r.iface}</span> : null}
                      </td>
                      <td className={tdClass + " text-xs"}>{r.comment}</td>
                      <td className={tdClass + " text-right"}>
                        {isAdmin && (
                          <ActionMenu
                            disabled={pending}
                            items={[
                              { label: "Wijzigen", onClick: () => setDialog({ kind: "rule", r }) },
                              { label: r.enable ? "Uitzetten" : "Aanzetten", onClick: () => void run({ method: "set", path: `${base}/firewall/rules/${r.pos}`, params: { enable: r.enable ? "0" : "1" } }) },
                              ...(i > 0 ? [{ label: "Omhoog", onClick: () => move(r, list[i - 1].pos) }] : []),
                              ...(i < list.length - 1 ? [{ label: "Omlaag", onClick: () => move(r, list[i + 1].pos + 1) }] : []),
                              { label: "Weghalen", onClick: () => void remove(r), danger: true },
                            ]}
                          />
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </QueryState>
        <p className="mt-3 text-xs text-slate-500">Proxmox kijkt de regels van boven naar onder na; de eerste die past, beslist.</p>
      </Card>

      {(isDC || isGuest) && <AliasesAndSets dc={dc} base={base} isAdmin={isAdmin} run={run} pending={pending} />}

      {dialog?.kind === "rule" && <RuleDialog base={base} edit={dialog.r} groups={(groups.data ?? []).map((g) => g.group)} isGuest={isGuest} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "options" && opts.data && <OptionsDialog base={base} o={opts.data} isDC={isDC} isGuest={isGuest} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "enable" && (
        <TypedConfirm
          title="Firewall van het datacenter aanzetten"
          expect="firewall"
          confirmLabel="Aanzetten"
          pending={pending}
          onClose={close}
          onConfirm={(typed) => void run({ method: "set", path: "/cluster/firewall/options", params: { enable: "1" }, confirm: typed }).then((ok) => ok && close())}
        >
          <p className="text-sm">
            Vanaf nu geldt de standaard &quot;{opts.data?.policy_in ?? "DROP"}&quot; voor binnenkomend verkeer op alle hosts. Proxmox laat de webinterface (8006) en SSH
            vanzelf toe vanaf het clusternetwerk, maar niet vanaf andere VLAN&apos;s. Zet eerst een regel voor je beheer-VLAN (192.168.90.0/24) en voor de agent
            (ClusterForge-server naar poort 4222 gaat uitgaand, dat blijft open).
          </p>
        </TypedConfirm>
      )}
    </div>
  );
}

function what(r: FwRule) {
  if (r.type === "group") return `groep ${r.action}`;
  if (r.macro) return r.macro;
  const port = r.dport ? `:${r.dport}` : "";
  return r.proto ? `${r.proto}${port}` : port ? `poort ${r.dport}` : "alles";
}

function describe(r: FwRule) {
  return `${r.type} ${r.action} ${what(r)}${r.source ? ` van ${r.source}` : ""}`;
}

type Runner = ReturnType<typeof useCallRunner>["run"];

function RuleDialog({ base, edit, groups, isGuest, run, pending, onClose }: { base: string; edit?: FwRule; groups: string[]; isGuest: boolean; run: Runner; pending: boolean; onClose: () => void }) {
  const [type, setType] = useState(edit?.type ?? "in");
  const [action, setAction] = useState(edit?.action ?? (type === "group" ? (groups[0] ?? "") : "ACCEPT"));
  const [mode, setMode] = useState<"macro" | "port">(edit?.macro ? "macro" : edit ? "port" : "macro");
  const [macro, setMacro] = useState(edit?.macro ?? "SSH");
  const [proto, setProto] = useState(edit?.proto ?? "tcp");
  const [dport, setDport] = useState(edit?.dport ?? "");
  const [source, setSource] = useState(edit?.source ?? "");
  const [dest, setDest] = useState(edit?.dest ?? "");
  const [iface, setIface] = useState(edit?.iface ?? "");
  const [comment, setComment] = useState(edit?.comment ?? "");
  const [enable, setEnable] = useState(edit ? !!edit.enable : true);
  const [log, setLog] = useState(edit?.log && edit.log !== "nolog" ? edit.log : "");
  const isGroup = type === "group";
  const addr = /^[!+]?[A-Za-z0-9_.:/,-]*$/;
  const error =
    isGroup && !action
      ? "Er zijn nog geen beveiligingsgroepen; maak er een in Proxmox of kies in/uit."
      : !isGroup && mode === "port" && dport && !/^[0-9:,]+$/.test(dport)
        ? "Poorten zijn getallen, met : voor een reeks en , ertussen (bijvoorbeeld 80,443 of 8000:8100)."
        : !addr.test(source) || !addr.test(dest)
          ? "Van en Naar zijn een IP, subnet, alias of +ipset."
          : null;

  const submit = () => {
    const p: Record<string, string> = { type, action, enable: enable ? "1" : "0" };
    const del: string[] = [];
    const opt = (k: string, v: string) => {
      if (v) p[k] = v;
      else if (edit && (edit as Record<string, unknown>)[k] !== undefined) del.push(k);
    };
    if (!isGroup) {
      if (mode === "macro") {
        p.macro = macro;
        if (edit?.proto) del.push("proto");
        if (edit?.dport) del.push("dport");
      } else {
        opt("proto", proto);
        opt("dport", dport);
        if (edit?.macro) del.push("macro");
      }
      opt("source", source);
      opt("dest", dest);
      opt("log", log);
    }
    opt("iface", iface);
    opt("comment", comment);
    if (del.length) p.delete = del.join(",");
    const call = edit ? { method: "set" as const, path: `${base}/firewall/rules/${edit.pos}`, params: p } : { method: "create" as const, path: `${base}/firewall/rules`, params: p };
    void run(call).then((ok) => ok && onClose());
  };

  return (
    <FormModal title={edit ? `Regel ${edit.pos} wijzigen` : "Regel toevoegen"} onClose={onClose} onSubmit={submit} submitLabel="Opslaan" disabled={pending} error={error} wide>
      <div className="grid gap-3 sm:grid-cols-3">
        <Field label="Richting" htmlFor="fr-t">
          <Select id="fr-t" value={type} onChange={(e) => (setType(e.target.value), setAction(e.target.value === "group" ? (groups[0] ?? "") : "ACCEPT"))}>
            <option value="in">Binnenkomend</option>
            <option value="out">Uitgaand</option>
            {groups.length > 0 && <option value="group">Beveiligingsgroep</option>}
          </Select>
        </Field>
        <Field label={isGroup ? "Groep" : "Actie"} htmlFor="fr-a">
          <Select id="fr-a" value={action} onChange={(e) => setAction(e.target.value)}>
            {isGroup ? (
              groups.map((g) => <option key={g}>{g}</option>)
            ) : (
              <>
                <option value="ACCEPT">Toelaten</option>
                <option value="DROP">Weggooien</option>
                <option value="REJECT">Weigeren</option>
              </>
            )}
          </Select>
        </Field>
        {!isGroup && (
          <Field label="Wat" htmlFor="fr-m">
            <Select id="fr-m" value={mode} onChange={(e) => setMode(e.target.value as "macro" | "port")}>
              <option value="macro">Dienst</option>
              <option value="port">Protocol en poort</option>
            </Select>
          </Field>
        )}
        {!isGroup && mode === "macro" && (
          <Field label="Dienst" htmlFor="fr-mc">
            <Select id="fr-mc" value={macro} onChange={(e) => setMacro(e.target.value)}>
              {(macros.includes(macro) ? macros : [macro, ...macros]).map((m) => (
                <option key={m}>{m}</option>
              ))}
            </Select>
          </Field>
        )}
        {!isGroup && mode === "port" && (
          <>
            <Field label="Protocol" htmlFor="fr-p">
              <Select id="fr-p" value={proto} onChange={(e) => setProto(e.target.value)}>
                <option value="tcp">tcp</option>
                <option value="udp">udp</option>
                <option value="icmp">icmp</option>
                <option value="">alle</option>
              </Select>
            </Field>
            <Field label="Poort" htmlFor="fr-dp">
              <Input id="fr-dp" value={dport} onChange={(e) => setDport(e.target.value.trim())} placeholder="443 of 8000:8100" />
            </Field>
          </>
        )}
        {!isGroup && (
          <>
            <Field label="Van" htmlFor="fr-s" hint="Leeg is overal">
              <Input id="fr-s" value={source} onChange={(e) => setSource(e.target.value.trim())} className="font-mono" placeholder="192.168.90.0/24" />
            </Field>
            <Field label="Naar" htmlFor="fr-d" hint="Leeg is overal">
              <Input id="fr-d" value={dest} onChange={(e) => setDest(e.target.value.trim())} className="font-mono" />
            </Field>
          </>
        )}
        <Field label="Interface" htmlFor="fr-i" hint={isGuest ? "Bijvoorbeeld net0; leeg is alle" : "Leeg is alle"}>
          <Input id="fr-i" value={iface} onChange={(e) => setIface(e.target.value.trim())} className="font-mono" />
        </Field>
        {!isGroup && (
          <Field label="Loggen" htmlFor="fr-l">
            <Select id="fr-l" value={log} onChange={(e) => setLog(e.target.value)}>
              <option value="">Nee</option>
              <option value="info">Ja (info)</option>
              <option value="warning">Ja (warning)</option>
            </Select>
          </Field>
        )}
      </div>
      <Field label="Opmerking" htmlFor="fr-c">
        <Input id="fr-c" value={comment} onChange={(e) => setComment(e.target.value)} />
      </Field>
      <Check id="fr-e" checked={enable} onChange={setEnable}>
        Regel actief
      </Check>
    </FormModal>
  );
}

function OptionsDialog({ base, o, isDC, isGuest, run, pending, onClose }: { base: string; o: FwOptions; isDC: boolean; isGuest: boolean; run: Runner; pending: boolean; onClose: () => void }) {
  const [pin, setPin] = useState(o.policy_in ?? "DROP");
  const [pout, setPout] = useState(o.policy_out ?? "ACCEPT");
  const [dhcp, setDhcp] = useState(o.dhcp === undefined ? true : !!o.dhcp);
  const [mac, setMac] = useState(o.macfilter === undefined ? true : !!o.macfilter);
  const [smurfs, setSmurfs] = useState(o.nosmurfs === undefined ? true : !!o.nosmurfs);
  const [tcpflags, setTcpflags] = useState(!!o.tcpflags);
  const submit = () => {
    const p: Record<string, string> = {};
    if (isDC || isGuest) Object.assign(p, { policy_in: pin, policy_out: pout });
    if (isGuest) Object.assign(p, { dhcp: dhcp ? "1" : "0", macfilter: mac ? "1" : "0" });
    if (!isDC && !isGuest) Object.assign(p, { nosmurfs: smurfs ? "1" : "0", tcpflags: tcpflags ? "1" : "0" });
    void run({ method: "set", path: `${base}/firewall/options`, params: p }).then((ok) => ok && onClose());
  };
  return (
    <FormModal title="Firewallopties" onClose={onClose} onSubmit={submit} submitLabel="Opslaan" disabled={pending}>
      {(isDC || isGuest) && (
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Binnenkomend standaard" htmlFor="fo-in">
            <Select id="fo-in" value={pin} onChange={(e) => setPin(e.target.value)}>
              <option value="DROP">Weggooien</option>
              <option value="REJECT">Weigeren</option>
              <option value="ACCEPT">Toelaten</option>
            </Select>
          </Field>
          <Field label="Uitgaand standaard" htmlFor="fo-out">
            <Select id="fo-out" value={pout} onChange={(e) => setPout(e.target.value)}>
              <option value="ACCEPT">Toelaten</option>
              <option value="DROP">Weggooien</option>
              <option value="REJECT">Weigeren</option>
            </Select>
          </Field>
        </div>
      )}
      {isGuest && (
        <>
          <Check id="fo-dhcp" checked={dhcp} onChange={setDhcp}>
            DHCP toelaten
          </Check>
          <Check id="fo-mac" checked={mac} onChange={setMac}>
            MAC-filter (de gast mag alleen zijn eigen MAC-adres gebruiken)
          </Check>
        </>
      )}
      {!isDC && !isGuest && (
        <>
          <Check id="fo-sm" checked={smurfs} onChange={setSmurfs}>
            Smurf-aanvallen tegenhouden
          </Check>
          <Check id="fo-tcp" checked={tcpflags} onChange={setTcpflags}>
            Ongeldige TCP-vlaggen tegenhouden
          </Check>
        </>
      )}
      {pout !== "ACCEPT" && <Alert kind="info">Uitgaand blokkeren breekt apt, ghcr en Docker Hub tenzij je daar regels voor zet.</Alert>}
    </FormModal>
  );
}

function AliasesAndSets({ dc, base, isAdmin, run, pending }: { dc: Datacenter; base: string; isAdmin: boolean; run: Runner; pending: boolean }) {
  const aliases = usePveGet<FwAlias[]>(dc.id, `${base}/firewall/aliases`, { refetch: 60_000 });
  const ipsets = usePveGet<FwIpset[]>(dc.id, `${base}/firewall/ipset`, { refetch: 60_000 });
  const [dialog, setDialog] = useState<{ kind: "alias" } | { kind: "ipset" } | { kind: "entry"; set: string } | null>(null);
  const close = () => setDialog(null);
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card
        title={
          <span className="flex items-center justify-between gap-2">
            <span>Aliassen</span>
            {isAdmin && (
              <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "alias" })}>
                Toevoegen
              </Button>
            )}
          </span>
        }
      >
        <QueryState q={aliases}>
          {(aliases.data ?? []).length === 0 ? (
            <Empty>Geen aliassen. Een alias is een naam voor een adres, bruikbaar in Van en Naar.</Empty>
          ) : (
            <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
              {aliases.data!.map((a) => (
                <li key={a.name} className="flex items-center justify-between gap-2 py-1.5">
                  <span>
                    <span className="font-medium">{a.name}</span> <span className="font-mono text-xs text-slate-500">{a.cidr}</span>
                    {a.comment && <span className="block text-xs text-slate-500">{a.comment}</span>}
                  </span>
                  {isAdmin && (
                    <Button
                      variant="secondary"
                      disabled={pending}
                      onClick={async () => {
                        if (await askConfirm(`Alias ${a.name} weghalen? Regels die hem gebruiken werken dan niet meer.`)) void run({ method: "delete", path: `${base}/firewall/aliases/${a.name}` });
                      }}
                    >
                      Weghalen
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </QueryState>
      </Card>
      <Card
        title={
          <span className="flex items-center justify-between gap-2">
            <span>IP-sets</span>
            {isAdmin && (
              <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "ipset" })}>
                Toevoegen
              </Button>
            )}
          </span>
        }
      >
        <QueryState q={ipsets}>
          {(ipsets.data ?? []).length === 0 ? (
            <Empty>Geen IP-sets. Gebruik ze in een regel als +naam.</Empty>
          ) : (
            <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
              {ipsets.data!.map((s) => (
                <li key={s.name} className="py-1.5">
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-medium">+{s.name}</span>
                    {isAdmin && (
                      <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "entry", set: s.name })}>
                        Adres toevoegen
                      </Button>
                    )}
                  </div>
                  <IpsetEntries dc={dc} base={base} set={s.name} isAdmin={isAdmin} run={run} pending={pending} />
                </li>
              ))}
            </ul>
          )}
        </QueryState>
      </Card>
      {dialog && <SmallDialog kind={dialog.kind} base={base} set={dialog.kind === "entry" ? dialog.set : undefined} run={run} pending={pending} onClose={close} />}
    </div>
  );
}

function IpsetEntries({ dc, base, set, isAdmin, run, pending }: { dc: Datacenter; base: string; set: string; isAdmin: boolean; run: Runner; pending: boolean }) {
  const q = usePveGet<FwIpsetEntry[]>(dc.id, `${base}/firewall/ipset/${set}`, { refetch: 60_000 });
  if (!q.data?.length) return <p className="text-xs text-slate-500">leeg</p>;
  return (
    <ul className="mt-1 flex flex-wrap gap-1">
      {q.data.map((e) => (
        <li key={e.cidr} className="flex items-center gap-1 rounded bg-slate-100 px-2 py-0.5 font-mono text-xs dark:bg-slate-800">
          {e.nomatch ? "!" : ""}
          {e.cidr}
          {isAdmin && (
            <button type="button" aria-label={`${e.cidr} weghalen`} disabled={pending} className="text-slate-500 hover:text-red-600" onClick={() => void run({ method: "delete", path: `${base}/firewall/ipset/${set}/${e.cidr}` })}>
              ×
            </button>
          )}
        </li>
      ))}
    </ul>
  );
}

function SmallDialog({ kind, base, set, run, pending, onClose }: { kind: "alias" | "ipset" | "entry"; base: string; set?: string; run: Runner; pending: boolean; onClose: () => void }) {
  const [name, setName] = useState("");
  const [cidr, setCidr] = useState("");
  const [comment, setComment] = useState("");
  const cidrOk = /^[0-9a-fA-F.:]+(\/\d{1,3})?$/.test(cidr);
  const error = kind !== "entry" && !/^[A-Za-z][A-Za-z0-9_-]{1,63}$/.test(name) ? "Geef een naam (letters, cijfers, - en _)." : kind !== "ipset" && !cidrOk ? "Geef een IP-adres of subnet." : null;
  const submit = () => {
    const call =
      kind === "alias"
        ? { method: "create" as const, path: `${base}/firewall/aliases`, params: { name, cidr, ...(comment ? { comment } : {}) } }
        : kind === "ipset"
          ? { method: "create" as const, path: `${base}/firewall/ipset`, params: { name, ...(comment ? { comment } : {}) } }
          : { method: "create" as const, path: `${base}/firewall/ipset/${set}`, params: { cidr, ...(comment ? { comment } : {}) } };
    void run(call).then((ok) => ok && onClose());
  };
  return (
    <FormModal title={kind === "alias" ? "Alias toevoegen" : kind === "ipset" ? "IP-set toevoegen" : `Adres in +${set}`} onClose={onClose} onSubmit={submit} submitLabel="Opslaan" disabled={pending} error={error}>
      {kind !== "entry" && (
        <Field label="Naam" htmlFor="sd-n">
          <Input id="sd-n" value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
      )}
      {kind !== "ipset" && (
        <Field label="Adres of subnet" htmlFor="sd-c">
          <Input id="sd-c" value={cidr} onChange={(e) => setCidr(e.target.value.trim())} className="font-mono" placeholder="192.168.90.0/24" />
        </Field>
      )}
      <Field label="Opmerking" htmlFor="sd-cm">
        <Input id="sd-cm" value={comment} onChange={(e) => setComment(e.target.value)} />
      </Field>
    </FormModal>
  );
}
