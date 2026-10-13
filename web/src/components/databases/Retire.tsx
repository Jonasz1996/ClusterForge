"use client";

import Link from "next/link";
import { useState } from "react";
import { CopyBlock } from "@/components/inventory/InstallAgent";
import { ago, formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { Alert, Badge, Button, Card, Input } from "@/components/ui";
import {
  engineLabel,
  useRetire,
  useRetireMove,
  type DatabaseGroup,
  type MigrationRun,
  type RetireMove,
  type RetireRule,
  type RetireSource,
} from "@/lib/databases";
import { useIsAdmin } from "@/lib/inventory";

// De laatste dump staat op de node in deze map, per database.
const dumpDir = "/var/backups/clusterforge-db";

const when = new Intl.DateTimeFormat("nl-BE", { day: "numeric", month: "numeric", hour: "2-digit", minute: "2-digit" });

function at(iso: string) {
  return when.format(new Date(iso));
}

function engineText(e: string) {
  return engineLabel[e as DatabaseGroup["engine"]] ?? e;
}

function names(list: string[]) {
  return list.length <= 1 ? list.join("") : `${list.slice(0, -1).join(", ")} en ${list[list.length - 1]}`;
}

function upper(s: string) {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

function verhuizenHref(source: string, db: string) {
  return `/databases/verhuizen?source=${encodeURIComponent(source)}&db=${encodeURIComponent(db)}`;
}

// lastUseCounts zegt of de laatste verbinding van buiten na het
// overschakelen kwam: dan telt de week vanaf die verbinding.
function lastUseCounts(mv: RetireMove) {
  const base = mv.run.switched_at ?? mv.run.started_at;
  return mv.last_used !== null && new Date(mv.last_used) > new Date(base);
}

// RetireSection is het afbouwen van de oude database na een verhuizing: de
// week wachten, de laatste dump, en de database en haar gebruikers weghalen.
export function RetireSection({ r }: { r: MigrationRun }) {
  const q = useRetire();
  const mv = q.data?.flatMap((s) => s.moves).find((m) => m.run.id === r.id);
  return (
    <section className="space-y-2 rounded border border-slate-200 p-3 text-sm text-slate-700 dark:border-slate-800 dark:text-slate-300">
      <h3 className="flex flex-wrap items-center gap-2 font-semibold text-slate-900 dark:text-slate-100">
        Oude database afbouwen
        {r.retired_at ? (
          <Badge tone="green">afgebouwd op {at(r.retired_at)}</Badge>
        ) : mv?.active ? (
          <Badge tone="blue">loopt</Badge>
        ) : mv?.ready ? (
          <Badge tone="amber">kan nu</Badge>
        ) : mv?.ready_at ? (
          <Badge>vanaf {at(mv.ready_at)}</Badge>
        ) : null}
      </h3>
      {r.retired_at ? (
        <Retired r={r} />
      ) : mv?.active ? (
        <Alert kind="info">
          Het afbouwen loopt: {mv.active.title}.{" "}
          <Link href={`/taken/detail?id=${mv.active.job_id}`} className="underline">
            Volg de taak
          </Link>
          .
        </Alert>
      ) : q.isError ? (
        <Alert>{q.error.message}</Alert>
      ) : !mv ? (
        <p className="text-slate-500">{q.isPending ? "Laden…" : "ClusterForge vindt de oude server niet meer."}</p>
      ) : (
        <>
          {r.retire_message && (
            <Alert>
              <p className="font-medium">Het vorige afbouwen lukte niet.</p>
              <p className="whitespace-pre-line">{r.retire_message}</p>
            </Alert>
          )}
          {mv.ready ? <RetireForm r={r} mv={mv} /> : <Waiting r={r} mv={mv} />}
          <LocalUse mv={mv} />
        </>
      )}
    </section>
  );
}

function Waiting({ r, mv }: { r: MigrationRun; mv: RetireMove }) {
  return (
    <p>
      Afbouwen kan vanaf <strong>{mv.ready_at ? at(mv.ready_at) : "–"}</strong>:{" "}
      {mv.last_used && lastUseCounts(mv)
        ? `zeven dagen nadat ${mv.last_by} op ${at(mv.last_used)} nog met ${r.database} op ${r.source_name} verbonden was. Wie verbindt er nog met de oude database?`
        : "zeven dagen na het overschakelen, als er zolang niemand meer met de oude database verbindt."}{" "}
      Tot dan blijft ze op slot staan, zodat je terug kunt.
    </p>
  );
}

// LocalUse: een verbinding op de databasenode zelf, zoals een back-up, telt
// niet voor de week, maar hij mislukt zodra de database weg is.
function LocalUse({ mv }: { mv: RetireMove }) {
  if (!mv.last_local || !mv.exists) return null;
  return (
    <p className="text-slate-500">
      {upper(mv.local_by)} verbond {ago(mv.last_local)} nog met de oude database, bijvoorbeeld een back-up. Dat telt niet voor de
      zeven dagen. Zet die back-up wel stop: na het afbouwen mislukt hij, en tijdens het afbouwen mag hij niet verbonden zijn.
    </p>
  );
}

function RetireForm({ r, mv }: { r: MigrationRun; mv: RetireMove }) {
  const isAdmin = useIsAdmin();
  const totp = useProdTotp();
  const retire = useRetireMove();
  const [typed, setTyped] = useState("");
  const [confirm, setConfirm] = useState("");
  const slug = mv.prod_slug;
  const ready = isAdmin && typed === r.database && !retire.isPending && (slug === "" || prodConfirmed(slug, confirm, totp));
  return (
    <div className="space-y-3">
      {mv.exists ? (
        <p>
          {mv.last_used && lastUseCounts(mv)
            ? `Sinds ${at(mv.last_used)} verbond niemand van buiten ${r.source_name} nog met ${r.database}.`
            : `Sinds het overschakelen verbond niemand van buiten ${r.source_name} nog met ${r.database}.`}{" "}
          Afbouwen maakt eerst een laatste dump op een node van {r.source_name}; die blijft daar staan tot je hem weghaalt. Daarna
          gaan {r.database} en de gebruikers die niets anders hebben weg. Wie ook rechten op een andere database heeft of er een
          gebruikte, blijft staan. Terug naar de oude database kan daarna alleen nog met die dump.
        </p>
      ) : (
        <p>
          {r.database} staat al niet meer op {r.source_name}. Afbouwen haalt alleen nog de gebruikers weg die niets anders hebben.
        </p>
      )}
      <p>Tik de naam van de database om te bevestigen.</p>
      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label="Naam van de database"
          className="max-w-56 font-mono"
          autoComplete="off"
          spellCheck={false}
          value={typed}
          onChange={(e) => setTyped(e.target.value.trim())}
          disabled={!isAdmin}
        />
      </div>
      {slug !== "" && isAdmin && <ProdConfirm slug={slug} value={confirm} onChange={setConfirm} />}
      {retire.isError && <Alert>{retire.error.message}</Alert>}
      <div className="flex flex-wrap items-center gap-3">
        <Button
          variant="danger"
          disabled={!ready}
          onClick={() => retire.mutate({ id: r.id, name: typed, confirm: slug !== "" ? confirm : undefined })}
        >
          {retire.isPending ? "Bezig…" : "Oude database afbouwen"}
        </Button>
        {!isAdmin && <span className="text-sm text-slate-500">Alleen een beheerder kan afbouwen.</span>}
      </div>
    </div>
  );
}

function Retired({ r }: { r: MigrationRun }) {
  const file = r.retire_file ? `${dumpDir}/${r.database}/${r.retire_file}` : "";
  return (
    <>
      <p>
        {r.database} en de gebruikers die niets anders hadden, zijn weg van {r.source_name}. Terug naar de oude database kan
        alleen nog met de laatste dump.
      </p>
      {file && r.retire_node && (
        <>
          <p>
            De laatste dump staat op {r.retire_node}: <span className="font-mono text-xs">{file}</span> (
            {formatBytes(r.retire_bytes)}). Hij blijft daar staan tot je hem weghaalt, en is weg als je de node weghaalt. Bewaar
            hem ergens anders, bijvoorbeeld zo:
          </p>
          <CopyBlock text={`scp root@${r.retire_node}:${file} .`} />
        </>
      )}
      {r.retire_message && (
        <details className="rounded border border-slate-200 px-3 py-2 dark:border-slate-800">
          <summary className="cursor-pointer font-medium">Wat het afbouwen deed</summary>
          <p className="mt-2 whitespace-pre-line break-words">{r.retire_message}</p>
        </details>
      )}
    </>
  );
}

// RetireCards toont per oude databaseserver wat er nog op staat, wat er op
// afbouwen wacht en welke firewallregel weg kan.
export function RetireCards() {
  const q = useRetire();
  return (
    <>
      {(q.data ?? []).map((s) => (
        <RetireCard key={s.keys.join(" ")} s={s} />
      ))}
    </>
  );
}

function RetireCard({ s }: { s: RetireSource }) {
  const left = s.remaining.filter((x) => !x.moved);
  const title = (
    <span className="flex flex-wrap items-center gap-2">
      <span>
        Oude databaseserver{" "}
        {s.cluster_id ? (
          <Link href={`/clusters/detail?id=${s.cluster_id}`} className="text-brand-700 hover:underline dark:text-sky-300">
            {s.name}
          </Link>
        ) : (
          s.name
        )}
      </span>
      {s.empty ? (
        <Badge tone="green">leeg</Badge>
      ) : !s.readable ? (
        <Badge>niet gelezen</Badge>
      ) : (
        <Badge tone="amber">
          nog {s.remaining.length} {s.remaining.length === 1 ? "database" : "databases"}
        </Badge>
      )}
    </span>
  );
  return (
    <Card title={title}>
      <div className="space-y-4 text-sm text-slate-700 dark:text-slate-300">
        {s.empty ? (
          <Alert kind="success">
            Op {names(s.servers)} staat geen database van een app meer. De CT&apos;s of VM&apos;s weghalen doe je in Proxmox;
            ClusterForge verwijdert geen nodes.
          </Alert>
        ) : (
          <p>
            Van {names(s.servers)} zijn databases naar de nodes van een app verhuisd. Een verhuisde database blijft hier op slot
            staan, zodat je terug kunt. Zeven dagen nadat niemand er nog mee verbond, kun je ze afbouwen.
            {!s.readable && " Niet elke server gaf een lijst met databases, dus of ze leeg zijn, weet ClusterForge nog niet."}
          </p>
        )}
        <MovesTable moves={s.moves} />
        {left.length > 0 && (
          <p>
            Nog niet verhuisd:{" "}
            {left.map((x, i) => (
              <span key={`${x.source_key} ${x.database}`}>
                {i > 0 && ", "}
                <Link
                  href={verhuizenHref(x.source_key, x.database)}
                  className="font-mono text-xs text-brand-700 hover:underline dark:text-sky-300"
                >
                  {x.database}
                </Link>{" "}
                <span className="text-xs text-slate-500">({engineText(x.engine)})</span>
              </span>
            ))}
            .
          </p>
        )}
        {s.rules.length > 0 && <RulesTable rules={s.rules} name={s.name} />}
      </div>
    </Card>
  );
}

function MovesTable({ moves }: { moves: RetireMove[] }) {
  return (
    // relative: anders maakt de onzichtbare kop de pagina op een telefoon breder dan het scherm.
    <div className="relative overflow-x-auto">
      <table className={tableClass}>
        <thead className="border-b border-slate-200 dark:border-slate-800">
          <tr>
            <th className={thClass}>Verhuisd</th>
            <th className={thClass}>Naar</th>
            <th className={thClass}>Oude database</th>
            <th className={thClass}>
              <span className="sr-only">Afbouwen</span>
            </th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {moves.map((mv) => (
            <tr key={mv.run.id}>
              <td className={`${tdClass} font-mono text-xs`}>
                {mv.run.database}
                <div className="font-sans text-slate-500">{engineText(mv.engine)}</div>
              </td>
              <td className={tdClass}>
                <Link
                  href={`/clusters/detail?id=${mv.run.cluster_id}`}
                  className="text-brand-700 hover:underline dark:text-sky-300"
                >
                  {mv.run.cluster_name}
                </Link>
                {mv.run.switched_at && <div className="text-xs text-slate-500">op {at(mv.run.switched_at)}</div>}
              </td>
              <td className={`${tdClass} min-w-64`}>
                <MoveState mv={mv} />
              </td>
              <td className={`${tdClass} whitespace-nowrap text-right`}>
                <Link
                  href={verhuizenHref(mv.source_key, mv.run.database)}
                  className="text-brand-700 hover:underline dark:text-sky-300"
                >
                  {mv.run.retired_at ? "Bekijken" : mv.ready && !mv.active ? "Afbouwen…" : "Openen"}
                </Link>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function MoveState({ mv }: { mv: RetireMove }) {
  const r = mv.run;
  if (r.retired_at) {
    return (
      <span>
        <Badge tone="green">afgebouwd</Badge>{" "}
        <span className="text-slate-500">
          op {at(r.retired_at)}
          {r.retire_node ? `, laatste dump op ${r.retire_node}` : ""}
        </span>
      </span>
    );
  }
  if (mv.active) return <Badge tone="blue">afbouwen loopt</Badge>;
  return (
    <span className="space-y-1">
      <span className="flex flex-wrap items-center gap-2">
        {mv.ready ? (
          <Badge tone="amber">kan afgebouwd worden</Badge>
        ) : (
          <>
            <Badge>op slot</Badge>
            {mv.ready_at && <span className="text-slate-500">afbouwen kan vanaf {at(mv.ready_at)}</span>}
          </>
        )}
        {r.retire_message && (
          <span title={r.retire_message}>
            <Badge tone="red">vorige keer mislukt</Badge>
          </span>
        )}
      </span>
      {mv.last_used && lastUseCounts(mv) && (
        <span className="block text-xs text-slate-500">
          laatst gebruikt door {mv.last_by}, {ago(mv.last_used)}
        </span>
      )}
      {!mv.exists && <span className="block text-xs text-slate-500">staat er al niet meer</span>}
    </span>
  );
}

const ruleInfo: Record<RetireRule["state"], { label: string; tone: "green" | "amber" | "slate" }> = {
  unused: { label: "kan weg", tone: "green" },
  until_retired: { label: "nodig tot het afbouwen", tone: "slate" },
  in_use: { label: "nog in gebruik", tone: "amber" },
};

// RulesTable: de firewallregels van de apps naar de oude servers, zoals
// ClusterForge ze uit de verbindingen afleidt. De firewall zelf beheert het
// niet.
function RulesTable({ rules, name }: { rules: RetireRule[]; name: string }) {
  return (
    <div className="space-y-2">
      <h3 className="font-semibold text-slate-900 dark:text-slate-100">Firewallregels naar {name}</h3>
      <p className="text-slate-500">
        ClusterForge ziet de verbindingen, niet je firewall. Een regel die weg kan, haal je op de firewall zelf weg.
      </p>
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead className="border-b border-slate-200 dark:border-slate-800">
            <tr>
              <th className={thClass}>Van</th>
              <th className={thClass}>Naar</th>
              <th className={thClass}>Regel</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {rules.map((r) => (
              <tr key={`${r.cluster_id} ${r.engine}`}>
                <td className={tdClass}>
                  <Link href={`/clusters/detail?id=${r.cluster_id}`} className="text-brand-700 hover:underline dark:text-sky-300">
                    {r.cluster_name}
                  </Link>
                </td>
                <td className={`${tdClass} font-mono text-xs`}>
                  {r.address ? `${r.address}:${r.port}` : `poort ${r.port}`}
                  <div className="font-sans text-slate-500">{engineText(r.engine)}</div>
                </td>
                <td className={`${tdClass} min-w-64`}>
                  <Badge tone={ruleInfo[r.state].tone}>{ruleInfo[r.state].label}</Badge>{" "}
                  <span className="text-slate-500">
                    {r.state === "in_use"
                      ? `${r.cluster_name} verbond na het overschakelen nog, ${ago(r.last_seen)}. Wijst er nog iets naar het oude adres?`
                      : r.state === "until_retired"
                        ? "om terug te kunnen"
                        : r.last_seen
                          ? `laatste verbinding ${ago(r.last_seen)}`
                          : ""}
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
