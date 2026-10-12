"use client";

import Link from "next/link";
import { useState, type FormEvent, type ReactNode } from "react";
import { KubeError, Learn } from "@/components/kube/Kube";
import { Modal } from "@/components/Modal";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { ago } from "@/components/inventory/bits";
import { Alert, Badge, Button, Field, Input, cx } from "@/components/ui";
import { useCluster, useIsAdmin } from "@/lib/inventory";
import {
  kubectl,
  kubectlObject,
  systemNamespaces,
  useKubeAction,
  useKubeDrain,
  useKubeDrainPlan,
  useKubeRevisions,
  type KubeActionInput,
  type KubeActionName,
  type KubeDrainPod,
  type KubeRef,
} from "@/lib/kube";

// ActionTarget is het object waarop de knoppen werken. owner is wie een
// pod maakte; zo zegt de dialoog of er een nieuwe komt.
export type ActionTarget = {
  kind: string;
  ref: KubeRef;
  actions: KubeActionName[];
  replicas?: number | null;
  owner?: { kind: string; name: string } | null;
};

// ActionDone is wat er gebeurde, voor de melding boven de pagina of tabel.
export type ActionDone = { message: string; jobId?: string; deleted?: boolean };

const labels: Record<KubeActionName, string> = {
  scale: "Schalen",
  restart: "Herstarten",
  rollback: "Terugdraaien",
  cordon: "Cordon",
  uncordon: "Uncordon",
  drain: "Leegmaken",
  delete: "Weghalen",
};

// workloadActions zijn de acties in de tabel Apps; weghalen staat op de
// pagina van het object zelf, waar je ziet wat je weghaalt.
export function workloadActions(kind: string): KubeActionName[] {
  if (kind === "Deployment") return ["scale", "restart", "rollback"];
  if (kind === "StatefulSet") return ["scale", "restart"];
  if (kind === "DaemonSet") return ["restart"];
  return [];
}

// KubeActions toont de acties bij een object, als knoppen of als menu.
// Alleen een beheerder ziet ze; de server controleert hetzelfde.
export function KubeActions({
  clusterId,
  target,
  layout = "buttons",
  names,
  onDone,
}: {
  clusterId: string;
  target: ActionTarget;
  layout?: "buttons" | "menu";
  // names geeft een knop een andere naam, zoals Opruimen voor weghalen.
  names?: Partial<Record<KubeActionName, string>>;
  onDone: (d: ActionDone) => void;
}) {
  const isAdmin = useIsAdmin();
  const [open, setOpen] = useState<KubeActionName | null>(null);
  if (!isAdmin || target.actions.length === 0) return null;
  const close = () => setOpen(null);
  const done = (d: ActionDone) => {
    setOpen(null);
    onDone(d);
  };
  const props = { clusterId, target, onClose: close, onDone: done };

  return (
    <>
      {layout === "buttons" ? (
        <span className="flex flex-wrap gap-2">
          {target.actions.map((a) => (
            <Button key={a} variant={a === "delete" ? "secondary-danger" : "secondary"} onClick={() => setOpen(a)}>
              {names?.[a] ?? labels[a]}
            </Button>
          ))}
        </span>
      ) : (
        <details className="relative inline-block text-left [&[open]>summary]:bg-slate-100 dark:[&[open]>summary]:bg-slate-800">
          <summary className="cursor-pointer list-none rounded-md px-2 py-1 text-sm font-medium whitespace-nowrap text-slate-600 select-none hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800">
            Acties ▾
          </summary>
          <div className="absolute right-0 z-20 mt-1 w-40 rounded-md border border-slate-200 bg-white py-1 shadow-lg dark:border-slate-700 dark:bg-slate-900">
            {target.actions.map((a) => (
              <button
                key={a}
                type="button"
                onClick={(e) => {
                  (e.currentTarget.closest("details") as HTMLDetailsElement | null)?.removeAttribute("open");
                  setOpen(a);
                }}
                className={cx(
                  "block w-full px-3 py-1.5 text-left text-sm hover:bg-slate-50 dark:hover:bg-slate-800",
                  a === "delete" && "text-red-700 dark:text-red-300",
                )}
              >
                {names?.[a] ?? labels[a]}…
              </button>
            ))}
          </div>
        </details>
      )}
      {open === "scale" && <ScaleDialog {...props} />}
      {open === "restart" && <RestartDialog {...props} />}
      {open === "rollback" && <RollbackDialog {...props} />}
      {open === "delete" && <DeleteDialog {...props} />}
      {(open === "cordon" || open === "uncordon") && <CordonDialog {...props} action={open} />}
      {open === "drain" && <DrainDialog {...props} />}
    </>
  );
}

// DoneAlert toont wat een actie deed, met de taak als die er is.
export function DoneAlert({ done, onClose }: { done: ActionDone | null; onClose: () => void }) {
  if (!done) return null;
  return (
    <Alert kind="success">
      <span className="flex flex-wrap items-start justify-between gap-2">
        <span>
          {done.message}
          {done.jobId && (
            <>
              {" "}
              <Link href={`/taken/detail?id=${done.jobId}`} className="font-medium underline">
                Taak volgen
              </Link>
            </>
          )}
        </span>
        <button type="button" className="text-xs underline" onClick={onClose}>
          Sluiten
        </button>
      </span>
    </Alert>
  );
}

type DialogProps = {
  clusterId: string;
  target: ActionTarget;
  onClose: () => void;
  onDone: (d: ActionDone) => void;
};

// useConfirm is de bevestiging op prod: de slug intikken, met
// tweestapsverificatie aan.
export function useConfirm(clusterId: string) {
  const cluster = useCluster(clusterId);
  const totp = useProdTotp();
  const [value, setValue] = useState("");
  const prod = cluster.data?.environment === "prod";
  const slug = cluster.data?.slug ?? "";
  return {
    prod,
    ok: !prod || prodConfirmed(slug, value, totp),
    confirm: prod ? value : undefined,
    field: prod ? <ProdConfirm slug={slug} value={value} onChange={setValue} /> : null,
  };
}

// ActionDialog is het kader van elke actie: uitleg, de kubectl-versie in de
// leermodus, de bevestiging op prod en een fout als het niet lukt.
function ActionDialog({
  title,
  submit,
  danger = false,
  ready = true,
  pending,
  error,
  prod,
  learn,
  commands,
  wide = false,
  onClose,
  onSubmit,
  children,
}: {
  title: string;
  submit: string;
  danger?: boolean;
  ready?: boolean;
  pending: boolean;
  error: unknown;
  prod: ReturnType<typeof useConfirm>;
  learn: ReactNode;
  commands: string[];
  wide?: boolean;
  onClose: () => void;
  onSubmit: () => void;
  children: ReactNode;
}) {
  const send = (e: FormEvent) => {
    e.preventDefault();
    if (ready && prod.ok && !pending) onSubmit();
  };
  return (
    <Modal title={title} onClose={onClose} wide={wide}>
      <form className="space-y-4 text-sm" onSubmit={send}>
        {children}
        <Learn title="Zelf met kubectl" commands={commands}>
          {learn}
        </Learn>
        {prod.field}
        {error !== null && error !== undefined && <KubeError error={error} />}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" variant={danger || prod.prod ? "danger" : "primary"} disabled={!ready || !prod.ok || pending}>
            {pending ? "Bezig…" : submit}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

// useRun voert een actie uit en meldt het resultaat.
function useRun({ clusterId, target, onDone }: DialogProps) {
  const action = useKubeAction(clusterId);
  const run = (body: Omit<KubeActionInput, "ref">) =>
    action.mutate(
      { ...body, ref: target.ref },
      { onSuccess: (r) => onDone({ message: r.message, deleted: body.action === "delete" }) },
    );
  return { run, pending: action.isPending, error: action.error };
}

function ScaleDialog(p: DialogProps) {
  const { clusterId, target, onClose } = p;
  const now = target.replicas ?? 1;
  const [value, setValue] = useState(String(now));
  const prod = useConfirm(clusterId);
  const { run, pending, error } = useRun(p);
  const n = Number(value);
  const valid = value.trim() !== "" && Number.isInteger(n) && n >= 0 && n <= 100;
  const what = kubectlObject(target.ref, target.kind);
  return (
    <ActionDialog
      title={`${target.ref.name} schalen`}
      submit={valid && n === 0 ? "Naar 0 pods" : "Schalen"}
      ready={valid && n !== now}
      danger={valid && n === 0}
      pending={pending}
      error={error}
      prod={prod}
      onClose={onClose}
      onSubmit={() => run({ action: "scale", replicas: n })}
      commands={[kubectl(`scale ${what} --replicas=${valid ? n : now}`), kubectl(`get pods -n ${target.ref.namespace} -w`)]}
      learn={
        <p>
          Schalen zet het aantal replica&apos;s: zoveel gelijke pods houdt Kubernetes draaiend. Zet je het hoger, dan start de
          planner er meteen nieuwe op de nodes met plaats; lager, dan stopt hij er.
          {target.kind === "StatefulSet" && " Een StatefulSet stopt eerst de pod met het hoogste nummer; zijn volume blijft."}
        </p>
      }
    >
      <p>
        {target.kind} <span className="font-mono">{target.ref.name}</span> vraagt nu {now} {now === 1 ? "pod" : "pods"}.
      </p>
      <Field label="Aantal pods" htmlFor="kube-replicas" hint="0 tot 100. Met 0 draait de app niet, maar hij blijft bestaan.">
        <Input
          id="kube-replicas"
          type="number"
          min={0}
          max={100}
          autoFocus
          className="w-32"
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
      </Field>
      {valid && n === 0 && <Alert kind="info">Met 0 pods is de app niet meer bereikbaar tot je hem weer hoger zet.</Alert>}
    </ActionDialog>
  );
}

function RestartDialog(p: DialogProps) {
  const { clusterId, target, onClose } = p;
  const prod = useConfirm(clusterId);
  const { run, pending, error } = useRun(p);
  const what = kubectlObject(target.ref, target.kind);
  return (
    <ActionDialog
      title={`${target.ref.name} herstarten`}
      submit="Herstarten"
      pending={pending}
      error={error}
      prod={prod}
      onClose={onClose}
      onSubmit={() => run({ action: "restart" })}
      commands={[kubectl(`rollout restart ${what}`), kubectl(`rollout status ${what}`)]}
      learn={
        <p>
          Kubernetes herstart geen containers op bevel; het vervangt de pods. ClusterForge zet, net als{" "}
          <code>kubectl rollout restart</code>, het tijdstip als annotatie in de pod-template. Voor Kubernetes is dat een nieuwe
          versie, dus komen er nieuwe pods en gaan de oude weg.
        </p>
      }
    >
      <p>
        {target.kind === "DaemonSet"
          ? "De pod op elke node wordt vervangen door een nieuwe, node na node."
          : "De pods worden een voor een vervangen door nieuwe met dezelfde versie. Zolang er meer dan één pod is, blijft de app bereikbaar."}
      </p>
      <p className="text-slate-600 dark:text-slate-400">
        Handig als de app een gewijzigde ConfigMap of Secret opnieuw moet lezen, of als hij vastzit.
      </p>
    </ActionDialog>
  );
}

function RollbackDialog(p: DialogProps) {
  const { clusterId, target, onClose } = p;
  const prod = useConfirm(clusterId);
  const { run, pending, error } = useRun(p);
  const revs = useKubeRevisions(clusterId, target.ref.namespace, target.ref.name);
  const older = (revs.data ?? []).filter((r) => !r.current);
  const [picked, setPicked] = useState<number | null>(null);
  const chosen = picked ?? older[0]?.revision ?? null;
  const what = kubectlObject(target.ref, target.kind);
  return (
    <ActionDialog
      title={`${target.ref.name} terugdraaien`}
      submit={chosen ? `Terug naar versie ${chosen}` : "Terugdraaien"}
      ready={chosen !== null}
      pending={pending}
      error={error ?? revs.error}
      prod={prod}
      wide
      onClose={onClose}
      onSubmit={() => chosen !== null && run({ action: "rollback", revision: chosen })}
      commands={[kubectl(`rollout history ${what}`), kubectl(`rollout undo ${what}${chosen ? ` --to-revision=${chosen}` : ""}`)]}
      learn={
        <p>
          Bij elke nieuwe versie maakt een Deployment een nieuwe ReplicaSet en houdt hij de oude bij, met nul pods. Terugdraaien
          neemt de pod-template van een oude ReplicaSet over. Dat telt als een nieuwe versie met een hoger nummer, en Kubernetes
          vervangt de pods een voor een.
        </p>
      }
    >
      {revs.isLoading && <p className="text-slate-500">Versies laden…</p>}
      {revs.data && older.length === 0 && (
        <Alert kind="info">Er is geen oudere versie om naar terug te gaan. Die komt er bij de eerste wijziging van de app.</Alert>
      )}
      {revs.data && older.length > 0 && (
        <fieldset className="space-y-2">
          <legend className="mb-1 font-medium">Kies de versie</legend>
          {revs.data.map((r) => (
            <label
              key={r.revision}
              className={cx(
                "flex items-start gap-3 rounded-md border px-3 py-2",
                r.current
                  ? "border-slate-200 bg-slate-50 dark:border-slate-800 dark:bg-slate-900"
                  : "cursor-pointer border-slate-200 dark:border-slate-700",
                chosen === r.revision && "border-brand-500 ring-1 ring-brand-500",
              )}
            >
              <input
                type="radio"
                name="revision"
                className="mt-1"
                disabled={r.current}
                checked={chosen === r.revision}
                onChange={() => setPicked(r.revision)}
              />
              <span className="min-w-0 flex-1">
                <span className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">Versie {r.revision}</span>
                  {r.current && <Badge tone="green">draait nu</Badge>}
                  <span className="text-xs text-slate-500">{ago(r.created_at)}</span>
                </span>
                <span className="block font-mono text-xs break-all">{r.images.join(", ")}</span>
                {r.change_cause && <span className="block text-xs text-slate-500">{r.change_cause}</span>}
              </span>
            </label>
          ))}
        </fieldset>
      )}
    </ActionDialog>
  );
}

// deleteEffect zegt wat er na het weghalen gebeurt.
function deleteEffect(t: ActionTarget) {
  switch (t.kind) {
    case "Pod":
      return t.owner
        ? `${t.owner.kind} ${t.owner.name} maakt meteen een nieuwe pod, misschien op een andere node.`
        : "Deze pod heeft geen eigenaar: hij komt niet terug.";
    case "Deployment":
      return "De Deployment gaat weg met zijn ReplicaSets en al zijn pods. Services en Ingresses blijven, maar sturen nergens meer naartoe.";
    case "StatefulSet":
      return "De StatefulSet gaat weg met zijn pods. De volumes (PersistentVolumeClaims) blijven, zodat de gegevens niet verloren gaan.";
    case "DaemonSet":
      return "De DaemonSet gaat weg met de pod op elke node.";
    case "ReplicaSet":
      return t.actions.includes("scale")
        ? "De ReplicaSet gaat weg met zijn pods."
        : "De Deployment die hem maakte, maakt meteen een nieuwe ReplicaSet met nieuwe pods.";
    case "Job":
      return "De Job gaat weg met zijn pods en hun logs.";
    case "CronJob":
      return "De CronJob gaat weg: er starten geen nieuwe Jobs meer. Ook de Jobs die hij maakte, gaan weg.";
    case "Namespace":
      return "Alles in de namespace gaat weg: apps, pods, Services, ConfigMaps, Secrets en volumes met hun gegevens.";
    case "PersistentVolumeClaim":
      return "De gegevens kunnen verloren gaan: met local-path haalt Kubernetes de map op de node weg. Een pod die hem gebruikt, houdt hem tegen tot die pod weg is.";
    case "PersistentVolume":
      return "Het volume gaat weg; afhankelijk van zijn reclaimPolicy ook de gegevens.";
    case "Service":
      return "Het vaste adres en de naam in DNS verdwijnen; de pods blijven draaien.";
    case "Secret":
    case "ConfigMap":
      return "Pods die hem gebruiken, blijven draaien met wat ze al lazen, maar een nieuwe pod start niet meer.";
    default:
      return `${t.kind} ${t.ref.name} gaat weg. Wat ernaar verwijst, werkt misschien niet meer.`;
  }
}

// Bij deze soorten tik je de naam in: weg is weg, met de gegevens.
const typeName = ["Namespace", "PersistentVolumeClaim", "PersistentVolume"];

function DeleteDialog(p: DialogProps) {
  const { clusterId, target, onClose } = p;
  const prod = useConfirm(clusterId);
  const { run, pending, error } = useRun(p);
  const [typed, setTyped] = useState("");
  const needName = typeName.includes(target.kind);
  const system = systemNamespaces.includes(target.ref.namespace);
  const what = kubectlObject(target.ref, target.kind);
  return (
    <ActionDialog
      title={`${target.kind} ${target.ref.name} weghalen`}
      submit="Weghalen"
      danger
      ready={!needName || typed === target.ref.name}
      pending={pending}
      error={error}
      prod={prod}
      onClose={onClose}
      onSubmit={() => run({ action: "delete" })}
      commands={[kubectl(`delete ${what}`)]}
      learn={
        <p>
          Kubernetes haalt eerst het object weg en daarna, op de achtergrond, wat het maakte: de ReplicaSets en pods van een
          Deployment bijvoorbeeld. Een pod krijgt even de tijd om netjes te stoppen.
        </p>
      }
    >
      <p>{deleteEffect(target)}</p>
      {system && (
        <Alert>
          Dit hoort bij Kubernetes zelf ({target.ref.namespace}). Weghalen kan het cluster stukmaken; k3s zet zijn eigen
          onderdelen wel opnieuw neer als het herstart.
        </Alert>
      )}
      {needName && (
        <Field label={`Tik ${target.ref.name} om te bevestigen`} htmlFor="kube-delete-name">
          <Input
            id="kube-delete-name"
            autoComplete="off"
            spellCheck={false}
            className="font-mono"
            value={typed}
            onChange={(e) => setTyped(e.target.value.trim())}
          />
        </Field>
      )}
    </ActionDialog>
  );
}

function CordonDialog(p: DialogProps & { action: "cordon" | "uncordon" }) {
  const { clusterId, target, onClose, action } = p;
  const prod = useConfirm(clusterId);
  const { run, pending, error } = useRun(p);
  const node = target.ref.name;
  return (
    <ActionDialog
      title={action === "cordon" ? `${node} op cordon zetten` : `${node} weer pods geven`}
      submit={action === "cordon" ? "Cordon" : "Uncordon"}
      pending={pending}
      error={error}
      prod={prod}
      onClose={onClose}
      onSubmit={() => run({ action })}
      commands={[kubectl(`${action} ${node}`), kubectl("get nodes")]}
      learn={
        <p>
          Cordon zet <code>spec.unschedulable</code> op de node. De planner slaat hem dan over; kubectl toont{" "}
          <code>SchedulingDisabled</code> bij de status.
        </p>
      }
    >
      {action === "cordon" ? (
        <p>
          Er komen geen nieuwe pods meer op deze node. Wat er al draait, blijft draaien; om ook die pods te verhuizen, kies je
          Leegmaken.
        </p>
      ) : (
        <p>
          De node krijgt weer nieuwe pods. Pods die intussen op een andere node draaien, komen niet vanzelf terug; dat gebeurt pas
          als ze vervangen worden, bijvoorbeeld bij een herstart of een nieuwe versie.
        </p>
      )}
    </ActionDialog>
  );
}

function PodList({ title, pods, tone }: { title: string; pods: KubeDrainPod[]; tone: "blue" | "slate" | "red" }) {
  if (pods.length === 0) return null;
  return (
    <div>
      <p className="mb-1 font-medium">
        {title} <Badge tone={tone}>{pods.length}</Badge>
      </p>
      <ul className="max-h-48 space-y-1 overflow-y-auto rounded-md border border-slate-200 px-3 py-2 dark:border-slate-800">
        {pods.map((d) => (
          <li key={`${d.namespace}/${d.name}`} className="text-xs">
            <span className="font-mono break-all">
              {d.namespace}/{d.name}
            </span>
            {d.owner_kind && (
              <span className="text-slate-500">
                {" "}
                · {d.owner_kind} {d.owner_name}
              </span>
            )}
            {d.reason && <span className="block text-slate-600 dark:text-slate-400">{d.reason}</span>}
          </li>
        ))}
      </ul>
    </div>
  );
}

function DrainDialog({ clusterId, target, onClose, onDone }: DialogProps) {
  const node = target.ref.name;
  const prod = useConfirm(clusterId);
  const [force, setForce] = useState(false);
  const plan = useKubeDrainPlan(clusterId, node, force);
  const drain = useKubeDrain(clusterId);
  const pl = plan.data;
  const blocked = pl ? pl.blocked.length > 0 : true;
  return (
    <ActionDialog
      title={`${node} leegmaken`}
      submit="Leegmaken"
      ready={pl !== undefined && !blocked}
      pending={drain.isPending}
      error={drain.error ?? (pl ? null : plan.error)}
      prod={prod}
      wide
      onClose={onClose}
      onSubmit={() =>
        drain.mutate(
          { node, force, confirm: prod.confirm },
          {
            onSuccess: (job) =>
              onDone({ message: `${node} wordt leeggemaakt: eerst cordon, dan gaan de pods een voor een weg.`, jobId: job.id }),
          },
        )
      }
      commands={[
        kubectl(`drain ${node} --ignore-daemonsets --delete-emptydir-data${force ? " --force" : ""}`),
        kubectl(`uncordon ${node}`),
      ]}
      learn={
        <>
          <p>
            Leegmaken zet de node op cordon en vraagt dan elke pod netjes te vertrekken (een eviction). De eigenaar van de pod
            maakt een nieuwe op een andere node. Een PodDisruptionBudget kan dat even tegenhouden, zodat er genoeg kopieën blijven
            draaien; ClusterForge probeert het dan om de vijf seconden opnieuw, tot vijf minuten lang.
          </p>
          <p>Na het onderhoud geeft Uncordon de node weer pods. Een herstart via ClusterForge doet beide vanzelf.</p>
        </>
      }
    >
      {plan.isLoading && <p className="text-slate-500">Kijken welke pods op {node} draaien…</p>}
      {pl && (
        <>
          <p>
            {pl.evict.length === 0
              ? `Er moeten geen pods weg van ${node}; hij gaat alleen op cordon.`
              : `${pl.evict.length} ${pl.evict.length === 1 ? "pod gaat" : "pods gaan"} weg van ${node} en ${
                  pl.evict.length === 1 ? "komt" : "komen"
                } terug op een andere node.`}
            {pl.cordoned && " De node staat al op cordon."}
          </p>
          <PodList title="Gaan weg" pods={pl.evict} tone="blue" />
          <PodList title="Houden het tegen" pods={pl.blocked} tone="red" />
          <PodList title="Blijven staan" pods={pl.skip} tone="slate" />
          {(pl.blocked.length > 0 || force) && (
            <label className="flex items-start gap-2">
              <input type="checkbox" className="mt-0.5" checked={force} onChange={(e) => setForce(e.target.checked)} />
              <span>
                Toch doorgaan
                <span className="block text-xs text-slate-500">
                  Ook pods zonder eigenaar en pods met emptyDir gaan weg. Zonder eigenaar komen ze niet terug, en de gegevens in
                  emptyDir zijn weg.
                </span>
              </span>
            </label>
          )}
        </>
      )}
    </ActionDialog>
  );
}
