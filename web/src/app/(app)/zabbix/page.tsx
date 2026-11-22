"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { ago } from "@/components/inventory/bits";
import {
  Alert,
  Badge,
  Button,
  Card,
  Field,
  Input,
  PageHeader,
} from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useIsAdmin } from "@/lib/inventory";
import {
  useSaveZabbixAgentSettings,
  useTestZabbixAgentAPI,
  useZabbixAgentNodes,
  useZabbixAgents,
  zabbixAgentState,
  type ZabbixAgentNode,
  type ZabbixAgentNodesInput,
  type ZabbixAgentSettings,
} from "@/lib/zabbixAgents";

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : "Er ging iets mis.";
}

export default function ZabbixAgentsPage() {
  const q = useZabbixAgents();
  const isAdmin = useIsAdmin();
  return (
    <div className="space-y-6">
      <PageHeader
        title="Zabbix-agents"
        description="ClusterForge zet zabbix-agent2 op de nodes, geeft elke node een eigen PSK en zet die sleutel meteen ook bij de host in Zabbix. Waar Docker draait, komt de Docker-plugin erbij."
      />
      {q.isError && <Alert>{errText(q.error)}</Alert>}
      {q.data && (
        <>
          {!q.data.settings.available && (
            <Alert>
              De server heeft geen masterkey (CF_MASTER_KEY), dus hij kan geen
              PSK&apos;s en API-token bewaren.
            </Alert>
          )}
          <SettingsCard settings={q.data.settings} isAdmin={isAdmin} />
          <NodesCard
            nodes={q.data.nodes}
            settings={q.data.settings}
            isAdmin={isAdmin}
          />
        </>
      )}
    </div>
  );
}

function SettingsCard({
  settings,
  isAdmin,
}: {
  settings: ZabbixAgentSettings;
  isAdmin: boolean;
}) {
  const save = useSaveZabbixAgentSettings();
  const test = useTestZabbixAgentAPI();
  const initial = useMemo(
    () => ({
      server: settings.server,
      server_active: settings.server_active,
      version: settings.version,
      host_metadata: settings.host_metadata,
      api_url: settings.api_url,
      host_group: settings.host_group,
      templates: settings.templates.join(", "),
      docker_template: settings.docker_template,
      proxy: settings.proxy,
      auto_apply: settings.auto_apply,
      auto_prod: settings.auto_prod,
    }),
    [settings],
  );
  const [form, setForm] = useState(initial);
  const [dirty, setDirty] = useState(false);
  const [token, setToken] = useState("");
  const f = dirty ? form : initial;
  const set = <K extends keyof typeof initial>(
    k: K,
    v: (typeof initial)[K],
  ) => {
    setForm({ ...f, [k]: v });
    setDirty(true);
  };

  const submit = () =>
    save.mutate(
      {
        ...f,
        templates: f.templates
          .split(",")
          .map((t) => t.trim())
          .filter(Boolean),
        ...(token.trim() ? { token: token.trim() } : {}),
      },
      {
        onSuccess: () => {
          setDirty(false);
          setToken("");
        },
      },
    );

  const text = (
    k:
      | "server"
      | "server_active"
      | "version"
      | "host_metadata"
      | "api_url"
      | "host_group"
      | "templates"
      | "docker_template"
      | "proxy",
    label: string,
    hint: string,
    placeholder = "",
  ) => (
    <Field label={label} htmlFor={`zbx-${k}`} hint={hint}>
      <Input
        id={`zbx-${k}`}
        value={f[k]}
        placeholder={placeholder}
        disabled={!isAdmin}
        onChange={(e) => set(k, e.target.value)}
      />
    </Field>
  );

  return (
    <Card title="Instellingen">
      <div className="space-y-4 text-sm">
        <div className="grid gap-3 sm:grid-cols-2">
          {text(
            "server",
            "Server",
            "Wie de agent mag bevragen (Server=): je proxy, of de Zabbix-servers.",
            "192.168.199.14",
          )}
          {text(
            "server_active",
            "ServerActive",
            "Waar de agent actieve checks ophaalt. Leeg is hetzelfde als Server.",
            "192.168.199.14",
          )}
          {text(
            "api_url",
            "Zabbix API",
            "Adres van de webinterface; /api_jsonrpc.php komt er vanzelf achter.",
            "http://192.168.199.50/api_jsonrpc.php",
          )}
          <Field
            label="API-token"
            htmlFor="zbx-token"
            hint={
              settings.token_set
                ? "Er is een token bewaard. Leeg laten houdt het."
                : "Zabbix: Users → API tokens, van een gebruiker die hosts mag aanpassen."
            }
          >
            <Input
              id="zbx-token"
              type="password"
              autoComplete="off"
              value={token}
              placeholder={settings.token_set ? "••••••••" : ""}
              disabled={!isAdmin}
              onChange={(e) => {
                setToken(e.target.value);
                setDirty(true);
              }}
            />
          </Field>
          {text("version", "Versie", "De repository van Zabbix, zoals 8.0.")}
          {text(
            "host_metadata",
            "HostMetadata",
            "Zoals in je autoregistratie.",
          )}
          {text(
            "host_group",
            "Groep voor nieuwe hosts",
            "Bestaat de host nog niet in Zabbix, dan maakt ClusterForge hem aan in deze groep.",
          )}
          {text(
            "proxy",
            "Proxy voor nieuwe hosts",
            "Naam van de proxy in Zabbix; leeg is de server.",
            "zabbix-proxy-01",
          )}
          {text(
            "templates",
            "Templates voor nieuwe hosts",
            "Gescheiden door komma's.",
          )}
          {text(
            "docker_template",
            "Template voor nodes met Docker",
            "Komt erbij als de node Docker heeft.",
          )}
        </div>
        <label className="flex items-start gap-2">
          <input
            type="checkbox"
            className="mt-0.5"
            checked={f.auto_apply}
            disabled={!isAdmin}
            onChange={(e) => set("auto_apply", e.target.checked)}
          />
          <span>
            Automatisch op elke node met een ClusterForge-agent
            <span className="block text-slate-500">
              Ook nieuwe nodes, en opnieuw als je hier iets verandert of een
              sleutel vernieuwt.
            </span>
          </span>
        </label>
        <label className="flex items-start gap-2">
          <input
            type="checkbox"
            className="mt-0.5"
            checked={f.auto_prod}
            disabled={!isAdmin}
            onChange={(e) => set("auto_prod", e.target.checked)}
          />
          <span>
            Ook productie
            <span className="block text-slate-500">
              Ook nodes van prod-clusters krijgen de agent vanzelf.
            </span>
          </span>
        </label>
        {isAdmin && (
          <div className="flex flex-wrap items-center gap-2">
            <Button disabled={!dirty || save.isPending} onClick={submit}>
              Bewaren
            </Button>
            {dirty && (
              <Button
                variant="secondary"
                onClick={() => {
                  setDirty(false);
                  setToken("");
                }}
              >
                Annuleren
              </Button>
            )}
            <Button
              variant="secondary"
              disabled={test.isPending || dirty}
              onClick={() => test.mutate()}
            >
              Zabbix API testen
            </Button>
          </div>
        )}
        {save.isError && <Alert>{errText(save.error)}</Alert>}
        {test.data && (
          <Alert kind={test.data.ok ? "success" : "error"}>
            {test.data.message}
          </Alert>
        )}
        {test.isError && <Alert>{errText(test.error)}</Alert>}
      </div>
    </Card>
  );
}

function NodesCard({
  nodes,
  settings,
  isAdmin,
}: {
  nodes: ZabbixAgentNode[];
  settings: ZabbixAgentSettings;
  isAdmin: boolean;
}) {
  const act = useZabbixAgentNodes();
  const [selected, setSelected] = useState<string[]>([]);
  const groups = useMemo(() => {
    const m = new Map<string, ZabbixAgentNode[]>();
    for (const n of nodes) {
      const k = n.cluster_name || "Zonder cluster";
      m.set(k, [...(m.get(k) ?? []), n]);
    }
    return [...m.entries()];
  }, [nodes]);
  const ready =
    settings.server !== "" && settings.api_url !== "" && settings.token_set;
  const count = (label: string) =>
    nodes.filter((n) => zabbixAgentState(n).label === label).length;

  const run = (action: ZabbixAgentNodesInput["action"], all = false) => {
    act.mutate(
      {
        action,
        ...(all ? { all: true } : { node_ids: selected }),
      },
      { onSuccess: () => setSelected([]) },
    );
  };

  return (
    <Card title="Nodes">
      <div className="space-y-4 text-sm">
        <p>
          {count("In orde")} in orde,{" "}
          {count("Nog te installeren") + count("Nog toe te passen")} nog toe te
          passen, {count("Mislukt")} mislukt,{" "}
          {count("Niet beheerd") + count("Geen agent")} niet beheerd.
        </p>
        {!ready && (
          <Alert kind="info">
            Vul eerst Server, de Zabbix API en het API-token in. Daarna kun je
            toepassen.
          </Alert>
        )}
        <div className="max-h-[32rem] overflow-auto rounded-md border border-slate-200 dark:border-slate-800">
          <table className="w-full text-left text-xs">
            <thead className="sticky top-0 bg-slate-50 text-slate-500 dark:bg-slate-900">
              <tr>
                <th className="w-8 p-2" />
                <th className="p-2">Node</th>
                <th className="p-2">Toestand</th>
                <th className="hidden p-2 md:table-cell">PSK</th>
                <th className="hidden p-2 md:table-cell">Zabbix</th>
              </tr>
            </thead>
            <tbody>
              {groups.map(([name, list]) => [
                <tr
                  key={`g-${name}`}
                  className="bg-slate-50/60 dark:bg-slate-900/60"
                >
                  <td colSpan={5} className="p-2 font-medium">
                    {name}
                    {list[0].cluster_environment === "prod" && (
                      <span className="ml-2">
                        <Badge tone="red">prod</Badge>
                      </span>
                    )}
                  </td>
                </tr>,
                ...list.map((n) => {
                  const st = zabbixAgentState(n);
                  return (
                    <tr
                      key={n.node_id}
                      className="border-t border-slate-100 align-top dark:border-slate-800"
                    >
                      <td className="p-2">
                        <input
                          type="checkbox"
                          aria-label={`${n.hostname} kiezen`}
                          disabled={!isAdmin}
                          checked={selected.includes(n.node_id)}
                          onChange={(e) =>
                            setSelected((s) =>
                              e.target.checked
                                ? [...s, n.node_id]
                                : s.filter((x) => x !== n.node_id),
                            )
                          }
                        />
                      </td>
                      <td className="p-2">
                        <Link
                          href={`/nodes/detail?id=${n.node_id}`}
                          className="font-medium hover:underline"
                        >
                          {n.hostname}
                        </Link>
                        <span className="block text-slate-500">
                          {n.ip || "geen IP"}
                          {n.docker && " · Docker"}
                          {!n.online && " · niet verbonden"}
                        </span>
                      </td>
                      <td className="p-2">
                        <Badge tone={st.tone}>{st.label}</Badge>
                        {n.agent_service && n.agent_service !== "active" && (
                          <span className="mt-1 block text-red-600 dark:text-red-400">
                            zabbix-agent2: {n.agent_service}
                          </span>
                        )}
                        {n.apply_error && n.pending && (
                          <span className="mt-1 block text-slate-500">
                            {n.apply_error}
                          </span>
                        )}
                        {n.apply_job_id && (
                          <Link
                            href={`/taken/detail?id=${n.apply_job_id}`}
                            className="mt-1 block underline"
                          >
                            laatste taak
                          </Link>
                        )}
                      </td>
                      <td className="hidden p-2 md:table-cell">
                        {n.psk_identity ? (
                          <>
                            <span className="font-mono">{n.psk_identity}</span>
                            <span className="block text-slate-500">
                              sleutel van {ago(n.psk_created_at)}
                            </span>
                          </>
                        ) : (
                          <span className="text-slate-500">nog geen</span>
                        )}
                      </td>
                      <td className="hidden p-2 md:table-cell">
                        {n.zabbix_host_id ? (
                          `hostid ${n.zabbix_host_id}`
                        ) : (
                          <span className="text-slate-500">nog niet</span>
                        )}
                        {n.applied_at && (
                          <span className="block text-slate-500">
                            toegepast {ago(n.applied_at)}
                          </span>
                        )}
                      </td>
                    </tr>
                  );
                }),
              ])}
              {nodes.length === 0 && (
                <tr>
                  <td colSpan={5} className="p-3 text-slate-500">
                    Nog geen nodes met een agent.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
        {isAdmin && (
          <div className="space-y-3">
            <div className="flex flex-wrap gap-2">
              {selected.length > 0 ? (
                <>
                  <Button
                    disabled={act.isPending || !ready}
                    onClick={() => run("apply")}
                  >
                    Toepassen ({selected.length})
                  </Button>
                  <Button
                    variant="secondary"
                    disabled={act.isPending || !ready}
                    onClick={() => run("rotate")}
                  >
                    Nieuwe sleutel
                  </Button>
                  <Button
                    variant="secondary"
                    disabled={act.isPending}
                    onClick={() => run("manage")}
                  >
                    Beheren
                  </Button>
                  <Button
                    variant="secondary"
                    disabled={act.isPending}
                    onClick={() => run("unmanage")}
                  >
                    Niet meer beheren
                  </Button>
                </>
              ) : (
                <Button
                  disabled={act.isPending || !ready}
                  onClick={() => run("apply", true)}
                >
                  Op alle nodes toepassen
                </Button>
              )}
            </div>
            <p className="text-slate-500">
              Toepassen installeert zabbix-agent2, zet de config en de sleutel
              op de node en zet de sleutel meteen daarna bij de host in Zabbix
              (of maakt de host aan). De host is daardoor hooguit enkele
              seconden onbereikbaar. Niet meer beheren laat zabbix-agent2 staan.
            </p>
            {act.isError && <Alert>{errText(act.error)}</Alert>}
            {act.data && act.data.skipped.length > 0 && (
              <Alert>Overgeslagen: {act.data.skipped.join("; ")}</Alert>
            )}
            {act.data && act.data.jobs.length > 0 && (
              <Alert kind="success">
                {act.data.jobs.length}{" "}
                {act.data.jobs.length === 1 ? "taak" : "taken"} gestart. Volg ze
                bij{" "}
                <Link href="/taken" className="underline">
                  Taken
                </Link>
                .
              </Alert>
            )}
          </div>
        )}
      </div>
    </Card>
  );
}
