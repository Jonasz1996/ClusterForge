"use client";

import { Suspense, useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Modal } from "@/components/Modal";
import { CallForm, PbsGate, usePbsRunner, when } from "@/components/pbs/bits";
import { ActionMenu, TypedConfirm } from "@/components/pve/bits";
import { Tile as Win, TileGrid } from "@/components/Tiles";
import { Alert, Badge, Button, Card, cx, Field, Input, PageHeader } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { pbsCreateToken, usePbsGet, type PbsServer } from "@/lib/pbs";
import { useQueryClient } from "@tanstack/react-query";

export default function PbsAccessPage() {
  const isAdmin = useIsAdmin();
  return (
    <div className="space-y-6">
      <PageHeader
        title="Toegang"
        description="Gebruikers, API-tokens en rechten van de Backup Server. Geef Proxmox of een sync-job een eigen token met alleen de rechten die het nodig heeft."
      />
      {!isAdmin ? (
        <Alert kind="info">Alleen een beheerder ziet gebruikers en rechten.</Alert>
      ) : (
        <Suspense>
          <PbsGate>
            {(s) => (
              <TileGrid key={s.node_id}>
                <Win wide>
                  <UsersCard srv={s} />
                </Win>
                <Win wide>
                  <AclCard srv={s} />
                </Win>
              </TileGrid>
            )}
          </PbsGate>
        </Suspense>
      )}
    </div>
  );
}

type Token = { tokenid: string; enable?: boolean; expire?: number; comment?: string };
type User = { userid: string; enable?: boolean; expire?: number; firstname?: string; lastname?: string; email?: string; comment?: string; tokens?: Token[] };

function UsersCard({ srv }: { srv: PbsServer }) {
  const id = srv.node_id;
  const q = usePbsGet<User[]>(id, "/access/users", { params: { "include_tokens": "1" }, refetch: 60_000 });
  const { run, pending, banner } = usePbsRunner(id);
  const [dialog, setDialog] = useState<{ kind: "create" } | { kind: "edit" | "password" | "delete" | "token"; u: User; regen?: string } | null>(null);
  return (
    <Card title="Gebruikers en tokens">
      <div className="space-y-3">
        {banner}
        <div className="flex justify-end">
          <Button variant="secondary" onClick={() => setDialog({ kind: "create" })}>
            Gebruiker toevoegen
          </Button>
        </div>
        <QueryState q={q}>
          {(q.data ?? []).length === 0 ? (
            <Empty>Geen gebruikers.</Empty>
          ) : (
            <div className="relative -mx-4 overflow-x-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Gebruiker</th>
                    <th className={thClass}>Tokens</th>
                    <th className={cx(thClass, "hidden md:table-cell")}>Vervalt</th>
                    <th className={thClass} />
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {(q.data ?? []).map((u) => (
                    <tr key={u.userid}>
                      <td className={tdClass}>
                        <span className="font-medium">{u.userid}</span>
                        {u.enable === false && (
                          <span className="ml-2">
                            <Badge tone="amber">uit</Badge>
                          </span>
                        )}
                        {(u.comment || u.email) && <span className="block text-xs text-slate-500">{[u.comment, u.email].filter(Boolean).join(" · ")}</span>}
                      </td>
                      <td className={tdClass}>
                        <ul className="space-y-1">
                          {(u.tokens ?? []).map((t) => (
                            <li key={t.tokenid} className="flex items-center gap-2 text-sm">
                              <span className="font-mono text-xs">{t.tokenid.split("!")[1] ?? t.tokenid}</span>
                              {t.enable === false && <Badge tone="amber">uit</Badge>}
                              <ActionMenu
                                disabled={pending}
                                items={[
                                  {
                                    label: "Nieuw geheim",
                                    onClick: () => setDialog({ kind: "token", u, regen: t.tokenid }),
                                  },
                                  {
                                    label: "Verwijderen",
                                    danger: true,
                                    onClick: async () => {
                                      if (await askConfirm(`Token ${t.tokenid} verwijderen? Wat het gebruikt (zoals Proxmox-storage) verliest meteen toegang.`, { danger: true, confirmLabel: "Verwijderen" }))
                                        void run({ method: "delete", path: `/access/users/${u.userid}/token/${t.tokenid.split("!")[1]}` });
                                    },
                                  },
                                ]}
                              />
                            </li>
                          ))}
                        </ul>
                      </td>
                      <td className={cx(tdClass, "hidden text-slate-500 md:table-cell")}>{u.expire ? when(u.expire) : "nooit"}</td>
                      <td className={cx(tdClass, "text-right")}>
                        <ActionMenu
                          disabled={pending}
                          items={[
                            { label: "Token maken…", onClick: () => setDialog({ kind: "token", u }) },
                            { label: "Wijzigen…", onClick: () => setDialog({ kind: "edit", u }) },
                            ...(u.userid.endsWith("@pbs") ? [{ label: "Wachtwoord…", onClick: () => setDialog({ kind: "password", u }) }] : []),
                            ...(u.userid === "root@pam" ? [] : [{ label: "Verwijderen…", danger: true, onClick: () => setDialog({ kind: "delete", u }) }]),
                          ]}
                        />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </QueryState>
      </div>
      {dialog?.kind === "create" && (
        <CallForm
          id={id}
          title="Gebruiker toevoegen"
          intro="Een gebruiker van PBS zelf (@pbs) met een wachtwoord, of van het systeem (@pam) als die op de machine bestaat. Rechten geef je daarna onder Rechten."
          fields={[
            { key: "userid", label: "Gebruiker", required: true, placeholder: "backup@pbs" },
            { key: "password", label: "Wachtwoord (alleen @pbs)", type: "password" },
            { key: "email", label: "E-mail" },
            { key: "comment", label: "Omschrijving" },
          ]}
          method="create"
          path="/access/users"
          submitLabel="Toevoegen"
          onClose={() => setDialog(null)}
        />
      )}
      {dialog?.kind === "edit" && (
        <CallForm
          id={id}
          title={`${dialog.u.userid} wijzigen`}
          fields={[
            { key: "enable", label: "Aan", type: "checkbox" },
            { key: "firstname", label: "Voornaam" },
            { key: "lastname", label: "Achternaam" },
            { key: "email", label: "E-mail" },
            { key: "comment", label: "Omschrijving" },
          ]}
          initial={{
            enable: dialog.u.enable === false ? "0" : "1",
            firstname: dialog.u.firstname ?? "",
            lastname: dialog.u.lastname ?? "",
            email: dialog.u.email ?? "",
            comment: dialog.u.comment ?? "",
          }}
          method="set"
          path={`/access/users/${dialog.u.userid}`}
          submitLabel="Opslaan"
          onClose={() => setDialog(null)}
        />
      )}
      {dialog?.kind === "password" && (
        <CallForm
          id={id}
          title={`Wachtwoord van ${dialog.u.userid}`}
          fields={[{ key: "password", label: "Nieuw wachtwoord", type: "password", required: true, hint: "Minstens 8 tekens. Gaat versleuteld naar de taak." }]}
          method="set"
          path="/access/password"
          onlyChanged={false}
          fixed={{ userid: dialog.u.userid }}
          submitLabel="Wijzigen"
          onClose={() => setDialog(null)}
        />
      )}
      {dialog?.kind === "delete" && (
        <TypedConfirm
          title={`${dialog.u.userid} verwijderen`}
          expect={dialog.u.userid}
          confirmLabel="Verwijderen"
          pending={pending}
          onClose={() => setDialog(null)}
          onConfirm={(typed) => {
            void run({ method: "delete", path: `/access/users/${dialog.u.userid}`, confirm: typed });
            setDialog(null);
          }}
        >
          <p>De gebruiker en zijn tokens verdwijnen. Back-ups die hij bezit, blijven.</p>
        </TypedConfirm>
      )}
      {dialog?.kind === "token" && <TokenDialog id={id} user={dialog.u} regen={dialog.regen ?? ""} onClose={() => setDialog(null)} />}
    </Card>
  );
}

function TokenDialog({ id, user, regen, onClose }: { id: string; user: User; regen: string; onClose: () => void }) {
  const [name, setName] = useState(regen ? (regen.split("!")[1] ?? "") : "");
  const [comment, setComment] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [made, setMade] = useState<{ full_tokenid: string; value: string } | null>(null);
  const qc = useQueryClient();
  const make = async () => {
    setBusy(true);
    setErr("");
    try {
      setMade(await pbsCreateToken(id, { userid: user.userid, tokenid: name.trim(), comment: comment.trim() || undefined, regenerate: !!regen }));
      void qc.invalidateQueries({ queryKey: ["pbs", id] });
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Mislukt");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal title={regen ? `Nieuw geheim voor ${regen}` : `Token voor ${user.userid}`} onClose={onClose}>
      <div className="space-y-3 text-sm">
        {err && <Alert>{err}</Alert>}
        {made ? (
          <>
            <Alert kind="success">Het token is gemaakt. Kopieer het geheim nu: PBS toont het niet opnieuw en ClusterForge bewaart het niet.</Alert>
            <Field label="Token" htmlFor="tok-id">
              <Input id="tok-id" readOnly value={made.full_tokenid} onFocus={(e) => e.target.select()} />
            </Field>
            <Field label="Geheim" htmlFor="tok-val">
              <Input id="tok-val" readOnly value={made.value} onFocus={(e) => e.target.select()} className="font-mono" />
            </Field>
            <p className="text-slate-500">Een nieuw token heeft geen rechten. Geef het rechten onder Rechten, bijvoorbeeld DatastoreBackup op /datastore/store1.</p>
          </>
        ) : (
          <>
            {!regen && (
              <>
                <Field label="Naam" htmlFor="tok-name">
                  <Input id="tok-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="pve" />
                </Field>
                <Field label="Omschrijving" htmlFor="tok-c">
                  <Input id="tok-c" value={comment} onChange={(e) => setComment(e.target.value)} />
                </Field>
              </>
            )}
            {regen && <p>Het oude geheim werkt meteen niet meer. Zet het nieuwe daarna overal waar het token gebruikt wordt.</p>}
          </>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            {made ? "Sluiten" : "Annuleren"}
          </Button>
          {!made && (
            <Button disabled={busy || !name.trim()} onClick={() => void make()}>
              {regen ? "Nieuw geheim" : "Maken"}
            </Button>
          )}
        </div>
      </div>
    </Modal>
  );
}

type Acl = { path: string; ugid: string; ugid_type?: string; roleid: string; propagate?: boolean };

const roles = [
  "Admin",
  "Audit",
  "DatastoreAdmin",
  "DatastoreAudit",
  "DatastoreBackup",
  "DatastorePowerUser",
  "DatastoreReader",
  "RemoteAdmin",
  "RemoteAudit",
  "RemoteSyncOperator",
  "RemoteSyncPushOperator",
  "TapeAdmin",
  "TapeAudit",
  "TapeOperator",
  "TapeReader",
  "NoAccess",
];

function AclCard({ srv }: { srv: PbsServer }) {
  const id = srv.node_id;
  const q = usePbsGet<Acl[]>(id, "/access/acl", { refetch: 60_000 });
  const { run, pending, banner } = usePbsRunner(id);
  const [open, setOpen] = useState(false);
  return (
    <Card title="Rechten">
      <div className="space-y-3">
        <p className="text-sm text-slate-500">
          Wie wat mag, per pad. Voor Proxmox volstaat DatastoreBackup op /datastore/&lt;naam&gt;; voor een sync-job RemoteSyncOperator op /remote/&lt;naam&gt;.
        </p>
        {banner}
        <div className="flex justify-end">
          <Button variant="secondary" onClick={() => setOpen(true)}>
            Recht geven
          </Button>
        </div>
        <QueryState q={q}>
          {(q.data ?? []).length === 0 ? (
            <Empty>Geen rechten buiten root@pam.</Empty>
          ) : (
            <div className="relative -mx-4 overflow-x-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Pad</th>
                    <th className={thClass}>Wie</th>
                    <th className={thClass}>Rol</th>
                    <th className={thClass} />
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {(q.data ?? []).map((a) => (
                    <tr key={a.path + a.ugid + a.roleid}>
                      <td className={cx(tdClass, "font-mono text-xs")}>
                        {a.path}
                        {a.propagate === false && <span className="ml-1 text-slate-500">(niet erven)</span>}
                      </td>
                      <td className={tdClass}>{a.ugid}</td>
                      <td className={tdClass}>{a.roleid}</td>
                      <td className={cx(tdClass, "text-right")}>
                        <Button
                          variant="secondary-danger"
                          disabled={pending}
                          onClick={async () => {
                            if (await askConfirm(`${a.roleid} op ${a.path} afnemen van ${a.ugid}?`, { danger: true, confirmLabel: "Afnemen" }))
                              void run({ method: "set", path: "/access/acl", params: { path: a.path, role: a.roleid, "auth-id": a.ugid, delete: "1" } });
                          }}
                        >
                          Afnemen
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </QueryState>
      </div>
      {open && (
        <CallForm
          id={id}
          title="Recht geven"
          fields={[
            { key: "path", label: "Pad", required: true, placeholder: "/datastore/store1", hint: "/ (alles), /datastore/<naam>[/<namespace>], /remote/<naam>, /system" },
            { key: "auth-id", label: "Gebruiker of token", required: true, placeholder: "pve@pbs!pve" },
            { key: "role", label: "Rol", type: "select", options: roles.map((r) => ({ value: r, label: r })) },
            { key: "propagate", label: "Ook voor alles eronder", type: "checkbox" },
          ]}
          initial={{ path: srv.datastores[0] ? `/datastore/${srv.datastores[0]}` : "/", role: "DatastoreBackup", propagate: "1" }}
          method="set"
          path="/access/acl"
          onlyChanged={false}
          submitLabel="Geven"
          onClose={() => setOpen(false)}
        />
      )}
    </Card>
  );
}
