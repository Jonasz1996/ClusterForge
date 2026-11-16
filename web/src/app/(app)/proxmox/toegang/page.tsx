"use client";

import { useQueryClient } from "@tanstack/react-query";
import { Suspense, useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { CopyBlock } from "@/components/inventory/InstallAgent";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Modal } from "@/components/Modal";
import { ActionMenu, DatacenterGate, TypedConfirm, useCallRunner } from "@/components/pve/bits";
import { Check, FormModal } from "@/components/pve/guest/common";
import { Alert, Badge, Button, Card, Field, Input, PageHeader, Select } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { createPveToken, usePveGet, type Datacenter, type PveAcl, type PveGroup, type PveRealm, type PveRole, type PveTokenCreated, type PveUser } from "@/lib/pve";

// Gebruikers, groepen, rechten en API-tokens van Proxmox (fase 26 M6).
// Alleen voor beheerders: /access is voor kijkers afgeschermd.

export default function AccessPage() {
  const isAdmin = useIsAdmin();
  return (
    <div className="space-y-6">
      <PageHeader title="Gebruikers en tokens" description="Wie in Proxmox mag inloggen, in welke groep, met welke rechten, en de API-tokens voor andere programma's (zoals je homepage)." />
      {isAdmin ? (
        <Suspense>
          <DatacenterGate>{(dc) => <AccessView key={dc.id} dc={dc} />}</DatacenterGate>
        </Suspense>
      ) : (
        <Alert kind="info">Alleen een beheerder van ClusterForge ziet de gebruikers van Proxmox.</Alert>
      )}
    </div>
  );
}

const groupsOf = (u: PveUser) => (Array.isArray(u.groups) ? u.groups : (u.groups ?? "").split(",").filter(Boolean));

function AccessView({ dc }: { dc: Datacenter }) {
  const users = usePveGet<PveUser[]>(dc.id, "/access/users", { params: { full: "1" }, refetch: 60_000 });
  const groups = usePveGet<PveGroup[]>(dc.id, "/access/groups", { refetch: 120_000 });
  const acl = usePveGet<PveAcl[]>(dc.id, "/access/acl", { refetch: 60_000 });
  const roles = usePveGet<PveRole[]>(dc.id, "/access/roles", { refetch: false });
  const realms = usePveGet<PveRealm[]>(dc.id, "/access/domains", { refetch: false });
  const { run, pending, banner } = useCallRunner(dc.id);
  const qc = useQueryClient();
  const [dialog, setDialog] = useState<
    | { kind: "user"; u?: PveUser }
    | { kind: "password"; u: PveUser }
    | { kind: "delete"; u: PveUser }
    | { kind: "token"; u: PveUser }
    | { kind: "shown"; t: PveTokenCreated }
    | { kind: "group" }
    | { kind: "acl" }
    | null
  >(null);
  const close = () => setDialog(null);
  const [now] = useState(() => Date.now());
  const list = [...(users.data ?? [])].sort((a, b) => a.userid.localeCompare(b.userid));

  return (
    <div className="space-y-4">
      {banner}
      <Card
        title={
          <span className="flex items-center justify-between gap-2">
            <span>Gebruikers</span>
            <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "user" })}>
              Gebruiker toevoegen
            </Button>
          </span>
        }
      >
        <QueryState q={users}>
          {list.length === 0 ? (
            <Empty>Geen gebruikers.</Empty>
          ) : (
            <div className="relative -mx-4 overflow-x-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Gebruiker</th>
                    <th className={thClass}>Groepen</th>
                    <th className={thClass}>API-tokens</th>
                    <th className={thClass}>Status</th>
                    <th className={thClass}>
                      <span className="sr-only">Acties</span>
                    </th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {list.map((u) => {
                    const expired = !!u.expire && u.expire * 1000 < now;
                    return (
                      <tr key={u.userid} className="align-top">
                        <td className={tdClass}>
                          <span className="font-medium">{u.userid}</span>
                          <span className="block text-xs text-slate-500">
                            {[[u.firstname, u.lastname].filter(Boolean).join(" "), u.email].filter(Boolean).join(" · ")}
                          </span>
                          {u.comment && <span className="block text-xs text-slate-500">{u.comment}</span>}
                        </td>
                        <td className={tdClass}>{groupsOf(u).join(", ") || "–"}</td>
                        <td className={tdClass}>
                          {(u.tokens ?? []).length === 0 ? (
                            <span className="text-slate-500">–</span>
                          ) : (
                            <ul className="space-y-1">
                              {u.tokens!.map((t) => (
                                <li key={t.tokenid} className="flex items-center gap-2 text-xs">
                                  <span className="font-mono">{t.tokenid}</span>
                                  {t.privsep ? <Badge tone="blue">eigen rechten</Badge> : <Badge tone="amber">rechten van gebruiker</Badge>}
                                  {t.expire ? <span className="text-slate-500">tot {new Date(t.expire * 1000).toLocaleDateString("nl-BE")}</span> : null}
                                  <button
                                    type="button"
                                    className="text-slate-500 hover:text-red-600"
                                    aria-label={`Token ${t.tokenid} intrekken`}
                                    disabled={pending}
                                    onClick={async () => {
                                      if (await askConfirm(`Token ${u.userid}!${t.tokenid} intrekken? Programma's die het gebruiken, krijgen meteen geen toegang meer.`))
                                        void run({ method: "delete", path: `/access/users/${u.userid}/token/${t.tokenid}` });
                                    }}
                                  >
                                    intrekken
                                  </button>
                                </li>
                              ))}
                            </ul>
                          )}
                        </td>
                        <td className={tdClass}>
                          {u.enable === 0 ? <Badge tone="slate">uit</Badge> : expired ? <Badge tone="red">verlopen</Badge> : <Badge tone="green">actief</Badge>}
                        </td>
                        <td className={tdClass + " text-right"}>
                          <ActionMenu
                            disabled={pending}
                            items={[
                              { label: "API-token maken", onClick: () => setDialog({ kind: "token", u }) },
                              { label: "Wijzigen", onClick: () => setDialog({ kind: "user", u }) },
                              ...(u.userid.endsWith("@pve") || u.userid.endsWith("@pam") ? [{ label: "Wachtwoord wijzigen", onClick: () => setDialog({ kind: "password", u }) }] : []),
                              { label: u.enable === 0 ? "Aanzetten" : "Uitzetten", onClick: () => void run({ method: "set", path: `/access/users/${u.userid}`, params: { enable: u.enable === 0 ? "1" : "0" } }) },
                              ...(u.userid !== "root@pam" ? [{ label: "Verwijderen…", onClick: () => setDialog({ kind: "delete", u }), danger: true }] : []),
                            ]}
                          />
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </QueryState>
      </Card>

      <div className="grid gap-4 lg:grid-cols-[1fr_2fr]">
        <Card
          title={
            <span className="flex items-center justify-between gap-2">
              <span>Groepen</span>
              <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "group" })}>
                Toevoegen
              </Button>
            </span>
          }
        >
          <QueryState q={groups}>
            {(groups.data ?? []).length === 0 ? (
              <Empty>Geen groepen.</Empty>
            ) : (
              <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
                {groups.data!.map((g) => (
                  <li key={g.groupid} className="flex items-center justify-between gap-2 py-1.5">
                    <span>
                      <span className="font-medium">{g.groupid}</span>
                      {g.comment && <span className="block text-xs text-slate-500">{g.comment}</span>}
                    </span>
                    <Button
                      variant="secondary"
                      disabled={pending}
                      onClick={async () => {
                        if (await askConfirm(`Groep ${g.groupid} weghalen? De rechten van die groep vervallen.`)) void run({ method: "delete", path: `/access/groups/${g.groupid}` });
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

        <Card
          title={
            <span className="flex items-center justify-between gap-2">
              <span>Rechten</span>
              <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "acl" })}>
                Recht geven
              </Button>
            </span>
          }
        >
          <QueryState q={acl}>
            {(acl.data ?? []).length === 0 ? (
              <Empty>Nog geen rechten; alleen root@pam kan alles.</Empty>
            ) : (
              <div className="relative -mx-4 overflow-x-auto sm:mx-0">
                <table className={tableClass}>
                  <thead>
                    <tr>
                      <th className={thClass}>Op</th>
                      <th className={thClass}>Wie</th>
                      <th className={thClass}>Rol</th>
                      <th className={thClass}>
                        <span className="sr-only">Acties</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                    {acl.data!.map((a) => (
                      <tr key={`${a.path}-${a.type}-${a.ugid}-${a.roleid}`}>
                        <td className={tdClass + " font-mono text-xs"}>
                          {a.path}
                          {a.propagate ? <span className="block font-sans text-slate-500">en alles eronder</span> : null}
                        </td>
                        <td className={tdClass}>
                          {a.ugid} <span className="text-xs text-slate-500">{a.type === "group" ? "groep" : a.type === "token" ? "token" : "gebruiker"}</span>
                        </td>
                        <td className={tdClass}>{a.roleid}</td>
                        <td className={tdClass + " text-right"}>
                          <Button
                            variant="secondary"
                            disabled={pending}
                            onClick={async () => {
                              if (!(await askConfirm(`${a.roleid} van ${a.ugid} op ${a.path} intrekken?`))) return;
                              const who = a.type === "group" ? "groups" : a.type === "token" ? "tokens" : "users";
                              void run({ method: "set", path: "/access/acl", params: { path: a.path, roles: a.roleid, [who]: a.ugid, delete: "1" } });
                            }}
                          >
                            Intrekken
                          </Button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </QueryState>
        </Card>
      </div>

      {dialog?.kind === "user" && (
        <UserDialog edit={dialog.u} realms={realms.data ?? []} groups={(groups.data ?? []).map((g) => g.groupid)} taken={list.map((u) => u.userid)} run={run} pending={pending} onClose={close} />
      )}
      {dialog?.kind === "password" && <PasswordDialog u={dialog.u} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "delete" && (
        <TypedConfirm
          title={`${dialog.u.userid} verwijderen`}
          expect={dialog.u.userid}
          confirmLabel="Verwijderen"
          pending={pending}
          onClose={close}
          onConfirm={(typed) => void run({ method: "delete", path: `/access/users/${dialog.u.userid}`, confirm: typed }).then((ok) => ok && close())}
        >
          <p className="text-sm">De gebruiker, zijn API-tokens en zijn rechten verdwijnen. Programma&apos;s die een token van hem gebruiken, krijgen geen toegang meer.</p>
        </TypedConfirm>
      )}
      {dialog?.kind === "token" && (
        <TokenDialog
          dc={dc}
          u={dialog.u}
          onClose={close}
          onCreated={(t) => {
            void qc.invalidateQueries({ queryKey: ["pve", dc.id] });
            setDialog({ kind: "shown", t });
          }}
        />
      )}
      {dialog?.kind === "shown" && (
        <Modal title="API-token gemaakt" onClose={close}>
          <div className="space-y-3 text-sm">
            <Alert kind="info">Kopieer de waarde nu. Proxmox toont hem nooit meer en ClusterForge bewaart hem niet.</Alert>
            <Field label="Token-ID" htmlFor="tk-id">
              <CopyBlock text={dialog.t.full_tokenid} />
            </Field>
            <Field label="Waarde" htmlFor="tk-v">
              <CopyBlock text={dialog.t.value} />
            </Field>
            <Field label="Header voor een programma" htmlFor="tk-h">
              <CopyBlock text={`Authorization: PVEAPIToken=${dialog.t.full_tokenid}=${dialog.t.value}`} />
            </Field>
            <p className="text-xs text-slate-500">Met eigen rechten heeft het token nog niets: geef het hieronder bij Rechten een rol (bijvoorbeeld PVEAuditor op / om alleen te lezen).</p>
          </div>
        </Modal>
      )}
      {dialog?.kind === "group" && <GroupDialog taken={(groups.data ?? []).map((g) => g.groupid)} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "acl" && <AclDialog users={list} groups={(groups.data ?? []).map((g) => g.groupid)} roles={roles.data ?? []} run={run} pending={pending} onClose={close} />}
    </div>
  );
}

type Runner = ReturnType<typeof useCallRunner>["run"];

function UserDialog({ edit, realms, groups, taken, run, pending, onClose }: { edit?: PveUser; realms: PveRealm[]; groups: string[]; taken: string[]; run: Runner; pending: boolean; onClose: () => void }) {
  const [name, setName] = useState("");
  const [realm, setRealm] = useState(realms.find((r) => r.type === "pve")?.realm ?? "pve");
  const [password, setPassword] = useState("");
  const [first, setFirst] = useState(edit?.firstname ?? "");
  const [last, setLast] = useState(edit?.lastname ?? "");
  const [email, setEmail] = useState(edit?.email ?? "");
  const [comment, setComment] = useState(edit?.comment ?? "");
  const [inGroups, setInGroups] = useState<string[]>(edit ? groupsOf(edit) : []);
  const [expire, setExpire] = useState(edit?.expire ? new Date(edit.expire * 1000).toISOString().slice(0, 10) : "");
  const userid = edit?.userid ?? `${name}@${realm}`;
  const realmType = realms.find((r) => r.realm === realm)?.type;
  const error = !edit && !/^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/.test(name)
    ? "Geef een gebruikersnaam (letters, cijfers, . - en _)."
    : !edit && taken.includes(userid)
      ? "Die gebruiker bestaat al."
      : !edit && realmType === "pve" && password.length < 8
        ? "Een wachtwoord heeft minstens 8 tekens."
        : email && !/^\S+@\S+\.\S+$/.test(email)
          ? "Ongeldig e-mailadres."
          : null;
  const submit = () => {
    const p: Record<string, string> = {};
    const del: string[] = [];
    const opt = (k: string, v: string) => {
      if (v) p[k] = v;
      else if (edit && (edit as Record<string, unknown>)[k]) del.push(k);
    };
    opt("firstname", first);
    opt("lastname", last);
    opt("email", email);
    opt("comment", comment);
    p.groups = inGroups.join(",");
    p.expire = expire ? String(Math.floor(new Date(expire + "T23:59:59").getTime() / 1000)) : "0";
    if (del.length) p.delete = del.join(",");
    const call = edit
      ? { method: "set" as const, path: `/access/users/${edit.userid}`, params: p }
      : { method: "create" as const, path: "/access/users", params: { ...p, userid, ...(realmType === "pve" && password ? { password } : {}) } };
    void run(call).then((ok) => ok && onClose());
  };
  return (
    <FormModal title={edit ? `${edit.userid} wijzigen` : "Gebruiker toevoegen"} onClose={onClose} onSubmit={submit} submitLabel="Opslaan" disabled={pending} error={error} wide>
      {!edit && (
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label="Naam" htmlFor="u-n">
            <Input id="u-n" value={name} onChange={(e) => setName(e.target.value.trim())} />
          </Field>
          <Field label="Realm" htmlFor="u-r" hint={realmType === "pam" ? "De Linux-gebruiker moet op elke host bestaan" : undefined}>
            <Select id="u-r" value={realm} onChange={(e) => setRealm(e.target.value)}>
              {(realms.length ? realms : [{ realm: "pve", type: "pve" }]).map((r) => (
                <option key={r.realm} value={r.realm}>
                  {r.realm} ({r.type === "pve" ? "Proxmox" : r.type === "pam" ? "Linux" : r.type})
                </option>
              ))}
            </Select>
          </Field>
          {realmType === "pve" && (
            <Field label="Wachtwoord" htmlFor="u-p">
              <Input id="u-p" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
          )}
        </div>
      )}
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Voornaam" htmlFor="u-f">
          <Input id="u-f" value={first} onChange={(e) => setFirst(e.target.value)} />
        </Field>
        <Field label="Achternaam" htmlFor="u-l">
          <Input id="u-l" value={last} onChange={(e) => setLast(e.target.value)} />
        </Field>
        <Field label="E-mail" htmlFor="u-e">
          <Input id="u-e" type="email" value={email} onChange={(e) => setEmail(e.target.value.trim())} />
        </Field>
        <Field label="Verloopt op" htmlFor="u-x" hint="Leeg is nooit">
          <Input id="u-x" type="date" value={expire} onChange={(e) => setExpire(e.target.value)} />
        </Field>
      </div>
      <Field label="Opmerking" htmlFor="u-c">
        <Input id="u-c" value={comment} onChange={(e) => setComment(e.target.value)} />
      </Field>
      {groups.length > 0 && (
        <fieldset>
          <legend className="text-sm font-medium">Groepen</legend>
          <div className="mt-1 grid gap-1 sm:grid-cols-3">
            {groups.map((g) => (
              <Check key={g} id={`u-g-${g}`} checked={inGroups.includes(g)} onChange={(v) => setInGroups(v ? [...inGroups, g] : inGroups.filter((x) => x !== g))}>
                {g}
              </Check>
            ))}
          </div>
        </fieldset>
      )}
    </FormModal>
  );
}

function PasswordDialog({ u, run, pending, onClose }: { u: PveUser; run: Runner; pending: boolean; onClose: () => void }) {
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const error = pw.length < 8 ? "Minstens 8 tekens." : pw !== pw2 ? "De twee wachtwoorden verschillen." : null;
  return (
    <FormModal
      title={`Wachtwoord van ${u.userid}`}
      onClose={onClose}
      onSubmit={() => void run({ method: "set", path: "/access/password", params: { userid: u.userid, password: pw } }).then((ok) => ok && onClose())}
      submitLabel="Wijzigen"
      disabled={pending}
      error={error}
    >
      <Field label="Nieuw wachtwoord" htmlFor="pw-1">
        <Input id="pw-1" type="password" autoComplete="new-password" value={pw} onChange={(e) => setPw(e.target.value)} />
      </Field>
      <Field label="Nog eens" htmlFor="pw-2">
        <Input id="pw-2" type="password" autoComplete="new-password" value={pw2} onChange={(e) => setPw2(e.target.value)} />
      </Field>
      {u.userid.endsWith("@pam") && <p className="text-xs text-slate-500">Bij een Linux-gebruiker (@pam) verandert Proxmox het wachtwoord alleen op de host waarlangs ClusterForge werkt.</p>}
    </FormModal>
  );
}

function TokenDialog({ dc, u, onClose, onCreated }: { dc: Datacenter; u: PveUser; onClose: () => void; onCreated: (t: PveTokenCreated) => void }) {
  const [id, setId] = useState("");
  const [comment, setComment] = useState("");
  const [privsep, setPrivsep] = useState(true);
  const [expire, setExpire] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const error = !/^[A-Za-z][A-Za-z0-9._-]{1,63}$/.test(id)
    ? "De naam begint met een letter (bijvoorbeeld homepage)."
    : (u.tokens ?? []).some((t) => t.tokenid === id)
      ? "Die gebruiker heeft al een token met die naam."
      : err;
  const submit = async () => {
    setBusy(true);
    setErr(null);
    try {
      const t = await createPveToken(dc.id, {
        userid: u.userid,
        tokenid: id,
        privsep,
        ...(comment ? { comment } : {}),
        ...(expire ? { expire: Math.floor(new Date(expire + "T23:59:59").getTime() / 1000) } : {}),
      });
      onCreated(t);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Mislukt");
    } finally {
      setBusy(false);
    }
  };
  return (
    <FormModal title={`API-token voor ${u.userid}`} onClose={onClose} onSubmit={() => void submit()} submitLabel="Maken" disabled={busy} error={error}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Naam" htmlFor="tk-n">
          <Input id="tk-n" value={id} onChange={(e) => (setId(e.target.value.trim()), setErr(null))} placeholder="homepage" />
        </Field>
        <Field label="Verloopt op" htmlFor="tk-x" hint="Leeg is nooit">
          <Input id="tk-x" type="date" value={expire} onChange={(e) => setExpire(e.target.value)} />
        </Field>
      </div>
      <Field label="Opmerking" htmlFor="tk-c">
        <Input id="tk-c" value={comment} onChange={(e) => setComment(e.target.value)} />
      </Field>
      <Check id="tk-p" checked={privsep} onChange={setPrivsep}>
        Eigen rechten (aanbevolen): het token kan alleen wat je het zelf geeft, niet alles wat {u.userid} kan
      </Check>
    </FormModal>
  );
}

function GroupDialog({ taken, run, pending, onClose }: { taken: string[]; run: Runner; pending: boolean; onClose: () => void }) {
  const [id, setId] = useState("");
  const [comment, setComment] = useState("");
  const error = !/^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/.test(id) ? "Geef de groep een naam." : taken.includes(id) ? "Die groep bestaat al." : null;
  return (
    <FormModal
      title="Groep toevoegen"
      onClose={onClose}
      onSubmit={() => void run({ method: "create", path: "/access/groups", params: { groupid: id, ...(comment ? { comment } : {}) } }).then((ok) => ok && onClose())}
      submitLabel="Opslaan"
      disabled={pending}
      error={error}
    >
      <Field label="Naam" htmlFor="g-n">
        <Input id="g-n" value={id} onChange={(e) => setId(e.target.value.trim())} />
      </Field>
      <Field label="Opmerking" htmlFor="g-c">
        <Input id="g-c" value={comment} onChange={(e) => setComment(e.target.value)} />
      </Field>
    </FormModal>
  );
}

const commonRoles = ["PVEAuditor", "PVEVMUser", "PVEVMAdmin", "PVEDatastoreUser", "PVEAdmin", "Administrator"];

function AclDialog({ users, groups, roles, run, pending, onClose }: { users: PveUser[]; groups: string[]; roles: PveRole[]; run: Runner; pending: boolean; onClose: () => void }) {
  const tokens = users.flatMap((u) => (u.tokens ?? []).map((t) => `${u.userid}!${t.tokenid}`));
  const [kind, setKind] = useState<"users" | "groups" | "tokens">(tokens.length ? "tokens" : "users");
  const options = kind === "users" ? users.map((u) => u.userid) : kind === "groups" ? groups : tokens;
  const [who, setWho] = useState("");
  const target = options.includes(who) ? who : (options[0] ?? "");
  const [path, setPath] = useState("/");
  const roleIds = roles.length ? roles.map((r) => r.roleid).sort((a, b) => Number(commonRoles.includes(b)) - Number(commonRoles.includes(a)) || a.localeCompare(b)) : commonRoles;
  const [role, setRole] = useState("PVEAuditor");
  const [propagate, setPropagate] = useState(true);
  const error = !target ? "Er is niemand van deze soort." : !/^\/[A-Za-z0-9/_.-]*$/.test(path) ? "Een pad begint met / (bijvoorbeeld /, /vms/101 of /storage/local)." : null;
  return (
    <FormModal
      title="Recht geven"
      onClose={onClose}
      onSubmit={() => void run({ method: "set", path: "/access/acl", params: { path, roles: role, [kind]: target, propagate: propagate ? "1" : "0" } }).then((ok) => ok && onClose())}
      submitLabel="Geven"
      disabled={pending}
      error={error}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Aan" htmlFor="acl-k">
          <Select id="acl-k" value={kind} onChange={(e) => setKind(e.target.value as typeof kind)}>
            <option value="users">Gebruiker</option>
            <option value="groups">Groep</option>
            <option value="tokens">API-token</option>
          </Select>
        </Field>
        <Field label="Wie" htmlFor="acl-w">
          <Select id="acl-w" value={target} onChange={(e) => setWho(e.target.value)}>
            {options.map((o) => (
              <option key={o}>{o}</option>
            ))}
          </Select>
        </Field>
        <Field label="Op" htmlFor="acl-p" hint="/ is alles; /vms/101 is één gast; /storage/local één opslag">
          <Input id="acl-p" value={path} onChange={(e) => setPath(e.target.value.trim())} className="font-mono" />
        </Field>
        <Field label="Rol" htmlFor="acl-r" hint="PVEAuditor kan alleen lezen">
          <Select id="acl-r" value={role} onChange={(e) => setRole(e.target.value)}>
            {roleIds.map((r) => (
              <option key={r}>{r}</option>
            ))}
          </Select>
        </Field>
      </div>
      <Check id="acl-pr" checked={propagate} onChange={setPropagate}>
        Ook alles eronder
      </Check>
    </FormModal>
  );
}
