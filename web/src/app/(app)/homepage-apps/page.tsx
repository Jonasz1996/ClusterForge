"use client";

import { useMemo, useState } from "react";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import {
  Alert,
  Badge,
  Button,
  Card,
  Field,
  Input,
  PageHeader,
  Select,
} from "@/components/ui";
import { useMe } from "@/lib/auth";
import {
  useDecideDoubt,
  useHomeApps,
  useSaveOverride,
  type HomeApp,
  type HomeAppOverride,
  type HomeAppsAdmin,
} from "@/lib/homeapps";

const tone = {
  ok: "green",
  warn: "amber",
  down: "red",
  unknown: "slate",
} as const;

const foundBy: Record<string, string> = {
  catalogus: "herkend",
  template: "template",
  npm: "via NPM",
  gekozen: "gekozen",
  weg: "weg?",
};

// Homepage-apps: ClusterForge herkent de apps zelf en geeft ze als tegels aan
// de homepage. Hier alleen bijsturen: verbergen, hernoemen, groep, velden.
export default function HomepageAppsPage() {
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const q = useHomeApps();
  const [picked, setOpen] = useState<string | null>(null);
  const [filter, setFilter] = useState("");
  // ?app=<slug> (de link uit de homepage-API) opent die app meteen.
  const [wanted] = useState(() =>
    typeof window === "undefined"
      ? null
      : new URLSearchParams(window.location.search).get("app"),
  );
  const open =
    picked ?? q.data?.apps.find((x) => x.slug === wanted)?.key ?? null;

  const groups = useMemo(() => {
    const f = filter.trim().toLowerCase();
    const out = new Map<string, HomeApp[]>();
    for (const a of q.data?.apps ?? []) {
      const text =
        `${a.name} ${a.slug} ${a.cluster?.name ?? ""} ${a.instances.map((i) => i.node).join(" ")}`.toLowerCase();
      if (f && !text.includes(f)) continue;
      out.set(a.group, [...(out.get(a.group) ?? []), a]);
    }
    return [...out.entries()];
  }, [q.data, filter]);

  const d = q.data;
  const counts = useMemo(() => {
    const c = { ok: 0, warn: 0, down: 0, hidden: 0 };
    for (const a of d?.apps ?? []) {
      if (a.hidden) c.hidden++;
      else if (a.status === "ok") c.ok++;
      else if (a.status === "warn") c.warn++;
      else if (a.status === "down") c.down++;
    }
    return c;
  }, [d]);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Homepage-apps"
        description="ClusterForge herkent je apps zelf uit de diensten op elke node en geeft ze als tegels aan je homepage. Een nieuwe app verschijnt vanzelf, een verdwenen app staat nog 24 uur als ‘weg?’ en verdwijnt dan. Hier stuur je alleen bij."
      />
      {q.error && <Alert>{(q.error as Error).message}</Alert>}
      {d && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <Badge tone="green">{counts.ok} gezond</Badge>
          {counts.warn > 0 && <Badge tone="amber">{counts.warn} let op</Badge>}
          {counts.down > 0 && <Badge tone="red">{counts.down} down</Badge>}
          {counts.hidden > 0 && <Badge>{counts.hidden} verborgen</Badge>}
          <span className="text-slate-500">
            API: <code className="text-xs">{d.base_path}/apps</code> en{" "}
            <code className="text-xs">{d.base_path}/homepage/layout</code>{" "}
            (tokens in Instellingen → Dashboard-API)
          </span>
          <Input
            className="ml-auto w-60"
            placeholder="Zoeken…"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
        </div>
      )}
      {d && d.doubts.length > 0 && <Doubts data={d} admin={admin} />}
      {groups.map(([group, apps]) => (
        <Card key={group} title={group}>
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>App</th>
                  <th className={thClass}>Status</th>
                  <th className={thClass}>Waar</th>
                  <th className={thClass}>Adres</th>
                  <th className={thClass}>Gevonden</th>
                  <th className={thClass}></th>
                </tr>
              </thead>
              <tbody>
                {apps.map((a) => (
                  <AppRow
                    key={a.key}
                    app={a}
                    data={d!}
                    admin={admin}
                    open={open === a.key}
                    onToggle={() => setOpen(open === a.key ? null : a.key)}
                  />
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      ))}
      {d && d.apps.length === 0 && (
        <Alert kind="info">
          Nog geen apps gevonden. De agents brengen hun diensten elke 5 minuten
          in kaart (Publicatie → Diensten); daarna verschijnen de apps hier
          vanzelf.
        </Alert>
      )}
    </div>
  );
}

function AppRow({
  app,
  data,
  admin,
  open,
  onToggle,
}: {
  app: HomeApp;
  data: HomeAppsAdmin;
  admin: boolean;
  open: boolean;
  onToggle: () => void;
}) {
  const where =
    app.cluster?.name ?? app.instances.map((i) => i.node).join(", ");
  return (
    <>
      <tr className={app.hidden ? "opacity-50" : undefined}>
        <td className={tdClass}>
          <div className="font-medium">{app.name}</div>
          <div className="text-xs text-slate-500">
            {app.slug}
            {app.version ? ` · ${app.version}` : ""}
          </div>
        </td>
        <td className={tdClass}>
          <Badge tone={tone[app.status]}>
            {app.gone ? "weg?" : app.status_label}
          </Badge>
          {app.hidden && (
            <span className="ml-1">
              <Badge>verborgen</Badge>
            </span>
          )}
          {app.reason && (
            <div className="text-xs text-slate-500">{app.reason}</div>
          )}
        </td>
        <td className={tdClass}>
          {where}
          {app.active_node && app.cluster && (
            <div className="text-xs text-slate-500">
              actief op {app.active_node}
            </div>
          )}
        </td>
        <td className={tdClass}>
          {app.url ? (
            <a
              className="text-brand-700 hover:underline dark:text-sky-300"
              href={app.url}
              target="_blank"
              rel="noreferrer"
            >
              {app.url.replace(/^https?:\/\//, "")}
            </a>
          ) : (
            "—"
          )}
        </td>
        <td className={tdClass}>
          <Badge tone={app.found_by === "weg" ? "amber" : "slate"}>
            {foundBy[app.found_by] ?? app.found_by}
          </Badge>
          {Object.keys(app.override).length > 0 && (
            <span className="ml-1">
              <Badge tone="blue">bijgestuurd</Badge>
            </span>
          )}
        </td>
        <td className={tdClass}>
          <Button variant="ghost" onClick={onToggle}>
            {open ? "Sluiten" : admin ? "Bijsturen" : "Details"}
          </Button>
        </td>
      </tr>
      {open && (
        <tr>
          <td className={tdClass} colSpan={6}>
            <AppEditor app={app} data={data} admin={admin} />
          </td>
        </tr>
      )}
    </>
  );
}

function AppEditor({
  app,
  data,
  admin,
}: {
  app: HomeApp;
  data: HomeAppsAdmin;
  admin: boolean;
}) {
  const save = useSaveOverride();
  const o = app.override;
  const [name, setName] = useState(o.name ?? "");
  const [group, setGroup] = useState(o.group ?? "");
  const [icon, setIcon] = useState(o.icon ?? "");
  const [url, setUrl] = useState(o.url ?? "");
  const [sort, setSort] = useState(String(o.sort ?? 0));
  const [fields, setFields] = useState<string[]>(o.fields ?? []);
  const values = data.values[app.key] ?? {};
  const shown = fields.length > 0 ? fields : data.default_fields;
  const available = Object.keys(data.fields).filter(
    (k) => !shown.includes(k) && values[k] !== undefined,
  );

  const submit = (patch: HomeAppOverride) => {
    const next: HomeAppOverride = {
      hidden: o.hidden,
      name: name || undefined,
      group: group || undefined,
      icon: icon || undefined,
      url: url || undefined,
      sort: Number(sort) || undefined,
      fields: fields.length > 0 ? fields : undefined,
      ...patch,
    };
    save.mutate({ key: app.key, override: next });
  };
  const move = (k: string, d: number) => {
    const list = [...shown];
    const i = list.indexOf(k);
    const j = i + d;
    if (j < 0 || j >= list.length) return;
    [list[i], list[j]] = [list[j], list[i]];
    setFields(list);
  };

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <div className="space-y-3 text-sm">
        <div className="font-medium">Waar het draait</div>
        <ul className="space-y-1">
          {app.instances.map((i) => (
            <li key={i.node_id + String(i.port)}>
              <Badge tone={i.online ? "green" : "red"}>
                {i.online ? "online" : "offline"}
              </Badge>{" "}
              {i.node}
              {i.ip ? ` (${i.ip})` : ""} · poort {i.port}
              {i.http_status ? ` · HTTP ${i.http_status}` : ""}
              {i.container ? ` · container ${i.container}` : ""}
              {i.unit ? ` · unit ${i.unit}` : ""}
            </li>
          ))}
          {app.instances.length === 0 && (
            <li className="text-slate-500">Niet meer gevonden.</li>
          )}
        </ul>
        {app.problems.length > 0 && (
          <ul className="list-disc pl-5 text-amber-700 dark:text-amber-300">
            {app.problems.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        )}
        <div className="font-medium">Op de tegel ({shown.length}/8)</div>
        <ul className="space-y-1">
          {shown.map((k) => (
            <li key={k} className="flex items-center gap-2">
              <span className="w-40">{data.fields[k]?.label ?? k}</span>
              <code className="text-xs">{String(values[k] ?? "—")}</code>
              {admin && (
                <span className="ml-auto flex gap-1">
                  <Button
                    variant="ghost"
                    onClick={() => move(k, -1)}
                    aria-label="Hoger"
                  >
                    ↑
                  </Button>
                  <Button
                    variant="ghost"
                    onClick={() => move(k, 1)}
                    aria-label="Lager"
                  >
                    ↓
                  </Button>
                  <Button
                    variant="ghost"
                    onClick={() => setFields(shown.filter((x) => x !== k))}
                  >
                    Weg
                  </Button>
                </span>
              )}
            </li>
          ))}
        </ul>
        {admin && available.length > 0 && shown.length < 8 && (
          <Select
            value=""
            onChange={(e) =>
              e.target.value && setFields([...shown, e.target.value])
            }
          >
            <option value="">Veld toevoegen…</option>
            {available.map((k) => (
              <option key={k} value={k}>
                {data.fields[k].label} ({String(values[k])})
              </option>
            ))}
          </Select>
        )}
      </div>
      {admin ? (
        <div className="space-y-3">
          <Field
            label="Naam"
            htmlFor={`n-${app.key}`}
            hint="Leeg = automatisch"
          >
            <Input
              id={`n-${app.key}`}
              value={name}
              placeholder={app.name}
              onChange={(e) => setName(e.target.value)}
            />
          </Field>
          <Field label="Groep" htmlFor={`g-${app.key}`}>
            <Select
              id={`g-${app.key}`}
              value={group}
              onChange={(e) => setGroup(e.target.value)}
            >
              <option value="">Automatisch ({app.group})</option>
              {data.groups.map((g) => (
                <option key={g} value={g}>
                  {g}
                </option>
              ))}
            </Select>
          </Field>
          <Field
            label="Icoon"
            htmlFor={`i-${app.key}`}
            hint="Naam uit dashboard-icons, zoals plex.png, of mdi-…"
          >
            <Input
              id={`i-${app.key}`}
              value={icon}
              placeholder={app.icon}
              onChange={(e) => setIcon(e.target.value)}
            />
          </Field>
          <Field
            label="Adres"
            htmlFor={`u-${app.key}`}
            hint="Leeg = het NPM-adres of het interne adres"
          >
            <Input
              id={`u-${app.key}`}
              value={url}
              placeholder={app.url}
              onChange={(e) => setUrl(e.target.value)}
            />
          </Field>
          <Field label="Volgorde in de groep" htmlFor={`s-${app.key}`}>
            <Input
              id={`s-${app.key}`}
              type="number"
              value={sort}
              onChange={(e) => setSort(e.target.value)}
            />
          </Field>
          {save.error && <Alert>{(save.error as Error).message}</Alert>}
          <div className="flex flex-wrap gap-2">
            <Button onClick={() => submit({})} disabled={save.isPending}>
              Bewaren
            </Button>
            <Button
              variant="secondary"
              onClick={() => submit({ hidden: !o.hidden })}
              disabled={save.isPending}
            >
              {o.hidden ? "Weer tonen" : "Verbergen op de homepage"}
            </Button>
            {Object.keys(o).length > 0 && (
              <Button
                variant="ghost"
                onClick={() => save.mutate({ key: app.key, override: {} })}
                disabled={save.isPending}
              >
                Alles automatisch
              </Button>
            )}
          </div>
        </div>
      ) : (
        <p className="text-sm text-slate-500">
          Alleen een beheerder kan bijsturen.
        </p>
      )}
    </div>
  );
}

function Doubts({ data, admin }: { data: HomeAppsAdmin; admin: boolean }) {
  const decide = useDecideDoubt();
  return (
    <Card title={`Twijfel (${data.doubts.length})`}>
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        Webdiensten die ClusterForge niet zeker als app herkent en die niet in
        NPM gepubliceerd zijn. Maak er met één klik een tegel van, of negeer ze.
      </p>
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Dienst</th>
              <th className={thClass}>Waar</th>
              <th className={thClass}>Antwoord</th>
              <th className={thClass}></th>
            </tr>
          </thead>
          <tbody>
            {data.doubts.map((x) => (
              <tr key={x.key}>
                <td className={tdClass}>
                  <div className="font-medium">{x.name}</div>
                  <div className="text-xs text-slate-500">
                    {x.image || x.unit}
                  </div>
                </td>
                <td className={tdClass}>
                  {x.cluster || x.node} · {x.kind} {x.port}
                </td>
                <td className={tdClass}>{x.title ?? "—"}</td>
                <td className={tdClass}>
                  {admin && (
                    <span className="flex gap-1">
                      <Button
                        variant="secondary"
                        disabled={decide.isPending}
                        onClick={() =>
                          decide.mutate({
                            key: x.key,
                            choice: "app",
                            name: x.title || x.name,
                          })
                        }
                      >
                        Tegel maken
                      </Button>
                      <Button
                        variant="ghost"
                        disabled={decide.isPending}
                        onClick={() =>
                          decide.mutate({ key: x.key, choice: "ignore" })
                        }
                      >
                        Negeren
                      </Button>
                    </span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}
