"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useState, type ReactNode } from "react";
import { Modal } from "@/components/Modal";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { DumpsCard } from "@/components/databases/Dumps";
import { CopyBlock } from "@/components/inventory/InstallAgent";
import { formatBytes, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, Field, Input, PageHeader } from "@/components/ui";
import {
  connectionText,
  useClusterComponents,
  useComponentDatabases,
  useCreateDatabase,
  useDropDatabase,
  useDropUser,
  useSetUserPassword,
  type ClusterComponent,
  type ComponentDatabase,
  type ComponentDatabases,
  type ComponentDatabaseUser,
} from "@/lib/components";
import { useCluster, useIsAdmin, type ClusterDetail } from "@/lib/inventory";

export default function ComponentDatabasesPage() {
  return (
    <Suspense>
      <Inner />
    </Suspense>
  );
}

// engines zijn de onderdelen met databases en gebruikers.
const engines = ["mariadb", "postgresql"];

function Inner() {
  const params = useSearchParams();
  const id = params.get("id") ?? "";
  const componentId = params.get("component") ?? "";
  const cluster = useCluster(id);
  const comps = useClusterComponents(id);
  const isAdmin = useIsAdmin();
  if (id === "") return <Alert>Geen cluster opgegeven.</Alert>;
  // Het gekozen onderdeel; zonder keuze MariaDB, dan PostgreSQL.
  const dbs = (comps.data ?? [])
    .filter((x) => engines.includes(x.engine))
    .sort((a, b) => engines.indexOf(a.engine) - engines.indexOf(b.engine));
  const comp = dbs.find((x) => x.id === componentId) ?? dbs[0];
  return (
    <QueryState q={cluster}>
      <QueryState q={comps}>
        {cluster.data && comps.data && (
          <div className="space-y-6">
            <PageHeader
              title="Databases en gebruikers"
              description={
                <>
                  In {comp ? comp.title : "de database"} van{" "}
                  <Link href={`/clusters/detail?id=${id}`} className="underline">
                    {cluster.data.name}
                  </Link>
                  .{" "}
                  {comp?.engine === "postgresql"
                    ? "ClusterForge schrijft op de primary; de replica's krijgen een nieuwe database of gebruiker via de replicatie."
                    : "Galera zet een nieuwe database of gebruiker zelf op alle nodes."}
                </>
              }
            />
            {dbs.length > 1 && comp && (
              <nav className="flex flex-wrap gap-2" aria-label="Database">
                {dbs.map((x) => (
                  <Link key={x.id} href={`/clusters/databases?id=${id}&component=${x.id}`}>
                    <Button
                      variant={x.id === comp.id ? "primary" : "secondary"}
                      aria-current={x.id === comp.id ? "page" : undefined}
                    >
                      {x.title}
                    </Button>
                  </Link>
                ))}
              </nav>
            )}
            {!comp ? (
              <Alert>Dit cluster heeft geen database van ClusterForge.</Alert>
            ) : comp.applied_revision === null ? (
              <Alert>Deze database is nog nooit helemaal uitgerold; pas haar eerst toe.</Alert>
            ) : (
              <div key={comp.id} className="space-y-6">
                <Lists c={cluster.data} comp={comp} isAdmin={isAdmin} />
                <DumpsCard clusterId={id} comp={comp} isAdmin={isAdmin} />
              </div>
            )}
          </div>
        )}
      </QueryState>
    </QueryState>
  );
}

type Dialog =
  | { kind: "create" }
  | { kind: "drop-db"; db: ComponentDatabase }
  | { kind: "password"; user: ComponentDatabaseUser }
  | { kind: "drop-user"; user: ComponentDatabaseUser };

// systemLabel zegt van wie een gebruiker is die ClusterForge niet beheert.
function systemLabel(d: ComponentDatabases, u: ComponentDatabaseUser) {
  if (d.engine !== "postgresql") return "van MariaDB";
  return u.name.startsWith("cf_herstel_") ? "tijdelijk, om terug te zetten" : "van PostgreSQL";
}

function Lists({ c, comp, isAdmin }: { c: ClusterDetail; comp: ClusterComponent; isAdmin: boolean }) {
  const q = useComponentDatabases(c.id, comp.id);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const d = q.data;
  const pg = d?.engine === "postgresql";
  return (
    <QueryState q={q}>
      {d && (
        <div className="space-y-6">
          <p className="text-sm text-slate-600 dark:text-slate-400">
            Een app verbindt met{" "}
            <code>
              {d.address}:{d.port}
            </code>
            . ClusterForge las dit op {d.node}
            {pg && ", de primary"}; de verbindingen zijn die op {d.node}.
          </p>
          <Card
            title={
              <span className="flex flex-wrap items-start justify-between gap-2">
                <span>Databases</span>
                {isAdmin && <Button onClick={() => setDialog({ kind: "create" })}>Database aanmaken</Button>}
              </span>
            }
          >
            {d.databases.length === 0 ? (
              <p className="text-sm text-slate-600 dark:text-slate-400">
                Er staat nog geen database in. Met Database aanmaken komt er een, met een eigen gebruiker en een sterk wachtwoord.
              </p>
            ) : (
              <div className="overflow-x-auto">
                <table className={tableClass}>
                  <thead>
                    <tr>
                      <th className={thClass}>Database</th>
                      <th className={thClass}>Grootte</th>
                      <th className={thClass}>Tabellen</th>
                      <th className={thClass}>Verbindingen</th>
                      <th className={thClass}>Gebruikers</th>
                      {isAdmin && <th className={thClass} />}
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                    {d.databases.map((db) => (
                      <tr key={db.name}>
                        <td className={tdClass + " font-medium"}>{db.name}</td>
                        <td className={tdClass}>{formatBytes(db.size_bytes)}</td>
                        <td className={tdClass}>{db.tables >= 0 ? db.tables : "?"}</td>
                        <td className={tdClass}>{db.connections}</td>
                        <td className={tdClass}>
                          {db.users.length > 0 ? db.users.join(", ") : <span className="text-slate-500">geen</span>}
                        </td>
                        {isAdmin && (
                          <td className={tdClass + " text-right"}>
                            <Button variant="ghost" className="text-red-700" onClick={() => setDialog({ kind: "drop-db", db })}>
                              Weghalen
                            </Button>
                          </td>
                        )}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
          <Card title="Gebruikers">
            <div className="overflow-x-auto">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Gebruiker</th>
                    <th className={thClass}>Mag inloggen vanaf</th>
                    <th className={thClass}>Rechten</th>
                    {isAdmin && <th className={thClass} />}
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {d.users.map((u) => (
                    <tr key={u.name}>
                      <td className={tdClass}>
                        <span className="font-medium">{u.name}</span>
                        {u.system && (
                          <span className="ml-2">
                            <Badge tone="slate">{systemLabel(d, u)}</Badge>
                          </span>
                        )}
                      </td>
                      <td className={tdClass}>
                        {pg ? <span className="text-slate-500">de nodes en het VIP (pg_hba)</span> : u.hosts.join(", ")}
                      </td>
                      <td className={tdClass}>
                        <Rights u={u} pg={pg} />
                      </td>
                      {isAdmin && (
                        <td className={tdClass + " text-right whitespace-nowrap"}>
                          {!u.system && (
                            <>
                              <Button variant="ghost" onClick={() => setDialog({ kind: "password", user: u })}>
                                Nieuw wachtwoord
                              </Button>
                              <Button
                                variant="ghost"
                                className="text-red-700"
                                onClick={() => setDialog({ kind: "drop-user", user: u })}
                              >
                                Weghalen
                              </Button>
                            </>
                          )}
                        </td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        </div>
      )}
      {d && dialog?.kind === "create" && <CreateDialog c={c} comp={comp} d={d} onClose={() => setDialog(null)} />}
      {d && dialog?.kind === "drop-db" && (
        <DropDatabaseDialog c={c} comp={comp} pg={pg} db={dialog.db} onClose={() => setDialog(null)} />
      )}
      {dialog?.kind === "password" && (
        <PasswordDialog c={c} comp={comp} d={d} user={dialog.user} onClose={() => setDialog(null)} />
      )}
      {dialog?.kind === "drop-user" && (
        <DropUserDialog c={c} comp={comp} pg={pg} user={dialog.user} onClose={() => setDialog(null)} />
      )}
    </QueryState>
  );
}

function Rights({ u, pg }: { u: ComponentDatabaseUser; pg: boolean }) {
  if (u.global.length === 0 && u.databases.length === 0) return <span className="text-slate-500">geen</span>;
  return (
    <ul className="space-y-0.5">
      {u.global.length > 0 && (
        <li>
          {pg ? "rol" : "alle databases"}: {u.global.length > 6 ? `${u.global.length} rechten` : u.global.join(", ")}
        </li>
      )}
      {u.databases.map((g) => (
        <li key={g.database}>
          {g.database}: {g.all ? (pg ? "eigenaar, alle rechten" : "alle rechten") : g.privileges.join(", ")}
        </li>
      ))}
    </ul>
  );
}

// Secret toont een wachtwoord één keer, met wat de app nodig heeft.
function Secret({ text }: { text: string }) {
  return (
    <div className="space-y-2">
      <Alert kind="info">
        Bewaar het wachtwoord nu, bijvoorbeeld in de instellingen van de app. ClusterForge bewaart het niet: sluit je dit venster,
        dan is het weg. Kwijt? Kies dan Nieuw wachtwoord.
      </Alert>
      <CopyBlock text={text} />
    </div>
  );
}

function CreateDialog({
  c,
  comp,
  d,
  onClose,
}: {
  c: ClusterDetail;
  comp: ClusterComponent;
  d: ComponentDatabases;
  onClose: () => void;
}) {
  const create = useCreateDatabase(c.id, comp.id);
  const [name, setName] = useState("");
  const [user, setUser] = useState("");
  const done = create.data;
  return (
    <Modal title="Database aanmaken" onClose={onClose}>
      {done ? (
        <div className="space-y-4 text-sm">
          <p>
            Database <strong>{done.database}</strong> staat erin, met gebruiker <strong>{done.user}</strong> die er alle rechten
            op heeft.
          </p>
          <Secret text={connectionText(done)} />
          <div className="flex justify-end">
            <Button onClick={onClose}>Klaar</Button>
          </div>
        </div>
      ) : (
        <form
          className="space-y-4 text-sm"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate({ name, user: user || undefined });
          }}
        >
          <Field label="Naam" htmlFor="db-name" hint="Kleine letters, cijfers en _, zoals nextcloud.">
            <Input id="db-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          </Field>
          <Field label="Gebruiker" htmlFor="db-user" hint="Standaard dezelfde naam als de database.">
            <Input id="db-user" value={user} placeholder={name} onChange={(e) => setUser(e.target.value)} />
          </Field>
          {d.engine === "postgresql" ? (
            <p className="text-slate-600 dark:text-slate-400">
              De gebruiker krijgt een sterk wachtwoord en wordt eigenaar van de database, met alle rechten erop. Hij mag inloggen
              vanaf de nodes van het cluster en het VIP {d.address}; dat regelt pg_hba van Patroni.
            </p>
          ) : (
            <p className="text-slate-600 dark:text-slate-400">
              De gebruiker krijgt een sterk wachtwoord en mag inloggen vanaf {d.hosts.join(", ")}: de nodes van het cluster en
              zijn VIP&apos;s. Een app op de node met het VIP die naar het VIP verbindt, komt binnen met het VIP als adres.
            </p>
          )}
          {create.isError && <Alert>{create.error.message}</Alert>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={onClose}>
              Annuleren
            </Button>
            <Button type="submit" disabled={name === "" || create.isPending}>
              {create.isPending ? "Bezig…" : "Aanmaken"}
            </Button>
          </div>
        </form>
      )}
    </Modal>
  );
}

function PasswordDialog({
  c,
  comp,
  d,
  user,
  onClose,
}: {
  c: ClusterDetail;
  comp: ClusterComponent;
  d?: ComponentDatabases;
  user: ComponentDatabaseUser;
  onClose: () => void;
}) {
  const set = useSetUserPassword(c.id, comp.id);
  const totp = useProdTotp();
  const [confirm, setConfirm] = useState("");
  const prod = c.environment === "prod";
  const done = set.data;
  return (
    <Modal title={`Nieuw wachtwoord voor ${user.name}`} onClose={onClose}>
      <div className="space-y-4 text-sm">
        {done ? (
          <>
            <Secret
              text={connectionText({
                address: d?.address ?? "",
                port: d?.port ?? 3306,
                database: user.databases.length === 1 ? user.databases[0].database : undefined,
                user: done.user,
                password: done.password,
              })}
            />
            <div className="flex justify-end">
              <Button onClick={onClose}>Klaar</Button>
            </div>
          </>
        ) : (
          <>
            <p>
              {user.name} krijgt{user.hosts.length > 0 && ` op ${user.hosts.join(", ")}`} een nieuw wachtwoord. Het oude werkt
              meteen niet meer: een app die als {user.name} inlogt, kan pas weer verbinden als het nieuwe wachtwoord in haar
              instellingen staat.
            </p>
            {prod && <ProdConfirm slug={c.slug} value={confirm} onChange={setConfirm} />}
            {set.isError && <Alert>{set.error.message}</Alert>}
            <div className="flex justify-end gap-2">
              <Button variant="secondary" onClick={onClose}>
                Annuleren
              </Button>
              <Button
                disabled={set.isPending || (prod && !prodConfirmed(c.slug, confirm, totp))}
                onClick={() => set.mutate({ user: user.name, confirm: prod ? confirm : undefined })}
              >
                {set.isPending ? "Bezig…" : "Nieuw wachtwoord"}
              </Button>
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}

function DropDatabaseDialog({
  c,
  comp,
  pg,
  db,
  onClose,
}: {
  c: ClusterDetail;
  comp: ClusterComponent;
  pg: boolean;
  db: ComponentDatabase;
  onClose: () => void;
}) {
  const drop = useDropDatabase(c.id, comp.id);
  return (
    <DropDialog
      c={c}
      title={`Database ${db.name} weghalen`}
      name={db.name}
      pending={drop.isPending}
      error={drop.isError ? drop.error.message : ""}
      onClose={onClose}
      onDrop={(typed, confirm) => drop.mutate({ name: db.name, typed, confirm }, { onSuccess: onClose })}
    >
      <p>
        {db.name} ({formatBytes(db.size_bytes)}, {db.tables >= 0 ? `${db.tables} tabellen` : "tabellen onbekend"}) gaat weg op
        alle nodes, met alle data. Dat kan niet ongedaan worden; zet eerst een dump of back-up klaar als je de data nog nodig
        hebt.
      </p>
      {db.connections > 0 && (
        <Alert>
          Er {db.connections === 1 ? "is nu 1 verbinding" : `zijn nu ${db.connections} verbindingen`} met {db.name}: een app
          gebruikt haar nog.{pg && " PostgreSQL weigert weghalen zolang er verbindingen zijn; stop de app eerst."}
        </Alert>
      )}
      {db.users.length > 0 &&
        (pg ? (
          <p className="text-slate-600 dark:text-slate-400">
            {db.users.join(", ")} {db.users.length === 1 ? "blijft" : "blijven"} bestaan; haal{" "}
            {db.users.length === 1 ? "hem" : "ze"} daarna apart weg bij Gebruikers.
          </p>
        ) : (
          <p className="text-slate-600 dark:text-slate-400">
            {db.users.join(", ")} {db.users.length === 1 ? "houdt zijn" : "houden hun"} rechten op de naam {db.name}; haal{" "}
            {db.users.length === 1 ? "hem" : "ze"} apart weg bij Gebruikers.
          </p>
        ))}
    </DropDialog>
  );
}

function DropUserDialog({
  c,
  comp,
  pg,
  user,
  onClose,
}: {
  c: ClusterDetail;
  comp: ClusterComponent;
  pg: boolean;
  user: ComponentDatabaseUser;
  onClose: () => void;
}) {
  const drop = useDropUser(c.id, comp.id);
  return (
    <DropDialog
      c={c}
      title={`Gebruiker ${user.name} weghalen`}
      name={user.name}
      pending={drop.isPending}
      error={drop.isError ? drop.error.message : ""}
      onClose={onClose}
      onDrop={(typed, confirm) => drop.mutate({ user: user.name, typed, confirm }, { onSuccess: onClose })}
    >
      {pg ? (
        <p>
          {user.name} gaat weg, met al zijn rechten. Een app die als {user.name} inlogt, kan dan niet meer verbinden. Is hij nog
          eigenaar van een database, dan weigert PostgreSQL: haal die database eerst weg.
        </p>
      ) : (
        <p>
          {user.name} gaat weg op {user.hosts.join(", ")}, met al zijn rechten. Een app die als {user.name} inlogt, kan dan niet
          meer verbinden. De databases blijven staan.
        </p>
      )}
    </DropDialog>
  );
}

function DropDialog({
  c,
  title,
  name,
  pending,
  error,
  onClose,
  onDrop,
  children,
}: {
  c: ClusterDetail;
  title: string;
  name: string;
  pending: boolean;
  error: string;
  onClose: () => void;
  onDrop: (typed: string, confirm?: string) => void;
  children: ReactNode;
}) {
  const totp = useProdTotp();
  const [typed, setTyped] = useState("");
  const [confirm, setConfirm] = useState("");
  const prod = c.environment === "prod";
  return (
    <Modal title={title} onClose={onClose}>
      <div className="space-y-4 text-sm">
        {children}
        <Field label={`Tik ${name} om te bevestigen`} htmlFor="drop-name">
          <Input id="drop-name" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />
        </Field>
        {prod && <ProdConfirm slug={c.slug} value={confirm} onChange={setConfirm} />}
        {error && <Alert>{error}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            variant="danger"
            disabled={typed !== name || pending || (prod && !prodConfirmed(c.slug, confirm, totp))}
            onClick={() => onDrop(typed, prod ? confirm : undefined)}
          >
            {pending ? "Bezig…" : "Weghalen"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
