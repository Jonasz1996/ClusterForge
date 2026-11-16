"use client";

import { Suspense, useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { Empty, QueryState } from "@/components/inventory/bits";
import { ActionMenu, DatacenterGate, TypedConfirm, useCallRunner } from "@/components/pve/bits";
import { Check, FormModal } from "@/components/pve/guest/common";
import { Alert, Badge, Button, Card, Field, Input, PageHeader, Select, Textarea } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { propKV, usePveGet, type AcmeAccount, type AcmeChallenge, type AcmeDirectory, type AcmePlugin, type CertInfo, type Datacenter, type NodeConfig } from "@/lib/pve";

// Certificaten van de Proxmox-webinterface (fase 26 M6): wat er nu op elke
// host staat, ACME (Let's Encrypt) met accounts en DNS-plugins, of een
// eigen certificaat.

export default function CertificatesPage() {
  return (
    <div className="space-y-6">
      <PageHeader title="Certificaten" description="Het certificaat van de Proxmox-webinterface op elke host, automatisch via ACME (Let's Encrypt) of een eigen certificaat." />
      <Suspense>
        <DatacenterGate>{(dc) => <CertView key={dc.id} dc={dc} />}</DatacenterGate>
      </Suspense>
    </div>
  );
}

function CertView({ dc }: { dc: Datacenter }) {
  const isAdmin = useIsAdmin();
  const { run, pending, banner } = useCallRunner(dc.id);
  return (
    <div className="space-y-4">
      {banner}
      {dc.hosts.map((h) => (
        <HostCert key={h.pve_node} dc={dc} node={h.pve_node} isAdmin={isAdmin} run={run} pending={pending} />
      ))}
      {isAdmin && <Acme dc={dc} run={run} pending={pending} />}
    </div>
  );
}

type Runner = ReturnType<typeof useCallRunner>["run"];

const days = (t?: number) => (t ? Math.floor((t * 1000 - Date.now()) / 86_400_000) : undefined);

function HostCert({ dc, node, isAdmin, run, pending }: { dc: Datacenter; node: string; isAdmin: boolean; run: Runner; pending: boolean }) {
  const info = usePveGet<CertInfo[]>(dc.id, `/nodes/${node}/certificates/info`, { refetch: 300_000 });
  const cfg = usePveGet<NodeConfig>(dc.id, `/nodes/${node}/config`, { refetch: 120_000 });
  const [dialog, setDialog] = useState<"acme" | "custom" | "revoke" | null>(null);
  const close = () => setDialog(null);
  const certs = info.data ?? [];
  const active = certs.find((c) => c.filename === "pveproxy-ssl.pem") ?? certs.find((c) => c.filename === "pve-ssl.pem");
  const custom = certs.some((c) => c.filename === "pveproxy-ssl.pem");
  const domains = Object.entries(cfg.data ?? {})
    .filter(([k]) => /^acmedomain\d+$/.test(k))
    .map(([, v]) => propKV(v as string));
  const left = days(active?.notafter);

  return (
    <Card
      title={
        <span className="flex items-center justify-between gap-2">
          <span>{node}</span>
          {isAdmin && (
            <ActionMenu
              disabled={pending}
              items={[
                { label: "ACME-domeinen instellen", onClick: () => setDialog("acme") },
                ...(domains.length ? [{ label: "Certificaat nu bestellen", onClick: () => void run({ method: "create", path: `/nodes/${node}/certificates/acme/certificate`, params: { force: "1" } }) }] : []),
                { label: "Eigen certificaat zetten", onClick: () => setDialog("custom") },
                ...(custom
                  ? [
                      {
                        label: "Terug naar het eigen Proxmox-certificaat",
                        onClick: async () => {
                          if (await askConfirm(`Het certificaat van ${node} weghalen? De webinterface gebruikt dan weer het zelfondertekende certificaat van Proxmox.`))
                            void run({ method: "delete", path: `/nodes/${node}/certificates/custom`, params: { restart: "1" } });
                        },
                      },
                    ]
                  : []),
                ...(domains.length && custom ? [{ label: "ACME-certificaat intrekken…", onClick: () => setDialog("revoke"), danger: true }] : []),
              ]}
            />
          )}
        </span>
      }
    >
      <QueryState q={info}>
        {!active ? (
          <Empty>Geen certificaat gevonden.</Empty>
        ) : (
          <div className="grid gap-2 text-sm sm:grid-cols-2">
            <div>
              <p>
                <span className="text-slate-500">Voor: </span>
                {(active.san ?? []).join(", ") || active.subject}
              </p>
              <p>
                <span className="text-slate-500">Uitgever: </span>
                {active.issuer?.replace(/^.*CN\s*=\s*/, "")}
              </p>
            </div>
            <div>
              <p>
                <span className="text-slate-500">Geldig tot: </span>
                {active.notafter ? new Date(active.notafter * 1000).toLocaleDateString("nl-BE") : "–"}{" "}
                {left !== undefined && <Badge tone={left < 0 ? "red" : left < 14 ? "amber" : "green"}>{left < 0 ? "verlopen" : `nog ${left} dagen`}</Badge>}
              </p>
              <p>
                <span className="text-slate-500">Soort: </span>
                {custom ? (domains.length ? "ACME" : "eigen certificaat") : "zelfondertekend (Proxmox)"}
              </p>
            </div>
          </div>
        )}
        {domains.length > 0 && (
          <p className="mt-2 text-xs text-slate-500">
            ACME-domeinen: {domains.map((d) => `${d.domain ?? d[""]}${d.plugin ? ` (${d.plugin})` : " (http)"}`).join(", ")}. Proxmox vernieuwt zelf vanaf 30 dagen voor het verloopt; staat hier minder, dan lukt het vernieuwen niet (kijk in Syslog of Proxmox-taken).
          </p>
        )}
      </QueryState>
      {dialog === "acme" && cfg.data && <AcmeDomainsDialog dc={dc} node={node} cfg={cfg.data} run={run} pending={pending} onClose={close} />}
      {dialog === "custom" && <CustomDialog node={node} run={run} pending={pending} onClose={close} />}
      {dialog === "revoke" && (
        <TypedConfirm
          title={`ACME-certificaat van ${node} intrekken`}
          expect={node}
          confirmLabel="Intrekken"
          pending={pending}
          onClose={close}
          onConfirm={(typed) => void run({ method: "delete", path: `/nodes/${node}/certificates/acme/certificate`, confirm: typed }).then((ok) => ok && close())}
        >
          <p className="text-sm">Let&apos;s Encrypt trekt het certificaat in; browsers vertrouwen het niet meer. Gebruik dit alleen als de sleutel gelekt is.</p>
        </TypedConfirm>
      )}
    </Card>
  );
}

function AcmeDomainsDialog({ dc, node, cfg, run, pending, onClose }: { dc: Datacenter; node: string; cfg: NodeConfig; run: Runner; pending: boolean; onClose: () => void }) {
  const accounts = usePveGet<AcmeAccount[]>(dc.id, "/cluster/acme/account", { refetch: false });
  const plugins = usePveGet<AcmePlugin[]>(dc.id, "/cluster/acme/plugins", { refetch: false });
  const initial = Object.entries(cfg)
    .filter(([k]) => /^acmedomain\d+$/.test(k))
    .map(([, v]) => {
      const p = propKV(v as string);
      return { domain: p.domain ?? p[""] ?? "", plugin: p.plugin ?? "" };
    });
  const [rows, setRows] = useState(initial.length ? initial : [{ domain: "", plugin: "" }]);
  const [account, setAccount] = useState(propKV(cfg.acme).account ?? "default");
  const dns = (plugins.data ?? []).filter((p) => p.type === "dns");
  const valid = rows.filter((r) => r.domain.trim());
  const error = valid.some((r) => !/^[A-Za-z0-9*][A-Za-z0-9.-]*\.[A-Za-z]{2,}$/.test(r.domain.trim()))
    ? "Een domein ziet eruit als pve1.voorbeeld.be."
    : valid.length > 6
      ? "Proxmox neemt hoogstens 6 domeinen per host."
      : !(accounts.data ?? []).some((a) => a.name === account) && valid.length
        ? "Maak eerst een ACME-account (onderaan de pagina)."
        : null;
  const submit = () => {
    const p: Record<string, string> = {};
    const del: string[] = [];
    for (let i = 0; i < 6; i++) {
      const r = valid[i];
      if (r) p[`acmedomain${i}`] = `domain=${r.domain.trim()}${r.plugin ? `,plugin=${r.plugin}` : ""}`;
      else if ((cfg as Record<string, unknown>)[`acmedomain${i}`] !== undefined) del.push(`acmedomain${i}`);
    }
    if (valid.length) p.acme = `account=${account}`;
    else if (cfg.acme) del.push("acme");
    if (del.length) p.delete = del.join(",");
    void run({ method: "set", path: `/nodes/${node}/config`, params: p }).then((ok) => ok && onClose());
  };
  return (
    <FormModal title={`ACME op ${node}`} onClose={onClose} onSubmit={submit} submitLabel="Opslaan" disabled={pending} error={error} wide>
      <Field label="Account" htmlFor="ac-acc">
        <Select id="ac-acc" value={account} onChange={(e) => setAccount(e.target.value)}>
          {(accounts.data ?? []).map((a) => (
            <option key={a.name}>{a.name}</option>
          ))}
          {!(accounts.data ?? []).length && <option value="default">geen account</option>}
        </Select>
      </Field>
      <div className="space-y-2">
        {rows.map((r, i) => (
          <div key={i} className="grid gap-2 sm:grid-cols-[1fr_14rem_auto]">
            <Input aria-label={`Domein ${i + 1}`} placeholder="pve1.voorbeeld.be" value={r.domain} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, domain: e.target.value } : x)))} />
            <Select aria-label={`Controle ${i + 1}`} value={r.plugin} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, plugin: e.target.value } : x)))}>
              <option value="">HTTP (poort 80 open naar deze host)</option>
              {dns.map((p) => (
                <option key={p.plugin} value={p.plugin}>
                  DNS via {p.plugin} ({p.api})
                </option>
              ))}
            </Select>
            <Button variant="secondary" type="button" onClick={() => setRows(rows.filter((_, j) => j !== i))} disabled={rows.length === 1}>
              Weg
            </Button>
          </div>
        ))}
        <Button variant="secondary" type="button" onClick={() => setRows([...rows, { domain: "", plugin: dns[0]?.plugin ?? "" }])} disabled={rows.length >= 6}>
          Domein toevoegen
        </Button>
      </div>
      <p className="text-xs text-slate-500">Voor een host binnen je LAN werkt de DNS-controle het best: dan hoeft niets van buitenaf naar binnen.</p>
    </FormModal>
  );
}

function CustomDialog({ node, run, pending, onClose }: { node: string; run: Runner; pending: boolean; onClose: () => void }) {
  const [cert, setCert] = useState("");
  const [key, setKey] = useState("");
  const error = !/-----BEGIN CERTIFICATE-----/.test(cert)
    ? "Plak het certificaat in PEM-vorm (met de tussencertificaten erachter)."
    : !/-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(key)
      ? "Plak de privésleutel in PEM-vorm."
      : null;
  return (
    <FormModal
      title={`Eigen certificaat op ${node}`}
      onClose={onClose}
      onSubmit={() => void run({ method: "create", path: `/nodes/${node}/certificates/custom`, params: { certificates: cert.trim(), key: key.trim(), restart: "1", force: "1" } }).then((ok) => ok && onClose())}
      submitLabel="Zetten"
      disabled={pending}
      error={error}
      wide
    >
      <Field label="Certificaat (PEM)" htmlFor="cc-c">
        <Textarea id="cc-c" rows={6} value={cert} onChange={(e) => setCert(e.target.value)} className="font-mono text-xs" />
      </Field>
      <Field label="Privésleutel (PEM)" htmlFor="cc-k" hint="Gaat versleuteld naar de host en komt nooit in een logboek">
        <Textarea id="cc-k" rows={5} value={key} onChange={(e) => setKey(e.target.value)} className="font-mono text-xs" />
      </Field>
      <p className="text-xs text-slate-500">De webinterface van {node} herstart even; open sessies blijven werken.</p>
    </FormModal>
  );
}

function Acme({ dc, run, pending }: { dc: Datacenter; run: Runner; pending: boolean }) {
  const accounts = usePveGet<AcmeAccount[]>(dc.id, "/cluster/acme/account", { refetch: 120_000 });
  const plugins = usePveGet<AcmePlugin[]>(dc.id, "/cluster/acme/plugins", { refetch: 120_000 });
  const [dialog, setDialog] = useState<"account" | "plugin" | null>(null);
  const close = () => setDialog(null);
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card
        title={
          <span className="flex items-center justify-between gap-2">
            <span>ACME-accounts</span>
            <Button variant="secondary" disabled={pending} onClick={() => setDialog("account")}>
              Toevoegen
            </Button>
          </span>
        }
      >
        <QueryState q={accounts}>
          {(accounts.data ?? []).length === 0 ? (
            <Empty>Nog geen account bij Let&apos;s Encrypt.</Empty>
          ) : (
            <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
              {accounts.data!.map((a) => (
                <li key={a.name} className="flex items-center justify-between py-1.5">
                  {a.name}
                  <Button
                    variant="secondary"
                    disabled={pending}
                    onClick={async () => {
                      if (await askConfirm(`Account ${a.name} opzeggen bij de ACME-dienst? Hosts die het gebruiken, vernieuwen dan niet meer.`))
                        void run({ method: "delete", path: `/cluster/acme/account/${a.name}` });
                    }}
                  >
                    Opzeggen
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </QueryState>
      </Card>
      <Card
        title={
          <span className="flex items-center justify-between gap-2">
            <span>DNS-plugins</span>
            <Button variant="secondary" disabled={pending} onClick={() => setDialog("plugin")}>
              Toevoegen
            </Button>
          </span>
        }
      >
        <QueryState q={plugins}>
          {(plugins.data ?? []).filter((p) => p.type === "dns").length === 0 ? (
            <Empty>Geen DNS-plugins. Met een plugin (bijvoorbeeld Cloudflare) bewijst Proxmox via DNS dat het domein van jou is.</Empty>
          ) : (
            <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
              {plugins
                .data!.filter((p) => p.type === "dns")
                .map((p) => (
                  <li key={p.plugin} className="flex items-center justify-between py-1.5">
                    <span>
                      {p.plugin} <span className="text-slate-500">{p.api}</span>
                    </span>
                    <Button
                      variant="secondary"
                      disabled={pending}
                      onClick={async () => {
                        if (await askConfirm(`Plugin ${p.plugin} weghalen?`)) void run({ method: "delete", path: `/cluster/acme/plugins/${p.plugin}` });
                      }}
                    >
                      Weghalen
                    </Button>
                  </li>
                ))}
            </ul>
          )}
        </QueryState>
      </Card>
      {dialog === "account" && <AccountDialog dc={dc} taken={(accounts.data ?? []).map((a) => a.name)} run={run} pending={pending} onClose={close} />}
      {dialog === "plugin" && <PluginDialog dc={dc} taken={(plugins.data ?? []).map((p) => p.plugin)} run={run} pending={pending} onClose={close} />}
    </div>
  );
}

function AccountDialog({ dc, taken, run, pending, onClose }: { dc: Datacenter; taken: string[]; run: Runner; pending: boolean; onClose: () => void }) {
  const dirs = usePveGet<AcmeDirectory[]>(dc.id, "/cluster/acme/directories", { refetch: false });
  const [name, setName] = useState(taken.includes("default") ? "" : "default");
  const [contact, setContact] = useState("");
  const [directory, setDirectory] = useState("");
  const dir = directory || dirs.data?.[0]?.url || "";
  const tos = usePveGet<string>(dc.id, "/cluster/acme/tos", { params: { directory: dir }, refetch: false, enabled: !!dir });
  const [agree, setAgree] = useState(false);
  const error = !/^[A-Za-z0-9_-]{1,64}$/.test(name)
    ? "Geef een naam (letters, cijfers, - en _)."
    : taken.includes(name)
      ? "Die naam bestaat al."
      : !/^\S+@\S+\.\S+$/.test(contact)
        ? "Geef een e-mailadres voor meldingen over verlopen."
        : tos.data && !agree
          ? "Ga akkoord met de voorwaarden van de ACME-dienst."
          : null;
  const submit = () => {
    const p: Record<string, string> = { name, contact, directory: dir };
    if (tos.data) p.tos_url = tos.data;
    void run({ method: "create", path: "/cluster/acme/account", params: p }).then((ok) => ok && onClose());
  };
  return (
    <FormModal title="ACME-account toevoegen" onClose={onClose} onSubmit={submit} submitLabel="Registreren" disabled={pending} error={error}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Naam" htmlFor="aa-n">
          <Input id="aa-n" value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="E-mail" htmlFor="aa-c">
          <Input id="aa-c" type="email" value={contact} onChange={(e) => setContact(e.target.value.trim())} />
        </Field>
      </div>
      <Field label="Dienst" htmlFor="aa-d">
        <Select id="aa-d" value={dir} onChange={(e) => (setDirectory(e.target.value), setAgree(false))}>
          {(dirs.data ?? []).map((d) => (
            <option key={d.url} value={d.url}>
              {d.name}
            </option>
          ))}
        </Select>
      </Field>
      {tos.data && (
        <Check id="aa-tos" checked={agree} onChange={setAgree}>
          Ik ga akkoord met de{" "}
          <a href={tos.data} target="_blank" rel="noreferrer" className="underline">
            voorwaarden
          </a>
          .
        </Check>
      )}
    </FormModal>
  );
}

function PluginDialog({ dc, taken, run, pending, onClose }: { dc: Datacenter; taken: string[]; run: Runner; pending: boolean; onClose: () => void }) {
  const schema = usePveGet<AcmeChallenge[]>(dc.id, "/cluster/acme/challenge-schema", { refetch: false });
  const apis = (schema.data ?? []).filter((c) => c.type === "dns").sort((a, b) => (a.name ?? a.id).localeCompare(b.name ?? b.id));
  const [id, setId] = useState("");
  const [api, setApi] = useState("cf");
  const [data, setData] = useState("");
  const error = !/^[A-Za-z0-9_-]{1,64}$/.test(id)
    ? "Geef de plugin een naam."
    : taken.includes(id)
      ? "Die naam bestaat al."
      : !data.trim()
        ? "Vul de gegevens van de DNS-dienst in, één per regel (bijvoorbeeld CF_Token=…)."
        : data.split("\n").some((l) => l.trim() && !/^[A-Za-z_][A-Za-z0-9_]*=/.test(l.trim()))
          ? "Elke regel is SLEUTEL=waarde."
          : null;
  const submit = () => {
    const text = data
      .split("\n")
      .map((l) => l.trim())
      .filter(Boolean)
      .join("\n");
    const b64 = btoa(unescape(encodeURIComponent(text)));
    void run({ method: "create", path: "/cluster/acme/plugins", params: { id, type: "dns", api, data: b64 } }).then((ok) => ok && onClose());
  };
  return (
    <FormModal title="DNS-plugin toevoegen" onClose={onClose} onSubmit={submit} submitLabel="Opslaan" disabled={pending} error={error} wide>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Naam" htmlFor="ap-n">
          <Input id="ap-n" value={id} onChange={(e) => setId(e.target.value)} placeholder="cloudflare" />
        </Field>
        <Field label="DNS-dienst" htmlFor="ap-a">
          <Select id="ap-a" value={api} onChange={(e) => setApi(e.target.value)}>
            {apis.length ? (
              apis.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name ?? a.id}
                </option>
              ))
            ) : (
              <option value="cf">Cloudflare</option>
            )}
          </Select>
        </Field>
      </div>
      <Field label="Gegevens" htmlFor="ap-d" hint="Zoals in de documentatie van acme.sh voor deze dienst, bijvoorbeeld CF_Token=… en CF_Account_ID=…">
        <Textarea id="ap-d" rows={4} value={data} onChange={(e) => setData(e.target.value)} className="font-mono text-xs" />
      </Field>
      <Alert kind="info">De gegevens gaan versleuteld naar Proxmox en komen niet in het takenlogboek.</Alert>
    </FormModal>
  );
}
