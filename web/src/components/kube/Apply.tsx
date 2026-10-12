"use client";

import { useState, type ReactNode } from "react";
import { Diff } from "@/components/gitops/bits";
import { useConfirm } from "@/components/kube/Actions";
import { CodeBlock, KubeError, Learn, ObjectLink, sentence } from "@/components/kube/Kube";
import { Alert, Badge, Button, Input, Label, Select, Textarea, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import {
  kubectl,
  refOf,
  systemNamespaces,
  useKubeApply,
  useKubeLearning,
  type KubeApplyObject,
  type KubeApplyResult,
  type KubeOverview,
} from "@/lib/kube";

// Een app zet je op met een formulier, met een Helm-chart of met eigen
// YAML. Alle drie maken YAML, en die gaat langs dezelfde weg: Controleren
// is een dry-run met het verschil per object, en pas Toepassen verandert
// het cluster.

export type ApplyMode = "app" | "helm" | "yaml";

export const applyModes: { id: ApplyMode; label: string; help: string }[] = [
  { id: "app", label: "Nieuwe app", help: "Een image laten draaien, met een adres in het cluster en op het web." },
  { id: "helm", label: "Helm-chart", help: "Een kant-en-klaar pakket uit een Helm-repository installeren." },
  { id: "yaml", label: "Eigen YAML", help: "YAML plakken zoals voor kubectl apply, of met een voorbeeld beginnen." },
];

export function ApplyTab({
  id,
  o,
  mode,
  onMode,
}: {
  id: string;
  o: KubeOverview;
  mode: ApplyMode;
  onMode: (m: ApplyMode) => void;
}) {
  return (
    <div className="space-y-4">
      <Learn
        commands={[
          kubectl("apply --server-side --dry-run=server -f app.yaml"),
          kubectl("diff --server-side -f app.yaml"),
          kubectl("apply --server-side -f app.yaml"),
        ]}
      >
        <p>
          In Kubernetes beschrijf je in YAML wat er moet draaien, en Kubernetes zorgt dat het zo wordt. Elk stuk YAML is een
          object, zoals een Deployment of een Service; <code>---</code> scheidt ze.
        </p>
        <p>
          <strong>Controleren</strong> stuurt de YAML naar de API als dry-run: Kubernetes keurt alles zoals bij echt toepassen,
          maar bewaart niets. Je ziet per object of het nieuw is en wat er verandert. Pas <strong>Toepassen</strong> verandert het
          cluster. Nog eens toepassen zonder wijziging doet niets: je zegt hoe het moet zijn, niet wat er moet gebeuren.
        </p>
        <p>Met kubectl zet je de YAML in een bestand, zoals app.yaml, en doe je hetzelfde:</p>
      </Learn>

      <div className="grid gap-2 sm:grid-cols-3" role="radiogroup" aria-label="Hoe wil je iets uitrollen?">
        {applyModes.map((m) => (
          <button
            key={m.id}
            type="button"
            role="radio"
            aria-checked={mode === m.id}
            onClick={() => onMode(m.id)}
            className={cx(
              "rounded-lg border px-4 py-3 text-left text-sm",
              mode === m.id
                ? "border-brand-500 bg-brand-50 ring-1 ring-brand-500 dark:border-sky-500 dark:bg-slate-800 dark:ring-sky-500"
                : "border-slate-200 bg-white hover:border-slate-300 dark:border-slate-800 dark:bg-slate-900 dark:hover:border-slate-700",
            )}
          >
            <span className="block font-medium">{m.label}</span>
            <span className="mt-0.5 block text-xs text-slate-500">{m.help}</span>
          </button>
        ))}
      </div>

      {/* Verborgen in plaats van weg: wat je invulde, blijft als je wisselt. */}
      <div hidden={mode !== "app"}>
        <AppForm id={id} o={o} />
      </div>
      <div hidden={mode !== "helm"}>
        <HelmForm id={id} o={o} />
      </div>
      <div hidden={mode !== "yaml"}>
        <YamlForm id={id} o={o} />
      </div>
    </div>
  );
}

// q zet tekst in YAML: zonder aanhalingstekens als dat veilig is, anders
// zoals in JSON, wat ook geldige YAML is.
function q(s: string) {
  const plain = /^[A-Za-z0-9][A-Za-z0-9._/:@-]*$/.test(s) && !s.endsWith(":");
  const special = /^(true|false|yes|no|on|off|y|n|null|~|0x[0-9a-f]+|0o[0-7]+|[-+.0-9e_:]+)$/i.test(s);
  return plain && !special ? s : JSON.stringify(s);
}

// webHost is het standaard webadres: een naam onder nip.io met het VIP
// erin, die zonder eigen DNS naar het VIP wijst.
function webHost(name: string, vip: string) {
  return vip ? `${name}.${vip}.nip.io` : `${name}.example.com`;
}

// Block is één object van de YAML, met uitleg voor de leermodus.
type Block = { title: string; help: ReactNode; yaml: string };

function joinBlocks(blocks: Block[]) {
  return blocks.map((b) => b.yaml).join("---\n");
}

// dutch vertaalt de zinnen van de API die een beginner het vaakst ziet.
function dutch(msg: string) {
  if (msg === "Required value") return "Dit veld is verplicht.";
  return msg.replace(/^Invalid value: /, "Ongeldige waarde: ");
}

// fieldErrors legt de fouten van de API bij de velden van een formulier.
function fieldErrors<K extends string>(r: KubeApplyResult | undefined, at: (kind: string, field: string) => K | null) {
  const out: Partial<Record<K, string>> = {};
  for (const o of r?.objects ?? []) {
    for (const e of o.errors) {
      const k = at(o.kind, e.field);
      if (k && !out[k]) out[k] = dutch(e.message);
    }
  }
  return out;
}

function FormField({
  label,
  id,
  hint,
  error,
  children,
}: {
  label: string;
  id: string;
  hint?: ReactNode;
  error?: string;
  children: ReactNode;
}) {
  return (
    <div>
      <Label htmlFor={id}>{label}</Label>
      {children}
      {error ? (
        <p className="mt-1 text-xs text-red-700 dark:text-red-300">{error}</p>
      ) : (
        hint && <p className="mt-1 text-xs text-slate-500">{hint}</p>
      )}
    </div>
  );
}

// Een ring, geen randkleur: die zou botsen met de rand van het invulvak.
const invalidClass = "ring-1 ring-red-400 dark:ring-red-700";

// YamlView toont de YAML naast een formulier; in de leermodus per object,
// met uitleg erbij.
function YamlView({ blocks }: { blocks: Block[] }) {
  const learning = useKubeLearning();
  const all = joinBlocks(blocks);
  if (!learning.on) return <CodeBlock text={all} className="max-h-[40rem]" />;
  return (
    <div className="space-y-4">
      {blocks.map((b) => (
        <div key={b.title} className="space-y-1.5">
          <p className="text-sm font-medium">{b.title}</p>
          <p className="text-xs leading-relaxed text-slate-600 dark:text-slate-400">{b.help}</p>
          <CodeBlock text={b.yaml} />
        </div>
      ))}
      <details>
        <summary className="cursor-pointer text-xs text-slate-600 dark:text-slate-400">Alles in één, om te kopiëren</summary>
        <div className="mt-2">
          <CodeBlock text={all} />
        </div>
      </details>
    </div>
  );
}

// ---- Nieuwe app ----

type AppInput = {
  name: string;
  namespace: string;
  image: string;
  port: string;
  replicas: string;
  env: string;
  volume: boolean;
  size: string;
  mount: string;
  url: string;
  urlEdited: boolean;
};

type AppKey = "name" | "namespace" | "image" | "port" | "replicas" | "env" | "size" | "mount" | "url";

const emptyApp: AppInput = {
  name: "",
  namespace: "default",
  image: "",
  port: "",
  replicas: "1",
  env: "",
  volume: false,
  size: "1",
  mount: "/data",
  url: "",
  urlEdited: false,
};

const whoamiApp: AppInput = { ...emptyApp, name: "whoami", image: "traefik/whoami:v1.11", port: "80", replicas: "2" };

// appField zegt bij welk veld van het formulier een fout van de API hoort.
function appField(kind: string, field: string): AppKey | null {
  if (kind === "Namespace" || field === "metadata.namespace") return "namespace";
  if (field === "metadata.name") return "name";
  if (kind === "Deployment") {
    if (field === "spec.replicas") return "replicas";
    if (field.endsWith(".image")) return "image";
    if (field.includes(".ports")) return "port";
    if (field.includes(".env")) return "env";
    if (field.includes(".volumeMounts")) return "mount";
    if (field.includes("labels") || field.includes("selector") || field.endsWith(".name")) return "name";
  }
  if (kind === "Service" && field.startsWith("spec.ports")) return "port";
  if (kind === "Ingress" && field.includes(".host")) return "url";
  if (kind === "PersistentVolumeClaim" && field.startsWith("spec.resources")) return "size";
  return null;
}

// appBlocks maakt de objecten van een app. missing zijn velden die nog
// leeg zijn; problems zijn fouten die het formulier zelf al ziet.
function appBlocks(a: AppInput, vip: string, namespaces: string[]) {
  const problems: Partial<Record<AppKey, string>> = {};
  const name = a.name.trim();
  const ns = a.namespace.trim() || "default";
  const image = a.image.trim();
  const missing = !name || !image;

  const port = a.port.trim() === "" ? null : Number(a.port);
  if (port !== null && !Number.isInteger(port)) problems.port = "Een poort is een getal, zoals 80.";
  const replicas = Number(a.replicas);
  if (a.replicas.trim() === "" || !Number.isInteger(replicas) || replicas < 0 || replicas > 20) {
    problems.replicas = "Kies 0 tot 20 pods.";
  }
  const env: [string, string][] = [];
  a.env.split("\n").forEach((line, i) => {
    if (line.trim() === "") return;
    const at = line.indexOf("=");
    if (at <= 0) {
      problems.env ??= `Regel ${i + 1}: schrijf NAAM=waarde.`;
      return;
    }
    env.push([line.slice(0, at).trim(), line.slice(at + 1)]);
  });
  const size = Number(a.size);
  if (a.volume && (!Number.isFinite(size) || size <= 0)) problems.size = "Hoeveel GiB? Bijvoorbeeld 1.";
  if (a.volume && !a.mount.trim().startsWith("/")) problems.mount = "Een pad in de container, zoals /data.";
  const typed = a.url
    .trim()
    .replace(/^https?:\/\//, "")
    .replace(/\/$/, "");
  const host = port === null ? "" : a.urlEdited ? typed : name ? webHost(name, vip) : "";

  const n = q(name || "naam");
  const label = `app.kubernetes.io/name: ${n}`;
  const blocks: Block[] = [];
  if (!namespaces.includes(ns)) {
    blocks.push({
      title: `Namespace ${ns}`,
      help: `Een namespace is een map voor objecten. ${ns} bestaat nog niet, dus komt hij eerst.`,
      yaml: ["apiVersion: v1", "kind: Namespace", "metadata:", `  name: ${q(ns)}`, ""].join("\n"),
    });
  }
  const claim = q(`${name || "naam"}-data`);
  if (a.volume) {
    blocks.push({
      title: "PersistentVolumeClaim: opslag",
      help: (
        <>
          Vraagt {a.size} GiB opslag die blijft als de pod herstart of een nieuwe versie krijgt. In k3s maakt local-path er een
          map voor op de schijf van één node, en de pod draait daarna altijd op die node.
        </>
      ),
      yaml: [
        "apiVersion: v1",
        "kind: PersistentVolumeClaim",
        "metadata:",
        `  name: ${claim}`,
        `  namespace: ${q(ns)}`,
        "spec:",
        "  accessModes:",
        "    - ReadWriteOnce",
        "  resources:",
        "    requests:",
        `      storage: ${a.size.trim() || "1"}Gi`,
        "",
      ].join("\n"),
    });
  }
  blocks.push({
    title: "Deployment: de app zelf",
    help: (
      <>
        Houdt {a.replicas || "0"} {replicas === 1 ? "pod" : "pods"} met dit image draaiend. De <code>selector</code> kiest de pods
        op hun label, en de <code>template</code> zegt hoe een pod eruitziet.
        {a.volume && (
          <>
            {" "}
            <code>Recreate</code> stopt bij een nieuwe versie eerst de oude pod, zodat nooit twee pods tegelijk in het volume
            schrijven.
          </>
        )}
      </>
    ),
    yaml: [
      "apiVersion: apps/v1",
      "kind: Deployment",
      "metadata:",
      `  name: ${n}`,
      `  namespace: ${q(ns)}`,
      "  labels:",
      `    ${label}`,
      "spec:",
      `  replicas: ${Number.isInteger(replicas) ? replicas : q(a.replicas)}`,
      ...(a.volume ? ["  strategy:", "    type: Recreate"] : []),
      "  selector:",
      "    matchLabels:",
      `      ${label}`,
      "  template:",
      "    metadata:",
      "      labels:",
      `        ${label}`,
      "    spec:",
      "      containers:",
      `        - name: ${n}`,
      `          image: ${q(image || "image")}`,
      ...(port !== null
        ? [
            "          ports:",
            "            - name: http",
            `              containerPort: ${Number.isInteger(port) ? port : q(a.port)}`,
          ]
        : []),
      ...(env.length > 0
        ? [
            "          env:",
            ...env.flatMap(([k, v]) => [`            - name: ${q(k)}`, `              value: ${JSON.stringify(v)}`]),
          ]
        : []),
      ...(a.volume
        ? ["          volumeMounts:", "            - name: data", `              mountPath: ${q(a.mount.trim())}`]
        : []),
      ...(a.volume
        ? ["      volumes:", "        - name: data", "          persistentVolumeClaim:", `            claimName: ${claim}`]
        : []),
      "",
    ].join("\n"),
  });
  if (port !== null) {
    blocks.push({
      title: "Service: één adres in het cluster",
      help: (
        <>
          Andere pods bereiken de app als{" "}
          <code>
            http://{name || "naam"}.{ns}
          </code>{" "}
          op poort 80. De Service stuurt door naar de poort met de naam http ({a.port}) van de pods die klaar zijn, en verdeelt
          het verkeer.
        </>
      ),
      yaml: [
        "apiVersion: v1",
        "kind: Service",
        "metadata:",
        `  name: ${n}`,
        `  namespace: ${q(ns)}`,
        "spec:",
        "  selector:",
        `    ${label}`,
        "  ports:",
        "    - name: http",
        "      port: 80",
        "      targetPort: http",
        "",
      ].join("\n"),
    });
  }
  if (host) {
    blocks.push({
      title: "Ingress: het webadres",
      help: (
        <>
          Traefik, de webserver van k3s, stuurt verkeer voor {host} naar de Service.
          {host.endsWith(".nip.io") && (
            <> nip.io laat elke naam met een IP-adres erin naar dat adres wijzen, dus zonder eigen DNS komt {host} bij het VIP.</>
          )}
        </>
      ),
      yaml: [
        "apiVersion: networking.k8s.io/v1",
        "kind: Ingress",
        "metadata:",
        `  name: ${n}`,
        `  namespace: ${q(ns)}`,
        "spec:",
        "  ingressClassName: traefik",
        "  rules:",
        `    - host: ${q(host)}`,
        "      http:",
        "        paths:",
        "          - path: /",
        "            pathType: Prefix",
        "            backend:",
        "              service:",
        `                name: ${n}`,
        "                port:",
        "                  name: http",
        "",
      ].join("\n"),
    });
  }
  return { blocks, missing, problems, host, hasPort: port !== null };
}

function AppForm({ id, o }: { id: string; o: KubeOverview }) {
  const [a, setA] = useState<AppInput>(emptyApp);
  const set = (patch: Partial<AppInput>) => setA((old) => ({ ...old, ...patch }));
  const { blocks, missing, problems, host, hasPort } = appBlocks(a, o.vip ?? "", o.namespaces);
  const yaml = joinBlocks(blocks);
  const flow = useApplyFlow(id, yaml, "default");
  const server = fieldErrors(flow.fresh?.result, appField);
  const err = (k: AppKey) => server[k] ?? problems[k];
  const ns = a.namespace.trim() || "default";
  const blocker = missing
    ? "Vul minstens een naam en een image in."
    : Object.keys(problems).length > 0
      ? "Los eerst de fouten in het formulier op."
      : null;
  const ownNamespaces = o.namespaces.filter((x) => !systemNamespaces.includes(x));

  return (
    <div className="space-y-6">
      <div className="grid gap-6 lg:grid-cols-2">
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (!blocker) flow.check(false);
          }}
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-sm text-slate-600 dark:text-slate-400">Bij YAML zie je meteen wat Kubernetes krijgt.</p>
            <Button type="button" variant="secondary" onClick={() => setA(whoamiApp)}>
              Voorbeeld: whoami
            </Button>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <FormField label="Naam" id="app-name" error={err("name")} hint="Kleine letters, cijfers en -, zoals mijn-app.">
              <Input
                id="app-name"
                value={a.name}
                placeholder="whoami"
                autoComplete="off"
                spellCheck={false}
                className={cx(err("name") && invalidClass)}
                onChange={(e) => set({ name: e.target.value })}
              />
            </FormField>
            <FormField
              label="Namespace"
              id="app-ns"
              error={err("namespace")}
              hint={o.namespaces.includes(ns) ? "Bestaat al." : "Nieuw: Kubernetes maakt hem eerst."}
            >
              <Input
                id="app-ns"
                list="app-ns-list"
                value={a.namespace}
                autoComplete="off"
                spellCheck={false}
                className={cx(err("namespace") && invalidClass)}
                onChange={(e) => set({ namespace: e.target.value })}
              />
              <datalist id="app-ns-list">
                {ownNamespaces.map((x) => (
                  <option key={x} value={x} />
                ))}
              </datalist>
            </FormField>
          </div>
          <FormField
            label="Image"
            id="app-image"
            error={err("image")}
            hint="Van Docker Hub of een andere registry. Een vaste versie, zoals nginx:1.29, geeft elke keer hetzelfde."
          >
            <Input
              id="app-image"
              value={a.image}
              placeholder="traefik/whoami:v1.11"
              autoComplete="off"
              spellCheck={false}
              className={cx("font-mono", err("image") && invalidClass)}
              onChange={(e) => set({ image: e.target.value })}
            />
          </FormField>
          <div className="grid gap-4 sm:grid-cols-2">
            <FormField
              label="Poort"
              id="app-port"
              error={err("port")}
              hint="Waar de app in de container luistert. Leeg: geen Service en geen webadres."
            >
              <Input
                id="app-port"
                inputMode="numeric"
                value={a.port}
                placeholder="80"
                className={cx(err("port") && invalidClass)}
                onChange={(e) => set({ port: e.target.value.trim() })}
              />
            </FormField>
            <FormField
              label="Aantal pods"
              id="app-replicas"
              error={err("replicas")}
              hint="Kubernetes houdt er altijd zoveel draaiend."
            >
              <Input
                id="app-replicas"
                type="number"
                min={0}
                max={20}
                value={a.replicas}
                className={cx(err("replicas") && invalidClass)}
                onChange={(e) => set({ replicas: e.target.value })}
              />
            </FormField>
          </div>
          <FormField
            label="Omgevingsvariabelen"
            id="app-env"
            error={err("env")}
            hint="Eén per regel: NAAM=waarde. Geen wachtwoorden: die horen in een Secret."
          >
            <Textarea
              id="app-env"
              rows={3}
              value={a.env}
              placeholder={"TZ=Europe/Brussels\nLOG_LEVEL=info"}
              spellCheck={false}
              className={cx("font-mono text-xs", err("env") && invalidClass)}
              onChange={(e) => set({ env: e.target.value })}
            />
          </FormField>
          <div className="space-y-3 rounded-md border border-slate-200 p-3 dark:border-slate-800">
            <label className="flex cursor-pointer items-start gap-2 text-sm">
              <input
                type="checkbox"
                className="mt-0.5 size-4"
                checked={a.volume}
                onChange={(e) => set({ volume: e.target.checked })}
              />
              <span>
                <span className="font-medium">Opslag die blijft</span>
                <span className="block text-xs text-slate-500">
                  Voor data die een herstart moet overleven, zoals een databank. Zonder volume begint elke pod leeg.
                </span>
              </span>
            </label>
            {a.volume && (
              <div className="grid gap-4 sm:grid-cols-2">
                <FormField label="Grootte (GiB)" id="app-size" error={err("size")}>
                  <Input
                    id="app-size"
                    inputMode="decimal"
                    value={a.size}
                    className={cx(err("size") && invalidClass)}
                    onChange={(e) => set({ size: e.target.value.trim() })}
                  />
                </FormField>
                <FormField label="Pad in de container" id="app-mount" error={err("mount")}>
                  <Input
                    id="app-mount"
                    value={a.mount}
                    spellCheck={false}
                    className={cx("font-mono", err("mount") && invalidClass)}
                    onChange={(e) => set({ mount: e.target.value })}
                  />
                </FormField>
              </div>
            )}
          </div>
          <FormField
            label="Webadres"
            id="app-url"
            error={err("url")}
            hint={
              !hasPort
                ? "Een webadres heeft een poort nodig."
                : o.vip
                  ? `Standaard naam.${o.vip}.nip.io: die naam wijst zonder eigen DNS naar het VIP, als je pc internet heeft. Leeg: geen webadres.`
                  : "Dit cluster heeft geen VIP; kies een naam die in je DNS naar een node wijst. Leeg: geen webadres."
            }
          >
            <Input
              id="app-url"
              value={hasPort ? (a.urlEdited ? a.url : host) : ""}
              disabled={!hasPort}
              placeholder={hasPort ? "geen webadres" : ""}
              autoComplete="off"
              spellCheck={false}
              className={cx("font-mono", err("url") && invalidClass)}
              onChange={(e) => set({ url: e.target.value, urlEdited: true })}
            />
          </FormField>
        </form>
        <div className="min-w-0 space-y-2 lg:sticky lg:top-4 lg:max-h-[calc(100vh-2rem)] lg:self-start lg:overflow-y-auto">
          <p className="text-sm font-medium">YAML</p>
          <YamlView blocks={blocks} />
        </div>
      </div>
      <ApplyPanel clusterId={id} flow={flow} yaml={yaml} blocker={blocker} url={host} />
    </div>
  );
}

// ---- Helm-chart ----

type HelmInput = { name: string; repo: string; chart: string; version: string; namespace: string; values: string };
type HelmKey = "name" | "repo" | "chart" | "version" | "namespace" | "values";

const emptyHelm: HelmInput = { name: "", repo: "", chart: "", version: "", namespace: "", values: "" };

// podinfo is een kleine webapp om Kubernetes mee te leren, met een chart
// die een Ingress kan maken.
function podinfo(vip: string): HelmInput {
  const values = vip
    ? [
        "replicaCount: 2",
        "ingress:",
        "  enabled: true",
        "  className: traefik",
        "  hosts:",
        `    - host: podinfo.${vip}.nip.io`,
        "      paths:",
        "        - path: /",
        "          pathType: ImplementationSpecific",
      ]
    : ["replicaCount: 2"];
  return {
    name: "podinfo",
    repo: "https://stefanprodan.github.io/podinfo",
    chart: "podinfo",
    version: "",
    namespace: "podinfo",
    values: values.join("\n"),
  };
}

function helmField(kind: string, field: string): HelmKey | null {
  if (kind !== "HelmChart") return null;
  if (field === "metadata.name") return "name";
  if (field === "spec.valuesContent") return "values";
  if (field === "spec.repo") return "repo";
  if (field === "spec.chart") return "chart";
  if (field === "spec.version") return "version";
  if (field === "spec.targetNamespace") return "namespace";
  return null;
}

function helmBlock(h: HelmInput): { block: Block; name: string; ns: string } {
  const chart = h.chart.trim();
  const name = h.name.trim() || chart.split("/").pop() || "";
  const ns = h.namespace.trim() || name;
  const values = h.values.replace(/\s+$/, "");
  const lines = values.split("\n");
  // Begint de eerste regel ingesprongen, dan zegt een getal hoe ver de
  // values zelf inspringen.
  const first = lines.find((l) => l.trim() !== "") ?? "";
  const indicator = /^\s/.test(first) ? "|2-" : "|-";
  const yaml = [
    "apiVersion: helm.cattle.io/v1",
    "kind: HelmChart",
    "metadata:",
    `  name: ${q(name || "naam")}`,
    "  namespace: kube-system",
    "spec:",
    ...(h.repo.trim() ? [`  repo: ${q(h.repo.trim())}`] : []),
    `  chart: ${q(chart || "chart")}`,
    ...(h.version.trim() ? [`  version: ${q(h.version.trim())}`] : []),
    `  targetNamespace: ${q(ns || "namespace")}`,
    "  createNamespace: true",
    ...(values.trim() ? [`  valuesContent: ${indicator}`, ...lines.map((l) => (l === "" ? "" : `    ${l}`))] : []),
    "",
  ].join("\n");
  return {
    name,
    ns,
    block: {
      title: "HelmChart",
      help: (
        <>
          Een object voor de helm-controller van k3s. Die installeert de chart {chart || "…"} in namespace {ns || "…"}, met een
          Job in kube-system. Pas je de HelmChart aan, dan werkt hij de installatie bij; haal je hem weg, dan gaat de app ook weg.
        </>
      ),
      yaml,
    },
  };
}

function HelmForm({ id, o }: { id: string; o: KubeOverview }) {
  const [h, setH] = useState<HelmInput>(emptyHelm);
  const set = (patch: Partial<HelmInput>) => setH((old) => ({ ...old, ...patch }));
  const { block, name, ns } = helmBlock(h);
  const flow = useApplyFlow(id, block.yaml, "kube-system");
  const server = fieldErrors(flow.fresh?.result, helmField);
  const blocker = !h.chart.trim() ? "Vul minstens de chart in." : null;
  const job = `helm-install-${name || "naam"}`;

  return (
    <div className="space-y-6">
      <Learn
        title="Wat is Helm?"
        commands={[
          kubectl("get helmcharts -n kube-system"),
          kubectl(`logs -n kube-system job/${job}`),
          kubectl(`get all -n ${ns || "<namespace>"}`),
        ]}
      >
        <p>
          Helm is een pakketbeheerder voor Kubernetes. Een <strong>chart</strong> is een pakket met YAML-sjablonen, en met{" "}
          <strong>values</strong> vul je ze in, zoals het aantal pods of een webadres. Charts vind je op artifacthub.io, met hun
          values.yaml erbij.
        </p>
        <p>
          k3s heeft Helm ingebouwd: maak je een HelmChart-object, dan installeert de helm-controller de chart zelf. Een fout bij
          het installeren zie je in de logs van de Job {job}.
        </p>
      </Learn>
      <div className="grid gap-6 lg:grid-cols-2">
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (!blocker) flow.check(false);
          }}
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-sm text-slate-600 dark:text-slate-400">
              Bij YAML zie je meteen de HelmChart die Kubernetes krijgt.
            </p>
            <Button type="button" variant="secondary" onClick={() => setH(podinfo(o.vip ?? ""))}>
              Voorbeeld: podinfo
            </Button>
          </div>
          <FormField
            label="Repository"
            id="helm-repo"
            error={server.repo}
            hint="Het adres van de Helm-repository. Leeg als de chart een oci://-adres is."
          >
            <Input
              id="helm-repo"
              value={h.repo}
              placeholder="https://stefanprodan.github.io/podinfo"
              autoComplete="off"
              spellCheck={false}
              className={cx("font-mono", server.repo && invalidClass)}
              onChange={(e) => set({ repo: e.target.value })}
            />
          </FormField>
          <div className="grid gap-4 sm:grid-cols-2">
            <FormField label="Chart" id="helm-chart" error={server.chart}>
              <Input
                id="helm-chart"
                value={h.chart}
                placeholder="podinfo"
                autoComplete="off"
                spellCheck={false}
                className={cx("font-mono", server.chart && invalidClass)}
                onChange={(e) => set({ chart: e.target.value })}
              />
            </FormField>
            <FormField label="Versie" id="helm-version" error={server.version} hint="Leeg: de nieuwste. Vast is voorspelbaarder.">
              <Input
                id="helm-version"
                value={h.version}
                placeholder="nieuwste"
                autoComplete="off"
                spellCheck={false}
                className={cx("font-mono", server.version && invalidClass)}
                onChange={(e) => set({ version: e.target.value })}
              />
            </FormField>
            <FormField label="Naam" id="helm-name" error={server.name} hint="De naam van de installatie; leeg is de chart.">
              <Input
                id="helm-name"
                value={h.name}
                placeholder={h.chart.trim().split("/").pop() || "podinfo"}
                autoComplete="off"
                spellCheck={false}
                className={cx(server.name && invalidClass)}
                onChange={(e) => set({ name: e.target.value })}
              />
            </FormField>
            <FormField label="Namespace" id="helm-ns" error={server.namespace} hint="Waar de app komt; leeg is de naam.">
              <Input
                id="helm-ns"
                value={h.namespace}
                placeholder={name || "podinfo"}
                autoComplete="off"
                spellCheck={false}
                className={cx(server.namespace && invalidClass)}
                onChange={(e) => set({ namespace: e.target.value })}
              />
            </FormField>
          </div>
          <FormField
            label="Values"
            id="helm-values"
            error={server.values}
            hint="YAML die de standaard values van de chart overschrijft. Leeg: alles standaard."
          >
            <Textarea
              id="helm-values"
              rows={10}
              value={h.values}
              placeholder={"replicaCount: 2"}
              spellCheck={false}
              wrap="off"
              className={cx("font-mono text-xs", server.values && invalidClass)}
              onChange={(e) => set({ values: e.target.value })}
            />
          </FormField>
        </form>
        <div className="min-w-0 space-y-2 lg:sticky lg:top-4 lg:max-h-[calc(100vh-2rem)] lg:self-start lg:overflow-y-auto">
          <p className="text-sm font-medium">YAML</p>
          <YamlView blocks={[block]} />
        </div>
      </div>
      <ApplyPanel
        clusterId={id}
        flow={flow}
        yaml={block.yaml}
        blocker={blocker}
        after={
          <p>
            De helm-controller installeert de chart nu. Volg het in de Job{" "}
            <ObjectLink clusterId={id} objRef={refOf("Job", "kube-system", job)}>
              {job}
            </ObjectLink>
            ; daarna staat de app bij Apps in namespace {ns}.
          </p>
        }
      />
    </div>
  );
}

// ---- Eigen YAML ----

type Example = { id: string; label: string; help: string; yaml: string };

function examples(vip: string): Example[] {
  const ingress = (name: string) =>
    [
      "apiVersion: networking.k8s.io/v1",
      "kind: Ingress",
      "metadata:",
      `  name: ${name}`,
      "spec:",
      "  ingressClassName: traefik",
      "  rules:",
      `    - host: ${webHost(name, vip)}`,
      "      http:",
      "        paths:",
      "          - path: /",
      "            pathType: Prefix",
      "            backend:",
      "              service:",
      `                name: ${name}`,
      "                port:",
      "                  name: http",
    ].join("\n");
  const service = (name: string) =>
    [
      "apiVersion: v1",
      "kind: Service",
      "metadata:",
      `  name: ${name}`,
      "spec:",
      "  selector:",
      `    app.kubernetes.io/name: ${name}`,
      "  ports:",
      "    - name: http",
      "      port: 80",
      "      targetPort: http",
    ].join("\n");
  return [
    {
      id: "whoami",
      label: "whoami: Deployment, Service en Ingress",
      help: "Een kleine webserver die toont welke pod antwoordt. Herlaad de pagina en zie het verkeer over de pods gaan.",
      yaml: [
        "# whoami toont bij elk verzoek de naam van de pod die antwoordt.",
        "apiVersion: apps/v1",
        "kind: Deployment",
        "metadata:",
        "  name: whoami",
        "spec:",
        "  replicas: 2",
        "  selector:",
        "    matchLabels:",
        "      app.kubernetes.io/name: whoami",
        "  template:",
        "    metadata:",
        "      labels:",
        "        app.kubernetes.io/name: whoami",
        "    spec:",
        "      containers:",
        "        - name: whoami",
        "          image: traefik/whoami:v1.11",
        "          ports:",
        "            - name: http",
        "              containerPort: 80",
        "---",
        service("whoami"),
        "---",
        ingress("whoami"),
        "",
      ].join("\n"),
    },
    {
      id: "nginx",
      label: "nginx: twee pods en een Service",
      help: "nginx alleen binnen het cluster: andere pods bereiken hem als http://nginx.",
      yaml: [
        "apiVersion: apps/v1",
        "kind: Deployment",
        "metadata:",
        "  name: nginx",
        "spec:",
        "  replicas: 2",
        "  selector:",
        "    matchLabels:",
        "      app.kubernetes.io/name: nginx",
        "  template:",
        "    metadata:",
        "      labels:",
        "        app.kubernetes.io/name: nginx",
        "    spec:",
        "      containers:",
        "        - name: nginx",
        "          image: nginx:1.29",
        "          ports:",
        "            - name: http",
        "              containerPort: 80",
        "          # Zonder deze grenzen mag een pod alles van de node gebruiken.",
        "          resources:",
        "            requests:",
        "              cpu: 50m",
        "              memory: 32Mi",
        "            limits:",
        "              memory: 128Mi",
        "---",
        service("nginx"),
        "",
      ].join("\n"),
    },
    {
      id: "uptime-kuma",
      label: "Uptime Kuma: met een volume en een webadres",
      help: "Uptime Kuma bewaakt websites. Zijn data staat op een volume, zodat ze een herstart overleeft.",
      yaml: [
        "# Uptime Kuma bewaakt websites en diensten.",
        "apiVersion: v1",
        "kind: PersistentVolumeClaim",
        "metadata:",
        "  name: uptime-kuma-data",
        "spec:",
        "  accessModes: [ReadWriteOnce]",
        "  resources:",
        "    requests:",
        "      storage: 1Gi",
        "---",
        "apiVersion: apps/v1",
        "kind: Deployment",
        "metadata:",
        "  name: uptime-kuma",
        "spec:",
        "  replicas: 1",
        "  # Recreate stopt eerst de oude pod: nooit twee tegelijk op hetzelfde volume.",
        "  strategy:",
        "    type: Recreate",
        "  selector:",
        "    matchLabels:",
        "      app.kubernetes.io/name: uptime-kuma",
        "  template:",
        "    metadata:",
        "      labels:",
        "        app.kubernetes.io/name: uptime-kuma",
        "    spec:",
        "      containers:",
        "        - name: uptime-kuma",
        "          image: louislam/uptime-kuma:2",
        "          ports:",
        "            - name: http",
        "              containerPort: 3001",
        "          volumeMounts:",
        "            - name: data",
        "              mountPath: /app/data",
        "      volumes:",
        "        - name: data",
        "          persistentVolumeClaim:",
        "            claimName: uptime-kuma-data",
        "---",
        service("uptime-kuma"),
        "---",
        ingress("uptime-kuma"),
        "",
      ].join("\n"),
    },
    {
      id: "postgres",
      label: "PostgreSQL: een StatefulSet met een Secret",
      help: "Een databank als StatefulSet: de pod heet altijd postgres-0 en houdt zijn eigen volume. Verander het wachtwoord voor je toepast.",
      yaml: [
        "# Verander het wachtwoord voor je toepast. ClusterForge toont het daarna nooit meer.",
        "apiVersion: v1",
        "kind: Secret",
        "metadata:",
        "  name: postgres",
        "type: Opaque",
        "stringData:",
        "  POSTGRES_PASSWORD: verander-mij",
        "---",
        "# Zonder clusterIP krijgt elke pod een eigen naam in DNS: postgres-0.postgres.",
        "apiVersion: v1",
        "kind: Service",
        "metadata:",
        "  name: postgres",
        "spec:",
        "  clusterIP: None",
        "  selector:",
        "    app.kubernetes.io/name: postgres",
        "  ports:",
        "    - name: postgres",
        "      port: 5432",
        "---",
        "apiVersion: apps/v1",
        "kind: StatefulSet",
        "metadata:",
        "  name: postgres",
        "spec:",
        "  serviceName: postgres",
        "  replicas: 1",
        "  selector:",
        "    matchLabels:",
        "      app.kubernetes.io/name: postgres",
        "  template:",
        "    metadata:",
        "      labels:",
        "        app.kubernetes.io/name: postgres",
        "    spec:",
        "      containers:",
        "        - name: postgres",
        "          image: postgres:18",
        "          ports:",
        "            - name: postgres",
        "              containerPort: 5432",
        "          envFrom:",
        "            - secretRef:",
        "                name: postgres",
        "          volumeMounts:",
        "            - name: data",
        "              mountPath: /var/lib/postgresql",
        "  # Elke pod krijgt een eigen volume: data-postgres-0.",
        "  volumeClaimTemplates:",
        "    - metadata:",
        "        name: data",
        "      spec:",
        "        accessModes: [ReadWriteOnce]",
        "        resources:",
        "          requests:",
        "            storage: 2Gi",
        "",
      ].join("\n"),
    },
  ];
}

function YamlForm({ id, o }: { id: string; o: KubeOverview }) {
  const [text, setText] = useState("");
  const [ns, setNs] = useState("default");
  const [picked, setPicked] = useState("");
  const learning = useKubeLearning();
  const flow = useApplyFlow(id, text, ns);
  const all = examples(o.vip ?? "");
  const example = all.find((e) => e.id === picked);
  const ownNamespaces = o.namespaces.filter((x) => !systemNamespaces.includes(x) || x === ns);
  const load = (key: string) => {
    const ex = all.find((e) => e.id === key);
    if (!ex) return;
    const own = text.trim() !== "" && !all.some((e) => e.yaml === text);
    if (own && !window.confirm("De YAML in het vak vervangen door het voorbeeld?")) return;
    setText(ex.yaml);
    setPicked(ex.id);
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end gap-3">
        <div>
          <Label htmlFor="yaml-example">Voorbeeld</Label>
          <Select id="yaml-example" className="w-auto" value="" onChange={(e) => load(e.target.value)}>
            <option value="">Kies een voorbeeld…</option>
            {all.map((e) => (
              <option key={e.id} value={e.id}>
                {e.label}
              </option>
            ))}
          </Select>
        </div>
        <div>
          <Label htmlFor="yaml-ns">Namespace voor objecten zonder namespace</Label>
          <Select id="yaml-ns" className="w-auto" value={ns} onChange={(e) => setNs(e.target.value)}>
            {ownNamespaces.map((x) => (
              <option key={x} value={x}>
                {x}
              </option>
            ))}
          </Select>
        </div>
      </div>
      {learning.on && example && text === example.yaml && (
        <p className="text-sm text-slate-600 dark:text-slate-400">{example.help}</p>
      )}
      <Textarea
        id="yaml-text"
        aria-label="YAML"
        rows={22}
        value={text}
        spellCheck={false}
        wrap="off"
        placeholder={"apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: mijn-app\n…"}
        className="font-mono text-xs leading-relaxed"
        onChange={(e) => setText(e.target.value)}
      />
      <ApplyPanel
        clusterId={id}
        flow={flow}
        yaml={text}
        blocker={text.trim() === "" ? "Plak YAML of kies een voorbeeld." : null}
      />
    </div>
  );
}

// ---- Controleren en toepassen ----

type Checked = { yaml: string; namespace: string; force: boolean; result: KubeApplyResult; taken: KubeApplyObject[] };

// useApplyFlow houdt bij wat er gecontroleerd is. Een controle geldt
// alleen voor precies die YAML: verandert er iets, dan moet het opnieuw.
// taken zijn de objecten met velden van een ander programma, uit de
// controle zonder overnemen, zodat je ziet wat je overneemt.
export function useApplyFlow(clusterId: string, yaml: string, namespace: string) {
  const preview = useKubeApply(clusterId);
  const apply = useKubeApply(clusterId);
  const [checked, setChecked] = useState<Checked | null>(null);
  const [done, setDone] = useState<KubeApplyResult | null>(null);
  const fresh = checked && checked.yaml === yaml && checked.namespace === namespace ? checked : null;
  const check = (force: boolean) => {
    const taken = force && fresh ? (fresh.force ? fresh.taken : fresh.result.objects.filter((x) => x.conflicts.length > 0)) : [];
    setDone(null);
    apply.reset();
    preview.mutate(
      { yaml, namespace, preview: true, force: force || undefined },
      {
        onSuccess: (result) => setChecked({ yaml, namespace, force, result, taken }),
        onError: () => setChecked(null),
      },
    );
  };
  const run = (confirm: string | undefined) => {
    if (!fresh) return;
    apply.mutate(
      { yaml, namespace, force: fresh.force || undefined, confirm },
      {
        onSuccess: (r) => {
          setDone(r);
          setChecked(null);
        },
      },
    );
  };
  const same = (v: { yaml: string; namespace?: string } | undefined) => v?.yaml === yaml && v?.namespace === namespace;
  return {
    fresh,
    done,
    check,
    run,
    checking: preview.isPending,
    // Het vinkje Overnemen verspringt meteen, ook al loopt de controle nog.
    forcing: preview.isPending && !!preview.variables?.force,
    applying: apply.isPending,
    checkError: same(preview.variables) ? preview.error : null,
    applyError: apply.error,
  };
}

type Tone = "slate" | "green" | "amber" | "red" | "blue";

const actionInfo: Record<KubeApplyObject["action"], { check: string; done: string; tone: Tone }> = {
  create: { check: "Nieuw", done: "Gemaakt", tone: "green" },
  update: { check: "Anders", done: "Bijgewerkt", tone: "blue" },
  unchanged: { check: "Hetzelfde", done: "Hetzelfde", tone: "slate" },
  failed: { check: "Fout", done: "Mislukt", tone: "red" },
  skipped: { check: "Niet toegepast", done: "Niet toegepast", tone: "slate" },
};

function summary(r: KubeApplyResult) {
  const count = (a: KubeApplyObject["action"]) => r.objects.filter((x) => x.action === a).length;
  const parts: [number, string][] = [
    [count("create"), r.applied ? "gemaakt" : "nieuw"],
    [count("update"), r.applied ? "bijgewerkt" : "anders"],
    [count("unchanged"), "hetzelfde"],
    [r.objects.filter((x) => x.action === "failed" && !isConflict(x)).length, "met een fout"],
    [r.objects.filter(isConflict).length, "met een conflict"],
    [count("skipped"), "niet toegepast"],
  ];
  const n = r.objects.length;
  return `${n} ${n === 1 ? "object" : "objecten"}: ${parts
    .filter(([c]) => c > 0)
    .map(([c, w]) => `${c} ${w}`)
    .join(", ")}`;
}

// isConflict: het object faalde alleen omdat een ander programma velden
// beheert. Dat is geen fout in de YAML.
function isConflict(x: KubeApplyObject) {
  return x.action === "failed" && x.errors.length === 0 && x.conflicts.length > 0;
}

function objectName(x: KubeApplyObject) {
  return `${x.kind} ${x.namespace ? `${x.namespace}/` : ""}${x.name}`;
}

// managerHelp zegt welk programma achter de naam van een field manager zit.
function managerHelp(m: string) {
  if (m === "kubectl-client-side-apply") return "kubectl-client-side-apply is een gewone kubectl apply.";
  if (m === "kubectl-edit") return "kubectl-edit is kubectl edit.";
  if (m === "kubectl" || m.startsWith("kubectl-")) return `${m} is kubectl.`;
  if (m.startsWith("helm")) return `${m} is Helm.`;
  return "";
}

export function ApplyPanel({
  clusterId,
  flow,
  yaml,
  blocker,
  url,
  after,
}: {
  clusterId: string;
  flow: ReturnType<typeof useApplyFlow>;
  yaml: string;
  blocker: string | null;
  url?: string;
  after?: ReactNode;
}) {
  const prod = useConfirm(clusterId);
  const fresh = flow.fresh;
  const r = fresh?.result;
  const conflicted = r ? r.objects.filter((x) => x.conflicts.length > 0) : [];
  const hasErrors = !!r && r.objects.some((x) => x.errors.length > 0);
  const canApply = !!r && r.ok && r.changed > 0;

  return (
    <div className="space-y-4 border-t border-slate-200 pt-4 dark:border-slate-800">
      <div className="flex flex-wrap items-center gap-3">
        <Button
          type="button"
          variant={canApply ? "secondary" : "primary"}
          disabled={!!blocker || flow.checking}
          onClick={() => flow.check(false)}
        >
          {flow.checking ? "Controleren…" : r ? "Opnieuw controleren" : "Controleren"}
        </Button>
        <span className="text-sm text-slate-500">
          {blocker ?? (r ? null : "Kubernetes keurt alles met een dry-run; er verandert nog niets.")}
        </span>
      </div>

      {flow.checkError !== null && flow.checkError !== undefined && <ApplyError error={flow.checkError} yaml={yaml} />}

      {r && fresh && (
        <>
          <ResultList clusterId={clusterId} result={r} />
          {conflicted.length > 0 && (
            <Conflicts objects={conflicted} forced={false} busy={flow.checking} forcing={flow.forcing} onForce={flow.check} />
          )}
          {fresh.force && fresh.taken.length > 0 && (
            <Conflicts objects={fresh.taken} forced busy={flow.checking} forcing={flow.forcing} onForce={flow.check} />
          )}
          {r.ok && r.changed === 0 && (
            <Alert kind="info">Geen verschil: alles staat al zo in het cluster. Toepassen zou niets veranderen.</Alert>
          )}
          {hasErrors && (
            <p className="text-sm text-red-700 dark:text-red-300">
              Los de fouten op en controleer opnieuw; dan kun je toepassen.
            </p>
          )}
          {canApply && (
            <div className="space-y-3 rounded-md border border-slate-200 bg-white p-4 text-sm dark:border-slate-800 dark:bg-slate-900">
              <p>
                Klaar om toe te passen: {r.changed} {r.changed === 1 ? "object verandert" : "objecten veranderen"}.
                {fresh.force && " Je neemt de velden hierboven over."}
              </p>
              {prod.field}
              <Button
                type="button"
                variant={prod.prod || fresh.force ? "danger" : "primary"}
                disabled={!prod.ok || flow.applying}
                onClick={() => flow.run(prod.confirm)}
              >
                {flow.applying ? "Bezig…" : fresh.force ? "Toepassen en overnemen" : "Toepassen"}
              </Button>
            </div>
          )}
        </>
      )}

      {flow.applyError !== null && flow.applyError !== undefined && <ApplyError error={flow.applyError} yaml={yaml} />}
      {flow.done && <Done clusterId={clusterId} result={flow.done} url={url} after={after} />}
    </div>
  );
}

// ApplyError toont een fout van Controleren of Toepassen. Bij een fout in
// de YAML zelf staan de regels erbij. De parser noemt de regel waar hij
// het blok begon te lezen; de fout zit daar of net eronder.
function ApplyError({ error, yaml }: { error: unknown; yaml: string }) {
  if (error instanceof ApiError && error.code === "yaml") {
    const m = /^regel (\d+)/.exec(error.message);
    const n = m ? Number(m[1]) : 0;
    const lines = yaml.split("\n");
    const near = n > 0 && n <= lines.length ? lines.slice(n - 1, n + 2) : [];
    const width = String(n + near.length - 1).length;
    return (
      <Alert>
        <span className="font-medium">Fout in de YAML.</span> {sentence(error.message)}
        {near.length > 0 && (
          <pre className="mt-2 overflow-x-auto rounded bg-red-100 px-2 py-1 font-mono text-xs dark:bg-red-900/50">
            {near.map((l, i) => `${String(n + i).padStart(width)} | ${l.replace(/\t/g, "→   ")}`).join("\n")}
          </pre>
        )}
        {yaml.includes("\t") && (
          <p className="mt-2 text-xs">Er staat een tab in de YAML (→). YAML springt alleen in met spaties.</p>
        )}
      </Alert>
    );
  }
  return <KubeError error={error} />;
}

function ResultList({ clusterId, result, diffs = true }: { clusterId: string; result: KubeApplyResult; diffs?: boolean }) {
  return (
    <div className="space-y-2">
      <p className="text-sm font-medium">{summary(result)}</p>
      <ul className="space-y-2">
        {result.objects.map((x) => (
          <ResultItem
            key={`${x.doc}/${x.kind}/${x.namespace}/${x.name}`}
            clusterId={clusterId}
            x={x}
            applied={result.applied}
            diff={diffs}
          />
        ))}
      </ul>
    </div>
  );
}

function ResultItem({ clusterId, x, applied, diff }: { clusterId: string; x: KubeApplyObject; applied: boolean; diff: boolean }) {
  const info = isConflict(x) ? { check: "Conflict", done: "Conflict", tone: "amber" as const } : actionInfo[x.action];
  // Bij controleren bestaat een nieuw object nog niet; een link zou leeg zijn.
  const exists = applied ? x.action !== "failed" && x.action !== "skipped" : x.action === "update" || x.action === "unchanged";
  return (
    <li className="rounded-md border border-slate-200 bg-white px-3 py-2 text-sm dark:border-slate-800 dark:bg-slate-900">
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone={info.tone}>{applied ? info.done : info.check}</Badge>
        <span className="font-medium break-all">
          {exists && x.ref ? (
            <ObjectLink clusterId={clusterId} objRef={x.ref}>
              {objectName(x)}
            </ObjectLink>
          ) : (
            objectName(x)
          )}
        </span>
        <span className="text-xs text-slate-500">
          {x.api_version}
          {x.doc > 0 && ` · document ${x.doc}`}
        </span>
      </div>
      {x.errors.length > 0 && (
        <ul className="mt-2 space-y-1 text-red-700 dark:text-red-300">
          {x.errors.map((e, i) => (
            <li key={i}>
              {e.field && <code className="mr-1.5 rounded bg-red-50 px-1 text-xs dark:bg-red-950">{e.field}</code>}
              {dutch(e.message)}
            </li>
          ))}
        </ul>
      )}
      {x.note && <p className="mt-2 text-xs text-slate-600 dark:text-slate-400">{x.note}</p>}
      {diff &&
        x.diff &&
        (x.action === "update" ? (
          <div className="mt-2">
            <Diff text={x.diff} />
          </div>
        ) : (
          <details className="mt-2">
            <summary className="cursor-pointer text-xs text-slate-600 dark:text-slate-400">
              Zoals Kubernetes het maakt, met de standaardwaarden die het zelf invult
            </summary>
            <div className="mt-2">
              <Diff text={x.diff} />
            </div>
          </details>
        ))}
    </li>
  );
}

function Conflicts({
  objects,
  forced,
  busy,
  forcing,
  onForce,
}: {
  objects: KubeApplyObject[];
  forced: boolean;
  busy: boolean;
  forcing: boolean;
  onForce: (force: boolean) => void;
}) {
  const managers = [...new Set(objects.flatMap((x) => x.conflicts.map((c) => c.manager)))];
  const help = managers.map(managerHelp).filter(Boolean).join(" ");
  return (
    <div className="space-y-2 rounded-md border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-950 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-100">
      <p className="font-medium">
        {forced ? "Je neemt deze velden over" : "Een ander programma beheert velden die deze YAML anders zet"}
      </p>
      <ul className="space-y-1">
        {objects.flatMap((x) =>
          x.conflicts.map((c) => (
            <li key={`${objectName(x)}/${c.field}`}>
              {objectName(x)}: <code className="text-xs">{c.field}</code> van{" "}
              <strong>{c.manager || "een ander programma"}</strong>
            </li>
          )),
        )}
      </ul>
      <p className="text-xs leading-relaxed">
        Kubernetes onthoudt per veld welk programma het zette. {help} Overnemen maakt ClusterForge de eigenaar van die velden.
        Past dat programma later zijn eigen YAML weer toe, dan zet het ze terug.
      </p>
      <label className="flex cursor-pointer items-start gap-2">
        <input
          type="checkbox"
          className="mt-0.5 size-4"
          checked={busy ? forcing : forced}
          disabled={busy}
          onChange={(e) => onForce(e.target.checked)}
        />
        <span>{forced ? "Velden overnemen" : "Velden overnemen; Controleren toont dan wat er verandert"}</span>
      </label>
    </div>
  );
}

function Done({
  clusterId,
  result,
  url,
  after,
}: {
  clusterId: string;
  result: KubeApplyResult;
  url?: string;
  after?: ReactNode;
}) {
  if (!result.ok) {
    return (
      <div className="space-y-3">
        <Alert>Toepassen stopte bij een fout. Wat ervoor stond, is wel toegepast; de rest niet.</Alert>
        <ResultList clusterId={clusterId} result={result} diffs={false} />
      </div>
    );
  }
  return (
    <Alert kind="success">
      <div className="space-y-2">
        <p className="font-medium">Toegepast. {summary(result)}.</p>
        <ul className="space-y-0.5">
          {result.objects.map((x) => (
            <li key={`${x.doc}/${x.kind}/${x.namespace}/${x.name}`}>
              {x.ref ? (
                <ObjectLink clusterId={clusterId} objRef={x.ref}>
                  {objectName(x)}
                </ObjectLink>
              ) : (
                objectName(x)
              )}
            </li>
          ))}
        </ul>
        {url && (
          <p>
            Open{" "}
            <a href={`http://${url}`} target="_blank" rel="noreferrer" className="font-medium underline">
              http://{url}
            </a>{" "}
            zodra de pods draaien; bij Apps zie je hoe ver ze zijn.
          </p>
        )}
        {after}
      </div>
    </Alert>
  );
}
