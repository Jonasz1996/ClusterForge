"use client";

import Link from "next/link";
import { useState } from "react";
import { Alert, Badge, Button, Card, Field, Input, Select } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { useCheckImage, useMetricsImport, useSelfInfo, useSelfMove, useSelfUpdate, type SelfJob, type SelfTarget } from "@/lib/selfhost";

const statusTone = { succeeded: "green", failed: "red", canceled: "slate", running: "blue", queued: "blue" } as const;
const statusLabel: Record<string, string> = {
  succeeded: "gelukt",
  failed: "mislukt",
  canceled: "geannuleerd",
  running: "loopt",
  queued: "wacht",
};

function JobLine({ j }: { j: SelfJob }) {
  return (
    <li className="flex flex-wrap items-baseline gap-2">
      <Badge tone={statusTone[j.status as keyof typeof statusTone] ?? "slate"}>{statusLabel[j.status] ?? j.status}</Badge>
      <Link href={`/taken/detail?id=${j.id}`} className="underline">
        {j.title}
      </Link>
      <span className="text-xs text-slate-500">{new Date(j.created_at).toLocaleString("nl-BE")}</span>
      {j.error && <span className="w-full text-xs text-red-700 dark:text-red-300">{j.error}</span>}
    </li>
  );
}

// Confirm vraagt de code van tweestapsverificatie, of zonder het wachtwoord.
function Confirm({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const me = useMe();
  const totp = me.data?.user.totp_enabled ?? false;
  return (
    <Field label={totp ? "Code van je tweestapsverificatie" : "Je wachtwoord"} htmlFor="self-confirm">
      <Input
        id="self-confirm"
        type={totp ? "text" : "password"}
        inputMode={totp ? "numeric" : undefined}
        autoComplete={totp ? "one-time-code" : "current-password"}
        className="max-w-xs font-mono"
        value={value}
        onChange={(e) => onChange(e.target.value.trim())}
      />
    </Field>
  );
}

function useConfirmBody() {
  const me = useMe();
  const totp = me.data?.user.totp_enabled ?? false;
  return (v: string) => (totp ? { totp_code: v } : { password: v });
}

// SelfHostCard: ClusterForge zelf bijwerken (tag kiezen, back-up, herstart,
// gezondheidscontrole, terugdraaien) en verhuizen naar een eigen cluster.
export function SelfHostCard({ isAdmin }: { isAdmin: boolean }) {
  const q = useSelfInfo();
  const d = q.data;
  return (
    <Card title="ClusterForge zelf">
      <div className="space-y-5 text-sm">
        {q.isLoading && <p className="text-slate-500">Laden…</p>}
        {q.isError && !d && <Alert>De server antwoordt niet; herstart hij net (bijwerken)? Deze pagina probeert het vanzelf opnieuw.</Alert>}
        {d && (
          <>
            <dl className="grid gap-x-4 gap-y-1 sm:grid-cols-[10rem_1fr]">
              <dt className="text-slate-500">Versie</dt>
              <dd className="font-mono">{d.version}</dd>
              <dt className="text-slate-500">Image</dt>
              <dd className="break-all font-mono">{d.image || "zelf gebouwd (onbekend)"}</dd>
              <dt className="text-slate-500">Draait als</dt>
              <dd>
                {d.mode === "cluster" && d.cluster ? (
                  <>
                    cluster{" "}
                    <Link href={`/clusters/detail?id=${d.cluster.id}`} className="underline">
                      {d.cluster.name}
                    </Link>{" "}
                    ({d.cluster.nodes.length} nodes{d.cluster.vip ? `, VIP ${d.cluster.vip}` : ""}); de server draait op de node met het VIP
                  </>
                ) : (
                  "één server met docker compose"
                )}
              </dd>
            </dl>
            {d.mode === "cluster" && <MetricsImport />}
            <UpdateSection info={d} isAdmin={isAdmin} />
            {d.mode === "compose" && <MoveSection info={d} isAdmin={isAdmin} />}
            {d.jobs.length > 0 && (
              <div className="space-y-2 border-t border-slate-200 pt-4 dark:border-slate-800">
                <p className="font-medium">Laatste taken</p>
                <ul className="space-y-1">
                  {d.jobs.map((j) => (
                    <JobLine key={j.id} j={j} />
                  ))}
                </ul>
              </div>
            )}
          </>
        )}
      </div>
    </Card>
  );
}

function UpdateSection({ info, isAdmin }: { info: NonNullable<ReturnType<typeof useSelfInfo>["data"]>; isAdmin: boolean }) {
  const check = useCheckImage();
  const upd = useSelfUpdate();
  const body = useConfirmBody();
  const [tag, setTag] = useState("");
  const [host, setHost] = useState("");
  const [code, setCode] = useState("");
  const running = info.jobs.some((j) => (j.status === "running" || j.status === "queued") && j.kind === "clusterforge.update");
  const chosenHost = host || info.host_node_id || "";
  const checked = check.data && check.variables === tag ? check.data : undefined;
  const needsHost = info.mode === "compose";
  const can = isAdmin && tag.trim() !== "" && code !== "" && (!needsHost || chosenHost !== "") && !running && checked?.exists === true;

  return (
    <div className="space-y-3 border-t border-slate-200 pt-4 dark:border-slate-800">
      <p className="font-medium">Bijwerken</p>
      <p className="text-slate-600 dark:text-slate-400">
        Kies een tag van {info.repo} (zoals de korte commit die je krijgt). Eerst een back-up van de database, dan het nieuwe image, een herstart en
        een gezondheidscontrole. Wordt de nieuwe versie niet gezond, dan gaat alles terug, met de database uit de back-up als er migraties liepen.
        {info.mode === "cluster"
          ? " In het cluster halen eerst de andere nodes het image; daarna werkt de VIP-node bij en krijgen de andere nodes de nieuwe versie."
          : " De agent op de host doet het werk; deze pagina is even weg tijdens de herstart."}
      </p>
      {running && <Alert kind="info">Bijwerken loopt; deze pagina werkt zich vanzelf bij.</Alert>}
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Tag of image" htmlFor="self-tag" hint={`Nu: ${info.image || info.version}`}>
          <div className="flex gap-2">
            <Input
              id="self-tag"
              className="font-mono"
              placeholder="b9116ae"
              value={tag}
              disabled={!isAdmin}
              onChange={(e) => setTag(e.target.value.trim())}
            />
            <Button variant="secondary" disabled={!tag || check.isPending || !isAdmin} onClick={() => check.mutate(tag)}>
              Controleren
            </Button>
          </div>
        </Field>
        {needsHost && (
          <Field label="Host van ClusterForge" htmlFor="self-host" hint="De node met de agent op de LXC of VM waar docker compose draait.">
            <Select id="self-host" value={chosenHost} disabled={!isAdmin} onChange={(e) => setHost(e.target.value)}>
              <option value="">Kies…</option>
              {info.hosts.map((n) => (
                <option key={n.id} value={n.id}>
                  {n.hostname}
                  {n.address ? ` (${n.address})` : ""}
                </option>
              ))}
            </Select>
          </Field>
        )}
      </div>
      {needsHost && info.hosts.length === 0 && (
        <Alert kind="info">
          Geen agent op de host van ClusterForge gevonden. Installeer er één keer de agent (Nodes → Agent installeren, op de LXC of VM zelf), dan werkt
          bijwerken vanaf hier.
        </Alert>
      )}
      {checked && checked.exists && <Alert kind="success">{checked.image} staat klaar in de registry.</Alert>}
      {checked && !checked.exists && <Alert>{checked.error ? checked.error : `${checked.image} bestaat niet (nog niet gebouwd door CI?).`}</Alert>}
      {check.isError && <Alert>{check.error.message}</Alert>}
      {isAdmin && checked?.exists && (
        <div className="space-y-3">
          <Confirm value={code} onChange={setCode} />
          <Button
            disabled={!can || upd.isPending}
            onClick={() =>
              upd.mutate(
                { image: checked.image, host_node_id: needsHost ? chosenHost : undefined, ...body(code) },
                {
                  onSuccess: () => {
                    setCode("");
                    setTag("");
                    check.reset();
                  },
                },
              )
            }
          >
            Bijwerken naar {checked.image.split(":").pop()}
          </Button>
        </div>
      )}
      {upd.isError && <Alert>{upd.error.message}</Alert>}
    </div>
  );
}

function MoveSection({ info, isAdmin }: { info: NonNullable<ReturnType<typeof useSelfInfo>["data"]>; isAdmin: boolean }) {
  const move = useSelfMove();
  const body = useConfirmBody();
  const [target, setTarget] = useState("");
  const [host, setHost] = useState("");
  const [code, setCode] = useState("");
  const [force, setForce] = useState(false);
  const [metrics, setMetrics] = useState(true);
  const t: SelfTarget | undefined = info.targets.find((x) => x.id === (target || info.targets[0]?.id));
  const chosenHost = host || info.host_node_id || "";
  const running = info.jobs.some((j) => (j.status === "running" || j.status === "queued") && j.kind === "clusterforge.move");
  const blockers: string[] = [];
  if (!info.master_key) blockers.push("deze server heeft geen hoofdsleutel (CF_MASTER_KEY)");
  if (!info.export) blockers.push("de server heeft nog geen exportmap: werk eerst bij naar deze versie (hierboven), dat zet het nieuwe compose-bestand");
  if (!info.image) blockers.push("het image van deze server is onbekend; werk eerst bij naar een image van CI");
  const can = isAdmin && !!t && chosenHost !== "" && code !== "" && blockers.length === 0 && !running && (info.stragglers.length === 0 || force);

  return (
    <div className="space-y-3 border-t border-slate-200 pt-4 dark:border-slate-800">
      <p className="font-medium">Verhuizen naar een eigen cluster</p>
      <ol className="list-decimal space-y-1 pl-5 text-slate-600 dark:text-slate-400">
        <li>
          Rol drie CT&apos;s uit met de template{" "}
          <Link href="/templates" className="underline">
            ClusterForge zelf (HA)
          </Link>
          : een vrij VIP en een vrij VIP voor de database (niet .1 tot .3). PostgreSQL komt er vanzelf bij.
        </li>
        <li>Kies hieronder dat cluster en de host waarop ClusterForge nu draait, en start de verhuizing.</li>
        <li>
          ClusterForge maakt een dump, zet die met de hoofdsleutel en de instellingen op het cluster, start de server op het VIP, zet alle agents om
          naar het VIP en stopt de oude server. Daarna log je in op http://VIP:8080; zet je reverse proxy (NPM) naar dat adres.
        </li>
      </ol>
      {info.targets.length === 0 ? (
        <Alert kind="info">Er is nog geen cluster met de template ClusterForge zelf.</Alert>
      ) : (
        <>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Cluster" htmlFor="self-target">
              <Select id="self-target" value={t?.id ?? ""} disabled={!isAdmin} onChange={(e) => setTarget(e.target.value)}>
                {info.targets.map((x) => (
                  <option key={x.id} value={x.id}>
                    {x.name}
                    {x.vip ? ` (VIP ${x.vip})` : ""}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Host van ClusterForge nu" htmlFor="self-move-host">
              <Select id="self-move-host" value={chosenHost} disabled={!isAdmin} onChange={(e) => setHost(e.target.value)}>
                <option value="">Kies…</option>
                {info.hosts.map((n) => (
                  <option key={n.id} value={n.id}>
                    {n.hostname}
                    {n.address ? ` (${n.address})` : ""}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
          {t && (
            <p className="text-slate-600 dark:text-slate-400">
              Nodes: {t.nodes.map((n) => `${n.hostname}${n.online ? "" : " (niet verbonden)"}`).join(", ") || "nog geen"}.
            </p>
          )}
        </>
      )}
      {blockers.map((b) => (
        <Alert key={b}>{b}</Alert>
      ))}
      {info.stragglers.length > 0 && (
        <Alert>
          Deze agents zijn te oud om om te zetten (protocol &lt; {info.protocol}): {info.stragglers.map((n) => n.hostname).join(", ")}. Werk ze eerst
          bij bij Nodes → Agents bijwerken, of kies hieronder toch verhuizen (ze blijven dan naar de oude server wijzen).
        </Alert>
      )}
      {info.offline.length > 0 && (
        <Alert kind="info">
          Niet verbonden, dus niet om te zetten: {info.offline.map((n) => n.hostname).join(", ")}. Die installeer je later opnieuw met het commando van
          het cluster.
        </Alert>
      )}
      {isAdmin && info.targets.length > 0 && (
        <div className="space-y-3">
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={metrics} onChange={(e) => setMetrics(e.target.checked)} />
            Metrics (grafieken) meenemen
          </label>
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} />
            Toch verhuizen als niet elke agent mee kan
          </label>
          <Confirm value={code} onChange={setCode} />
          <Button
            disabled={!can || move.isPending}
            onClick={() =>
              t &&
              move.mutate(
                { cluster_id: t.id, host_node_id: chosenHost, force, metrics, ...body(code) },
                { onSuccess: () => setCode("") },
              )
            }
          >
            Verhuizen naar {t?.name ?? "het cluster"}
          </Button>
        </div>
      )}
      {running && <Alert kind="info">De verhuizing loopt; volg ze bij de taak hieronder. Op het einde stopt deze server en ga je naar het VIP.</Alert>}
      {move.isError && <Alert>{move.error.message}</Alert>}
    </div>
  );
}

const mins = (a?: string, b?: string) => {
  if (!a) return "";
  const s = Math.max(0, ((b ? new Date(b).getTime() : Date.now()) - new Date(a).getTime()) / 1000);
  return s < 90 ? `${Math.round(s)} s` : `${Math.round(s / 60)} min`;
};

// MetricsImport toont na een verhuizing per node hoe het inladen van de
// metrics van de oude server staat (fase 48, 4.5). De server draait al; de
// grafieken van vóór de verhuizing verschijnen per node zodra die klaar is.
function MetricsImport() {
  const q = useMetricsImport(true);
  const nodes = q.data?.nodes ?? [];
  if (nodes.length === 0) return null;
  const busy = nodes.some((n) => n.state === "bezig");
  return (
    <div className="space-y-2 border-t border-slate-200 pt-4 dark:border-slate-800">
      <p className="font-medium">Metrics van de oude server</p>
      {busy && (
        <p className="text-slate-600 dark:text-slate-400">
          Ze worden op de achtergrond ingeladen, op alle nodes tegelijk. ClusterForge werkt al; de grafieken van vóór de verhuizing
          verschijnen zodra een node klaar is.
        </p>
      )}
      <ul className="space-y-1">
        {nodes.map((n) => (
          <li key={n.address} className="flex flex-wrap items-baseline gap-2">
            <Badge tone={n.state === "klaar" ? "green" : n.state === "mislukt" ? "red" : "blue"}>{n.state}</Badge>
            <span>{n.hostname || n.address}</span>
            <span className="text-xs text-slate-500">
              {n.state === "bezig"
                ? `${mins(n.started_at)} bezig${n.bytes ? `, ${Math.round(n.bytes / 1048576)} MB` : ""}`
                : mins(n.started_at, n.ended_at)}
            </span>
            {n.reason && <span className="w-full text-xs text-red-700 dark:text-red-300">{n.reason}</span>}
          </li>
        ))}
      </ul>
    </div>
  );
}
