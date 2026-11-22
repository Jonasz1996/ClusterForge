"use client";

import { useState } from "react";
import { ActionModal } from "@/components/publish/ActionModal";
import { Alert, Button, Card, Field, Input, Select } from "@/components/ui";
import {
  usePublishOverview,
  useSavePublishSettings,
  useTestPublish,
  type PublishDefaults,
  type PublishOverview,
  type PublishSettings,
} from "@/lib/publish";

// PublishSettingsCard: het hoofddomein (jbogaert.be), de standaarden voor
// nieuwe proxy hosts en de koppeling met NPM en Authentik (fase 35). De
// geheimen gaan versleuteld naar de server en komen nooit terug.
export function PublishSettingsCard({ isAdmin }: { isAdmin: boolean }) {
  const q = usePublishOverview();
  if (!q.data) return <Card title="Domeinen en publicatie">{q.isError ? <Alert>{(q.error as Error).message}</Alert> : null}</Card>;
  return <Form ov={q.data} isAdmin={isAdmin} />;
}

type Where = { cluster?: string; node?: string };

function whereOf(cluster?: string, node?: string): string {
  return cluster ? `c:${cluster}` : node ? `n:${node}` : "";
}

function parseWhere(v: string): Where {
  if (v.startsWith("c:")) return { cluster: v.slice(2) };
  if (v.startsWith("n:")) return { node: v.slice(2) };
  return {};
}

function Form({ ov, isAdmin }: { ov: PublishOverview; isAdmin: boolean }) {
  const st: PublishSettings = ov.settings;
  const save = useSavePublishSettings();
  const test = useTestPublish();
  const [domain, setDomain] = useState(st.domain);
  const [d, setD] = useState<PublishDefaults>(st.defaults);
  const [skip, setSkip] = useState((st.defaults.authentik_skip ?? []).join(", "));
  const [npmWhere, setNpmWhere] = useState(whereOf(st.npm_cluster_id, st.npm_node_id));
  const [npmPort, setNpmPort] = useState(String(st.npm_port));
  const [npmUser, setNpmUser] = useState(st.npm_identity);
  const [npmSecret, setNpmSecret] = useState("");
  const [akWhere, setAkWhere] = useState(whereOf(st.authentik_cluster_id, st.authentik_node_id));
  const [akPort, setAkPort] = useState(String(st.authentik_port));
  const [akToken, setAkToken] = useState("");
  const [outpost, setOutpost] = useState(st.authentik_outpost);
  const [renameFrom, setRenameFrom] = useState<string | null>(null);
  const [renaming, setRenaming] = useState(false);
  const [keepOld, setKeepOld] = useState(true);
  const [tested, setTested] = useState<{
    what: string;
    text: string;
    ok: boolean;
  } | null>(null);

  const managedUnder = (dom: string) => ov.hosts.filter((h) => h.managed && h.domains.some((x) => x.endsWith("." + dom))).length;

  const submit = async (extra: { clear_npm?: boolean; clear_authentik?: boolean } = {}) => {
    const npm = parseWhere(npmWhere);
    const ak = parseWhere(akWhere);
    const old = st.domain;
    try {
      const out = await save.mutateAsync({
        domain,
        defaults: {
          ...d,
          authentik_skip: skip
            .split(/[,\s]+/)
            .map((s) => s.trim())
            .filter(Boolean),
        },
        npm_cluster_id: npm.cluster,
        npm_node_id: npm.node,
        npm_port: Number(npmPort) || undefined,
        npm_identity: npmUser,
        npm_secret: npmSecret || undefined,
        authentik_cluster_id: ak.cluster,
        authentik_node_id: ak.node,
        authentik_port: Number(akPort) || undefined,
        authentik_token: akToken || undefined,
        authentik_outpost: outpost,
        ...extra,
      });
      if (out.domain !== old && managedUnder(old) > 0) setRenameFrom(old);
    } catch {
      /* de fout staat onder het formulier */
    }
  };

  const runTest = (what: "npm" | "authentik") =>
    test.mutate(what, {
      onSuccess: (r) =>
        setTested({
          what,
          ok: true,
          text: Object.entries(r)
            .map(([k, v]) => `${k}: ${v}`)
            .join(" · "),
        }),
      onError: (e) => setTested({ what, ok: false, text: (e as Error).message }),
    });

  const whereOptions = (
    <>
      <option value="">Niet gekoppeld</option>
      <optgroup label="Cluster (de node met het VIP)">
        {ov.clusters.map((c) => (
          <option key={c.id} value={`c:${c.id}`}>
            {c.name}
            {c.vips?.length ? ` (${c.vips.join(", ")})` : ""}
          </option>
        ))}
      </optgroup>
      <optgroup label="Eén node">
        {ov.nodes.map((n) => (
          <option key={n.id} value={`n:${n.id}`}>
            {n.hostname}
            {n.cluster_name ? ` (${n.cluster_name})` : ""}
          </option>
        ))}
      </optgroup>
    </>
  );

  const box = (key: keyof PublishDefaults, label: string) => (
    <label className="flex items-center gap-2">
      <input type="checkbox" disabled={!isAdmin} checked={!!d[key]} onChange={(e) => setD({ ...d, [key]: e.target.checked })} />
      {label}
    </label>
  );

  return (
    <Card title="Domeinen en publicatie">
      <div className="space-y-5 text-sm">
        <p className="text-slate-600 dark:text-slate-300">
          Nieuwe diensten krijgen een naam onder het hoofddomein, zoals homepage.{domain || "jbogaert.be"}. ClusterForge praat met NPM en Authentik
          via de agent op hun node, nooit rechtstreeks.
        </p>
        <Field
          label="Hoofddomein"
          htmlFor="pub-domain"
          hint="Veranderen hernoemt nog niets; daarna kun je de beheerde hosts met een voorbeeld omzetten."
        >
          <Input id="pub-domain" value={domain} disabled={!isAdmin} onChange={(e) => setDomain(e.target.value)} />
        </Field>

        <fieldset className="space-y-2">
          <legend className="font-medium">Standaard voor een nieuwe proxy host</legend>
          <div className="grid gap-2 sm:grid-cols-2">
            {box("ssl_forced", "SSL afdwingen")}
            {box("http2", "HTTP/2")}
            {box("hsts", "HSTS")}
            {box("websockets", "Websockets")}
            {box("block_exploits", "Exploits blokkeren")}
            {box("letsencrypt", "Let's Encrypt als er geen passend certificaat is")}
            {box("authentik", "Achter Authentik")}
            {box("authentik_auto_repair", "Half ingestelde Authentik vanzelf aanvullen")}
          </div>
          <Field
            label="Niet achter Authentik (eigen login)"
            htmlFor="pub-skip"
            hint="Woorden in de naam, het image of de container, gescheiden door komma's."
          >
            <Input id="pub-skip" value={skip} disabled={!isAdmin} onChange={(e) => setSkip(e.target.value)} />
          </Field>
          {d.letsencrypt && (
            <Field label="E-mailadres voor Let's Encrypt" htmlFor="pub-le" hint="Alleen nodig voor oudere NPM-versies.">
              <Input
                id="pub-le"
                value={d.letsencrypt_email ?? ""}
                disabled={!isAdmin}
                onChange={(e) => setD({ ...d, letsencrypt_email: e.target.value })}
              />
            </Field>
          )}
        </fieldset>

        <fieldset className="space-y-2">
          <legend className="font-medium">Nginx Proxy Manager</legend>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Waar draait NPM" htmlFor="pub-npm-where">
              <Select id="pub-npm-where" value={npmWhere} disabled={!isAdmin} onChange={(e) => setNpmWhere(e.target.value)}>
                {whereOptions}
              </Select>
            </Field>
            <Field label="Poort van de beheer-API" htmlFor="pub-npm-port">
              <Input id="pub-npm-port" inputMode="numeric" value={npmPort} disabled={!isAdmin} onChange={(e) => setNpmPort(e.target.value)} />
            </Field>
            <Field
              label="NPM-gebruiker (e-mail)"
              htmlFor="pub-npm-user"
              hint="Liefst een eigen beheerder voor ClusterForge, zonder tweestapsverificatie."
            >
              <Input id="pub-npm-user" value={npmUser} disabled={!isAdmin} autoComplete="off" onChange={(e) => setNpmUser(e.target.value)} />
            </Field>
            <Field label="Wachtwoord" htmlFor="pub-npm-secret" hint={st.npm_has_secret ? "Bewaard; leeg laten om het te houden." : undefined}>
              <Input
                id="pub-npm-secret"
                type="password"
                value={npmSecret}
                disabled={!isAdmin}
                autoComplete="new-password"
                onChange={(e) => setNpmSecret(e.target.value)}
              />
            </Field>
          </div>
          {isAdmin && (
            <div className="flex flex-wrap gap-2">
              <Button variant="secondary" disabled={test.isPending || !ov.npm.linked} onClick={() => runTest("npm")}>
                NPM testen
              </Button>
              {st.npm_has_secret && (
                <Button variant="secondary-danger" disabled={save.isPending} onClick={() => void submit({ clear_npm: true })}>
                  NPM loskoppelen
                </Button>
              )}
            </div>
          )}
        </fieldset>

        <fieldset className="space-y-2">
          <legend className="font-medium">Authentik</legend>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Waar draait Authentik" htmlFor="pub-ak-where">
              <Select id="pub-ak-where" value={akWhere} disabled={!isAdmin} onChange={(e) => setAkWhere(e.target.value)}>
                {whereOptions}
              </Select>
            </Field>
            <Field label="Poort (http)" htmlFor="pub-ak-port">
              <Input id="pub-ak-port" inputMode="numeric" value={akPort} disabled={!isAdmin} onChange={(e) => setAkPort(e.target.value)} />
            </Field>
            <Field
              label="API-token"
              htmlFor="pub-ak-token"
              hint={st.authentik_has_token ? "Bewaard; leeg laten om het te houden." : "Admin-interface → Directory → Tokens, met intentie API."}
            >
              <Input
                id="pub-ak-token"
                type="password"
                value={akToken}
                disabled={!isAdmin}
                autoComplete="new-password"
                onChange={(e) => setAkToken(e.target.value)}
              />
            </Field>
            <Field label="Adres van de outpost voor NPM" htmlFor="pub-ak-outpost" hint="Leeg: het VIP of IP van Authentik met de poort hierboven.">
              <Input
                id="pub-ak-outpost"
                value={outpost}
                placeholder="http://192.168.x.x:9000"
                disabled={!isAdmin}
                onChange={(e) => setOutpost(e.target.value)}
              />
            </Field>
          </div>
          {isAdmin && (
            <div className="flex flex-wrap gap-2">
              <Button variant="secondary" disabled={test.isPending || !ov.authentik.linked} onClick={() => runTest("authentik")}>
                Authentik testen
              </Button>
              {st.authentik_has_token && (
                <Button variant="secondary-danger" disabled={save.isPending} onClick={() => void submit({ clear_authentik: true })}>
                  Authentik loskoppelen
                </Button>
              )}
            </div>
          )}
        </fieldset>

        {tested && (
          <Alert kind={tested.ok ? "success" : "error"}>
            {tested.what === "npm" ? "NPM" : "Authentik"}: {tested.text}
          </Alert>
        )}
        {save.isError && <Alert>{(save.error as Error).message}</Alert>}
        {save.isSuccess && !renameFrom && <Alert kind="success">Bewaard.</Alert>}
        {renameFrom && (
          <Alert kind="info">
            <div className="space-y-2">
              <p>
                Het hoofddomein is nu {st.domain}. Er zijn {managedUnder(renameFrom)} beheerde proxy hosts onder {renameFrom}. Wil je die omzetten?
              </p>
              <label className="flex items-center gap-2">
                <input type="checkbox" checked={keepOld} onChange={(e) => setKeepOld(e.target.checked)} />
                De oude naam blijft als extra naam werken
              </label>
              <Button variant="secondary" onClick={() => setRenaming(true)}>
                Voorbeeld en omzetten
              </Button>
            </div>
          </Alert>
        )}
        {isAdmin && (
          <div className="flex justify-end">
            <Button disabled={save.isPending} onClick={() => void submit()}>
              {save.isPending ? "Bewaren…" : "Bewaren"}
            </Button>
          </div>
        )}
      </div>
      {renaming && renameFrom && (
        <ActionModal
          title={`Hosts omzetten naar ${st.domain}`}
          action={{
            action: "rename",
            old_domain: renameFrom,
            new_domain: st.domain,
            keep_old: keepOld,
          }}
          onClose={() => {
            setRenaming(false);
            setRenameFrom(null);
          }}
        />
      )}
    </Card>
  );
}
