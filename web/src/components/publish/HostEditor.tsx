"use client";

import { useState } from "react";
import { Modal } from "@/components/Modal";
import { ActionModal } from "@/components/publish/ActionModal";
import { Button, Field, Input, Select } from "@/components/ui";
import type { PublishAction, PublishHost, PublishOverview } from "@/lib/publish";

// HostEditor: een proxy host toevoegen of volledig bewerken (fase 42): namen,
// doel, certificaat, SSL-opties en de eigen Advanced. Het blok van
// ClusterForge voor Authentik blijft staan; dat gaat aan of uit met de knop.
// Eerst "Voorbeeld" voor het plan, dan pas uitvoeren.

type Form = {
  names: string;
  scheme: string;
  host: string;
  port: string;
  ssl: string;
  sslForced: boolean;
  http2: boolean;
  hsts: boolean;
  websockets: boolean;
  blockExploits: boolean;
  advanced: string;
  authentik: boolean;
};

function initial(h: PublishHost | null, ov: PublishOverview): Form {
  const d = ov.settings.defaults;
  if (!h) {
    return {
      names: "",
      scheme: "http",
      host: "",
      port: "",
      ssl: "auto",
      sslForced: d.ssl_forced,
      http2: d.http2,
      hsts: d.hsts,
      websockets: d.websockets,
      blockExploits: d.block_exploits,
      advanced: "",
      authentik: d.authentik,
    };
  }
  return {
    names: h.domains.join("\n"),
    scheme: h.forward_scheme,
    host: h.forward_host,
    port: String(h.forward_port),
    ssl: "",
    sslForced: h.ssl_forced,
    http2: h.http2,
    hsts: h.hsts,
    websockets: h.websockets,
    blockExploits: h.block_exploits,
    advanced: h.advanced ?? "",
    authentik: false,
  };
}

function toAction(f: Form, h: PublishHost | null): PublishAction {
  const names = f.names
    .split(/[\s,]+/)
    .map((x) => x.trim().toLowerCase())
    .filter(Boolean);
  const port = Number(f.port) || 0;
  if (!h) {
    return {
      action: "publish",
      label: names[0] ?? "",
      forward_scheme: f.scheme,
      forward_host: f.host.trim(),
      forward_port: port,
      ssl: f.ssl,
      websockets: f.websockets,
      block_exploits: f.blockExploits,
      http2: f.http2,
      hsts: f.hsts,
      authentik: f.authentik,
    };
  }
  const a: PublishAction = { action: "update", host_id: h.id };
  if (names.join(" ") !== h.domains.join(" ")) a.domains = names;
  if (f.scheme !== h.forward_scheme) a.forward_scheme = f.scheme;
  if (f.host.trim() !== h.forward_host) a.forward_host = f.host.trim();
  if (port !== h.forward_port) a.forward_port = port;
  if (f.ssl) a.ssl = f.ssl;
  if (f.sslForced !== h.ssl_forced) a.ssl_forced = f.sslForced;
  if (f.http2 !== h.http2) a.http2 = f.http2;
  if (f.hsts !== h.hsts) a.hsts = f.hsts;
  if (f.websockets !== h.websockets) a.websockets = f.websockets;
  if (f.blockExploits !== h.block_exploits) a.block_exploits = f.blockExploits;
  if (f.advanced.trim() !== (h.advanced ?? "").trim()) a.advanced_config = f.advanced;
  return a;
}

export function HostEditor({ host, ov, onClose }: { host: PublishHost | null; ov: PublishOverview; onClose: () => void }) {
  const [f, setF] = useState<Form>(() => initial(host, ov));
  const [shown, setShown] = useState<PublishAction | null>(null);
  const draft = toAction(f, host);
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setF((x) => ({ ...x, [k]: v }));
  const certs = ov.certs ?? [];
  const blocked = !!shown && JSON.stringify(shown) !== JSON.stringify(draft);

  const fields = (
    <div className="space-y-3">
      <Field label={host ? "Namen (één per regel)" : "Naam of subdomein"} htmlFor="he-names">
        {host ? (
          <textarea
            id="he-names"
            rows={Math.min(4, Math.max(2, host.domains.length))}
            className="w-full rounded border border-slate-300 bg-white p-2 font-mono text-xs dark:border-slate-700 dark:bg-slate-900"
            value={f.names}
            onChange={(e) => set("names", e.target.value)}
          />
        ) : (
          <Input id="he-names" value={f.names} placeholder={`app of app.${ov.settings.domain}`} onChange={(e) => set("names", e.target.value)} />
        )}
      </Field>
      {host?.typo_domains && (
        <Button variant="secondary" onClick={() => set("names", host.typo_domains!.join("\n"))}>
          Tikfout rechtzetten: {host.typo}
        </Button>
      )}
      <div className="grid gap-3 sm:grid-cols-[7rem_1fr_7rem]">
        <Field label="Schema" htmlFor="he-scheme">
          <Select id="he-scheme" value={f.scheme} onChange={(e) => set("scheme", e.target.value)}>
            <option value="http">http</option>
            <option value="https">https</option>
          </Select>
        </Field>
        <Field label="Doel (IP of naam)" htmlFor="he-host">
          <Input id="he-host" value={f.host} onChange={(e) => set("host", e.target.value)} />
        </Field>
        <Field label="Poort" htmlFor="he-port">
          <Input id="he-port" inputMode="numeric" value={f.port} onChange={(e) => set("port", e.target.value)} />
        </Field>
      </div>
      {host?.suggest && (
        <Button
          variant="secondary"
          onClick={() =>
            setF((x) => ({
              ...x,
              scheme: host.suggest!.forward_scheme,
              host: host.suggest!.forward_host,
              port: String(host.suggest!.forward_port),
            }))
          }
        >
          Voorstel invullen: {host.suggest.forward_scheme}://{host.suggest.forward_host}:{host.suggest.forward_port}
        </Button>
      )}
      <Field label="Certificaat" htmlFor="he-ssl">
        <Select id="he-ssl" value={f.ssl} onChange={(e) => set("ssl", e.target.value)}>
          {host && (
            <option value="">ongewijzigd{host.certificate_name ? ` (${host.certificate_name})` : host.certificate_id ? "" : " (geen)"}</option>
          )}
          <option value="auto">automatisch: passend certificaat, anders Let&apos;s Encrypt</option>
          <option value="new">nieuw aanvragen bij Let&apos;s Encrypt</option>
          <option value="none">geen (alleen http)</option>
          {certs.map((c) => (
            <option key={c.id} value={String(c.id)}>
              {c.name}
              {c.expires ? ` · tot ${new Date(c.expires).toLocaleDateString("nl-BE")}` : ""}
            </option>
          ))}
        </Select>
      </Field>
      <div className="flex flex-wrap gap-x-5 gap-y-2">
        {(
          [
            ["sslForced", "SSL afdwingen"],
            ["http2", "HTTP/2"],
            ["hsts", "HSTS"],
            ["websockets", "Websockets"],
            ["blockExploits", "Exploits blokkeren"],
            ...(host ? [] : ([["authentik", "Achter Authentik"]] as const)),
          ] as const
        ).map(([k, l]) => (
          <label key={k} className="flex items-center gap-1">
            <input type="checkbox" checked={f[k]} onChange={(e) => set(k, e.target.checked)} />
            {l}
          </label>
        ))}
      </div>
      <Field label="Eigen Advanced (nginx)" htmlFor="he-adv">
        <textarea
          id="he-adv"
          rows={5}
          className="w-full rounded border border-slate-300 bg-white p-2 font-mono text-xs dark:border-slate-700 dark:bg-slate-900"
          value={f.advanced}
          placeholder="client_max_body_size 0;"
          onChange={(e) => set("advanced", e.target.value)}
        />
      </Field>
      {host?.authentik === "beschermd" && (
        <p className="text-xs text-slate-500">De forward auth van Authentik blijft staan; die gaat aan of uit met de knop in de lijst.</p>
      )}
      <Button onClick={() => setShown(draft)}>{shown ? "Voorbeeld bijwerken" : "Voorbeeld"}</Button>
    </div>
  );

  const title = host ? `${host.domains[0]} bewerken` : "Proxy host toevoegen";
  if (!shown) {
    return (
      <Modal title={title} onClose={onClose} wide>
        <div className="space-y-4 text-sm">{fields}</div>
      </Modal>
    );
  }
  return (
    <ActionModal title={title} action={shown} onClose={onClose} blocked={blocked}>
      {fields}
    </ActionModal>
  );
}
