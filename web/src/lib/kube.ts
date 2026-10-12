"use client";

import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Me } from "./api/client";
import type { components } from "./api/schema";
import { meQueryKey, useMe } from "./auth";
import { liveInterval } from "./inventory";

export type KubeClusterSummary = components["schemas"]["KubeClusterSummary"];
export type KubeOverview = components["schemas"]["KubeOverview"];
export type KubeNode = components["schemas"]["KubeNode"];
export type KubeWorkload = components["schemas"]["KubeWorkload"];
export type KubePod = components["schemas"]["KubePod"];
export type KubeEvent = components["schemas"]["KubeEvent"];
export type KubeUsage = components["schemas"]["KubeUsage"];
export type KubeRef = components["schemas"]["KubeRef"];
export type KubeObject = components["schemas"]["KubeObject"];
export type KubeChainLink = components["schemas"]["KubeChainLink"];
export type KubeContainer = components["schemas"]["KubeContainer"];
export type KubeResourceType = components["schemas"]["KubeResourceType"];
export type KubeNetwork = components["schemas"]["KubeNetwork"];
export type KubeStorage = components["schemas"]["KubeStorage"];
export type KubeConfig = components["schemas"]["KubeConfig"];
export type KubeActionName = components["schemas"]["KubeActionName"];
export type KubeActionInput = components["schemas"]["KubeActionInput"];
export type KubeRevision = components["schemas"]["KubeRevision"];
export type KubeDrainPlan = components["schemas"]["KubeDrainPlan"];
export type KubeDrainPod = components["schemas"]["KubeDrainPod"];
type Job = components["schemas"]["Job"];

type Tone = "slate" | "green" | "amber" | "red" | "blue";

// Het dashboard leest live via de agent; elke vijf seconden is vaak genoeg
// om een pod te zien starten zonder de API te belasten.
export const kubeInterval = 5_000;

const keys = {
  clusters: ["kube", "clusters"] as const,
  overview: (id: string) => ["kube", id, "overview"] as const,
  nodes: (id: string) => ["kube", id, "nodes"] as const,
  workloads: (id: string, ns: string, system: boolean) => ["kube", id, "workloads", ns, system] as const,
  pods: (id: string, ns: string, system: boolean, node: string) => ["kube", id, "pods", ns, system, node] as const,
  list: (id: string, what: string, ns: string, system: boolean) => ["kube", id, what, ns, system] as const,
  resources: (id: string) => ["kube", id, "resources"] as const,
  objects: (id: string, g: string, v: string, r: string, ns: string, system: boolean) =>
    ["kube", id, "objects", g, v, r, ns, system] as const,
  object: (id: string, ref: KubeRef) => ["kube", id, "object", ref] as const,
  logs: (id: string, ns: string, pod: string, o: LogOptions) => ["kube", id, "logs", ns, pod, o] as const,
  revisions: (id: string, ns: string, name: string) => ["kube", id, "revisions", ns, name] as const,
  drainPlan: (id: string, node: string, force: boolean) => ["kube", id, "drain-plan", node, force] as const,
};

// scope is de query van een tabblad met een namespace en het vinkje
// Systeem tonen.
function scope(namespace: string, system: boolean) {
  return { namespace: namespace || undefined, system: system || undefined };
}

export function useKubeClusters() {
  return useQuery({
    queryKey: keys.clusters,
    queryFn: async () => unwrap(await api.GET("/kubernetes/clusters")).items,
    refetchInterval: liveInterval,
  });
}

// Een 503 of 502 van de brug wordt niet herhaald: de volgende ronde van het
// verversen probeert opnieuw, en de fout staat meteen op het scherm.
export function useKubeOverview(id: string) {
  return useQuery({
    queryKey: keys.overview(id),
    queryFn: async () => unwrap(await api.GET("/kubernetes/clusters/{clusterId}", { params: { path: { clusterId: id } } })),
    enabled: id !== "",
    refetchInterval: kubeInterval,
    retry: false,
  });
}

export function useKubeNodes(id: string, enabled = true) {
  return useQuery({
    queryKey: keys.nodes(id),
    queryFn: async () =>
      unwrap(await api.GET("/kubernetes/clusters/{clusterId}/nodes", { params: { path: { clusterId: id } } })).items,
    enabled: enabled && id !== "",
    refetchInterval: kubeInterval,
    retry: false,
  });
}

export function useKubeWorkloads(id: string, namespace: string, system: boolean, enabled = true) {
  return useQuery({
    queryKey: keys.workloads(id, namespace, system),
    queryFn: async () =>
      unwrap(
        await api.GET("/kubernetes/clusters/{clusterId}/workloads", {
          params: { path: { clusterId: id }, query: { namespace: namespace || undefined, system: system || undefined } },
        }),
      ).items,
    enabled: enabled && id !== "",
    refetchInterval: kubeInterval,
    placeholderData: keepPreviousData,
    retry: false,
  });
}

export function useKubePods(id: string, namespace: string, system: boolean, node: string, enabled = true) {
  return useQuery({
    queryKey: keys.pods(id, namespace, system, node),
    queryFn: async () =>
      unwrap(
        await api.GET("/kubernetes/clusters/{clusterId}/pods", {
          params: {
            path: { clusterId: id },
            query: { namespace: namespace || undefined, system: system || undefined, node: node || undefined },
          },
        }),
      ).items,
    enabled: enabled && id !== "",
    refetchInterval: kubeInterval,
    placeholderData: keepPreviousData,
    retry: false,
  });
}

export function useKubeNetwork(id: string, namespace: string, system: boolean) {
  return useQuery({
    queryKey: keys.list(id, "network", namespace, system),
    queryFn: async () =>
      unwrap(
        await api.GET("/kubernetes/clusters/{clusterId}/network", {
          params: { path: { clusterId: id }, query: scope(namespace, system) },
        }),
      ),
    enabled: id !== "",
    refetchInterval: kubeInterval,
    placeholderData: keepPreviousData,
    retry: false,
  });
}

export function useKubeStorage(id: string, namespace: string, system: boolean) {
  return useQuery({
    queryKey: keys.list(id, "storage", namespace, system),
    queryFn: async () =>
      unwrap(
        await api.GET("/kubernetes/clusters/{clusterId}/storage", {
          params: { path: { clusterId: id }, query: scope(namespace, system) },
        }),
      ),
    enabled: id !== "",
    refetchInterval: kubeInterval,
    placeholderData: keepPreviousData,
    retry: false,
  });
}

export function useKubeConfig(id: string, namespace: string, system: boolean) {
  return useQuery({
    queryKey: keys.list(id, "config", namespace, system),
    queryFn: async () =>
      unwrap(
        await api.GET("/kubernetes/clusters/{clusterId}/config", {
          params: { path: { clusterId: id }, query: scope(namespace, system) },
        }),
      ),
    enabled: id !== "",
    refetchInterval: kubeInterval,
    placeholderData: keepPreviousData,
    retry: false,
  });
}

export function useKubeEvents(id: string, namespace: string, system: boolean, warnings: boolean) {
  return useQuery({
    queryKey: [...keys.list(id, "events", namespace, system), warnings],
    queryFn: async () =>
      unwrap(
        await api.GET("/kubernetes/clusters/{clusterId}/events", {
          params: { path: { clusterId: id }, query: { ...scope(namespace, system), warnings: warnings || undefined } },
        }),
      ).items,
    enabled: id !== "",
    refetchInterval: kubeInterval,
    placeholderData: keepPreviousData,
    retry: false,
  });
}

// De soorten veranderen zelden: alleen als je een app met eigen soorten
// installeert.
export function useKubeResources(id: string) {
  return useQuery({
    queryKey: keys.resources(id),
    queryFn: async () =>
      unwrap(await api.GET("/kubernetes/clusters/{clusterId}/resources", { params: { path: { clusterId: id } } })).items,
    enabled: id !== "",
    staleTime: 60_000,
    retry: false,
  });
}

export function useKubeObjects(id: string, type: KubeResourceType | undefined, namespace: string, system: boolean) {
  return useQuery({
    queryKey: keys.objects(id, type?.group ?? "", type?.version ?? "", type?.resource ?? "", namespace, system),
    queryFn: async () =>
      unwrap(
        await api.GET("/kubernetes/clusters/{clusterId}/objects", {
          params: {
            path: { clusterId: id },
            query: {
              group: type!.group || undefined,
              version: type!.version,
              resource: type!.resource,
              ...scope(type!.namespaced ? namespace : "", system),
            },
          },
        }),
      ).items,
    enabled: id !== "" && type !== undefined,
    refetchInterval: kubeInterval,
    placeholderData: keepPreviousData,
    retry: false,
  });
}

export function useKubeObject(id: string, ref: KubeRef, enabled = true) {
  return useQuery({
    queryKey: keys.object(id, ref),
    queryFn: async () =>
      unwrap(
        await api.GET("/kubernetes/clusters/{clusterId}/object", {
          params: {
            path: { clusterId: id },
            query: {
              group: ref.group || undefined,
              version: ref.version,
              resource: ref.resource,
              namespace: ref.namespace || undefined,
              name: ref.name,
            },
          },
        }),
      ),
    enabled: enabled && id !== "" && ref.name !== "" && ref.resource !== "",
    refetchInterval: kubeInterval,
    retry: false,
  });
}

export type LogOptions = { container: string; previous: boolean; lines: number; timestamps: boolean };

// useKubeLogs haalt de log van een container; follow ververst hem zoals
// kubectl logs -f, maar elke vijf seconden.
export function useKubeLogs(id: string, namespace: string, pod: string, o: LogOptions, follow: boolean) {
  return useQuery({
    queryKey: keys.logs(id, namespace, pod, o),
    queryFn: async () =>
      unwrap(
        await api.GET("/kubernetes/clusters/{clusterId}/logs", {
          params: {
            path: { clusterId: id },
            query: {
              namespace,
              pod,
              container: o.container || undefined,
              previous: o.previous || undefined,
              lines: o.lines,
              timestamps: o.timestamps || undefined,
            },
          },
        }),
      ),
    enabled: id !== "" && pod !== "",
    refetchInterval: follow ? kubeInterval : false,
    placeholderData: keepPreviousData,
    retry: false,
  });
}

// useKubeAction voert een actie uit, zoals schalen of cordon. Daarna wordt
// alles van het cluster opnieuw gelezen, zodat je het effect meteen ziet.
export function useKubeAction(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: KubeActionInput) =>
      unwrap(await api.POST("/kubernetes/clusters/{clusterId}/actions", { params: { path: { clusterId: id } }, body })),
    // Niet wachten op het opnieuw lezen: na weghalen zou de pagina van het
    // object eerst even een 404 tonen.
    onSettled: () => void qc.invalidateQueries({ queryKey: ["kube", id] }),
  });
}

// useKubeRevisions geeft de versies van een Deployment, de nieuwste eerst.
export function useKubeRevisions(id: string, namespace: string, name: string, enabled = true) {
  return useQuery({
    queryKey: keys.revisions(id, namespace, name),
    queryFn: async () =>
      unwrap(
        await api.GET("/kubernetes/clusters/{clusterId}/revisions", {
          params: { path: { clusterId: id }, query: { namespace, name } },
        }),
      ).items,
    enabled: enabled && id !== "" && name !== "",
    retry: false,
  });
}

// useKubeDrainPlan toont wat leegmaken met een node zou doen, zonder iets
// te veranderen; het plan volgt de pods zolang de dialoog open is.
export function useKubeDrainPlan(id: string, node: string, force: boolean, enabled = true) {
  return useQuery({
    queryKey: keys.drainPlan(id, node, force),
    queryFn: async () =>
      unwrap(
        await api.POST("/kubernetes/clusters/{clusterId}/drain", {
          params: { path: { clusterId: id } },
          body: { node, preview: true, force: force || undefined },
        }),
      ) as KubeDrainPlan,
    enabled: enabled && id !== "" && node !== "",
    refetchInterval: kubeInterval,
    placeholderData: keepPreviousData,
    retry: false,
  });
}

// useKubeDrain start het leegmaken als taak.
export function useKubeDrain(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: { node: string; force: boolean; confirm?: string }) =>
      unwrap(
        await api.POST("/kubernetes/clusters/{clusterId}/drain", {
          params: { path: { clusterId: id } },
          body: { node: body.node, force: body.force || undefined, confirm: body.confirm },
        }),
      ) as Job,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["jobs"] });
      void qc.invalidateQueries({ queryKey: ["kube", id] });
    },
  });
}

// objectHref is de pagina van één object.
export function objectHref(clusterId: string, ref: KubeRef) {
  const q = new URLSearchParams({ id: clusterId });
  if (ref.group) q.set("g", ref.group);
  q.set("v", ref.version);
  q.set("r", ref.resource);
  if (ref.namespace) q.set("ns", ref.namespace);
  q.set("name", ref.name);
  return `/kubernetes/object?${q.toString()}`;
}

// De soorten die het dashboard zelf toont, met hun plaats in de API.
const builtin: Record<string, { group: string; version: string; resource: string }> = {
  Pod: { group: "", version: "v1", resource: "pods" },
  Node: { group: "", version: "v1", resource: "nodes" },
  Service: { group: "", version: "v1", resource: "services" },
  ConfigMap: { group: "", version: "v1", resource: "configmaps" },
  Secret: { group: "", version: "v1", resource: "secrets" },
  PersistentVolumeClaim: { group: "", version: "v1", resource: "persistentvolumeclaims" },
  PersistentVolume: { group: "", version: "v1", resource: "persistentvolumes" },
  Deployment: { group: "apps", version: "v1", resource: "deployments" },
  StatefulSet: { group: "apps", version: "v1", resource: "statefulsets" },
  DaemonSet: { group: "apps", version: "v1", resource: "daemonsets" },
  ReplicaSet: { group: "apps", version: "v1", resource: "replicasets" },
  Job: { group: "batch", version: "v1", resource: "jobs" },
  CronJob: { group: "batch", version: "v1", resource: "cronjobs" },
  Ingress: { group: "networking.k8s.io", version: "v1", resource: "ingresses" },
  StorageClass: { group: "storage.k8s.io", version: "v1", resource: "storageclasses" },
};

// refOf wijst een object van een bekende soort aan.
export function refOf(kind: string, namespace: string, name: string): KubeRef {
  const b = builtin[kind] ?? { group: "", version: "v1", resource: `${kind.toLowerCase()}s` };
  return { ...b, namespace, name };
}

// kubectlType is hoe je de soort aan kubectl geeft: deployment, of voor
// een soort van een app de volledige naam, zoals ingressroutes.traefik.io.
export function kubectlType(ref: Pick<KubeRef, "group" | "resource">, kind: string) {
  if (ref.group === "" || !ref.group.includes(".") || ref.group.endsWith(".k8s.io")) return kind.toLowerCase();
  return `${ref.resource}.${ref.group}`;
}

// kubectlObject is soort, naam en namespace voor een kubectl-commando.
export function kubectlObject(ref: KubeRef, kind: string) {
  return `${kubectlType(ref, kind)} ${ref.name}${ref.namespace ? ` -n ${ref.namespace}` : ""}`;
}

// useKubeLearning geeft de leermodus van de ingelogde gebruiker en een
// functie om hem aan of uit te zetten. Hij geldt voor elke browser.
export function useKubeLearning() {
  const me = useMe();
  const qc = useQueryClient();
  const put = (on: boolean) =>
    qc.setQueryData<Me | null>(meQueryKey, (old) => (old ? { ...old, user: { ...old.user, kube_learning: on } } : old));
  // De schakelaar verspringt meteen; lukt het bewaren niet, dan springt hij terug.
  const set = useMutation({
    mutationFn: async (on: boolean) => unwrap(await api.PUT("/auth/preferences", { body: { kube_learning: on } })),
    onMutate: async (on) => {
      // Een /auth/me die nog onderweg is, zou de schakelaar terugzetten.
      await qc.cancelQueries({ queryKey: meQueryKey });
      put(on);
    },
    onError: (_err, on) => put(!on),
  });
  return { on: me.data?.user.kube_learning ?? true, set: (on: boolean) => set.mutate(on) };
}

export const stateInfo: Record<KubeWorkload["state"], { label: string; tone: Tone }> = {
  ready: { label: "Klaar", tone: "green" },
  rolling: { label: "Wordt bijgewerkt", tone: "blue" },
  degraded: { label: "Niet alles draait", tone: "amber" },
  stopped: { label: "Gestopt", tone: "slate" },
  failed: { label: "Mislukt", tone: "red" },
  running: { label: "Bezig", tone: "blue" },
  suspended: { label: "Gepauzeerd", tone: "slate" },
  scheduled: { label: "Ingepland", tone: "green" },
};

// podTone kleurt de status van een pod zoals kubectl hem toont.
export function podTone(p: Pick<KubePod, "status" | "problem">): Tone {
  if (p.problem) return "red";
  if (p.status === "Running") return "green";
  if (p.status === "Completed" || p.status === "Succeeded") return "slate";
  return "amber";
}

// formatCPU toont millicores als cores, zoals 1,5 cores of 250 m.
export function formatCPU(millis: number) {
  if (millis < 1000) return `${millis} m`;
  return `${(millis / 1000).toLocaleString("nl-BE", { maximumFractionDigits: 1 })} ${millis === 1000 ? "core" : "cores"}`;
}

export function percent(used: number, total: number) {
  if (total <= 0) return 0;
  return Math.min(100, Math.round((used / total) * 100));
}

// kubectl maakt een commando voor een k3s-server; k3s zet kubectl daar
// klaar en leest de beheerdersconfig, dus met sudo.
export function kubectl(args: string) {
  return `sudo kubectl ${args}`;
}

// nsFlag is -n voor één namespace of -A voor alle.
export function nsFlag(namespace: string) {
  return namespace ? `-n ${namespace}` : "-A";
}

// Uitleg over de statussen die een beginner het vaakst ziet.
export const podStatusHelp: Record<string, string> = {
  Running: "De containers draaien.",
  Pending: "De pod wacht op een node met genoeg plaats, of het image wordt nog gedownload.",
  ContainerCreating: "Het image wordt gedownload en de container gemaakt.",
  CrashLoopBackOff:
    "De container start, crasht en Kubernetes probeert het steeds later opnieuw. Kijk in de logs waarom hij stopt.",
  ImagePullBackOff: "Het image kan niet gedownload worden: een tikfout in de naam, een tag die niet bestaat of geen toegang.",
  ErrImagePull: "Het image kan niet gedownload worden: een tikfout in de naam, een tag die niet bestaat of geen toegang.",
  Completed: "De pod deed zijn werk en stopte netjes, zoals een Job hoort te doen.",
  Error: "De container stopte met een fout.",
  OOMKilled: "De container gebruikte meer geheugen dan zijn limiet en werd gestopt.",
  Terminating: "De pod wordt gestopt.",
  Unknown: "De node van de pod antwoordt niet; Kubernetes weet niet hoe het met de pod gaat.",
};

export const kindHelp: Record<KubeWorkload["kind"], string> = {
  Deployment: "Houdt een aantal gelijke pods draaiend en vervangt ze stap voor stap bij een nieuwe versie. Voor de meeste apps.",
  StatefulSet: "Zoals een Deployment, maar elke pod houdt zijn naam en eigen opslag. Voor databanken.",
  DaemonSet: "Eén pod op elke node, zoals een agent voor logs of netwerk.",
  CronJob: "Start volgens een schema een Job, zoals een back-up elke nacht.",
  Job: "Draait een pod tot zijn werk af is en stopt dan.",
};

// Namespaces van Kubernetes zelf; ze staan standaard verborgen.
export const systemNamespaces = ["kube-system", "kube-public", "kube-node-lease"];

// age toont hoe oud iets is zoals kubectl: 45s, 12m, 3h, 5d.
export function age(iso: string | null | undefined, now = Date.now()) {
  if (!iso) return "–";
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  if (s < 120) return `${s}s`;
  if (s < 7200) return `${Math.floor(s / 60)}m`;
  if (s < 172800) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86400)}d`;
}

// roleLabel maakt van de rollen van een node server of agent, zoals k3s
// ze noemt.
export function roleLabel(roles: string[]) {
  return roles.includes("control-plane") || roles.includes("master") ? "server" : "agent";
}

// objectHelp legt een soort object uit, op de pagina van dat object.
export const objectHelp: Record<string, string> = {
  Pod: "Een pod is één of meer containers die samen op een node draaien, met een eigen IP-adres. Meestal maakt een ReplicaSet of een andere app hem; verdwijnt hij, dan komt er een nieuwe.",
  Deployment:
    "Een Deployment houdt een aantal gelijke pods draaiend. Bij een nieuwe versie maakt hij een nieuwe ReplicaSet en vervangt hij de pods stap voor stap.",
  ReplicaSet:
    "Een ReplicaSet houdt een vast aantal pods van één versie draaiend. Je maakt hem zelden zelf: een Deployment doet dat, met een nieuwe per versie.",
  StatefulSet:
    "Een StatefulSet is zoals een Deployment, maar elke pod houdt zijn naam (web-0, web-1) en zijn eigen volume. Voor databanken.",
  DaemonSet: "Een DaemonSet draait één pod op elke node, zoals ServiceLB van k3s of een agent voor logs.",
  Job: "Een Job draait pods tot hun werk af is. Lukt het niet, dan probeert hij het een paar keer opnieuw.",
  CronJob: "Een CronJob maakt volgens een schema een Job, zoals een back-up elke nacht.",
  Node: "Een node is een machine in het cluster. De kubelet daarop start de containers van de pods die Kubernetes er neerzet.",
  Service:
    "Een Service geeft een groep pods één vast adres en een naam in DNS. Hij kiest de pods op hun labels en verdeelt het verkeer over de pods die klaar zijn.",
  Ingress:
    "Een Ingress stuurt webverkeer op hostnaam en pad naar een Service. In k3s doet Traefik dat, op poort 80 en 443 van de nodes en dus van het VIP.",
  PersistentVolumeClaim:
    "Een PersistentVolumeClaim vraagt opslag voor een pod, zoals 1 GiB. Kubernetes bindt hem aan een volume; met local-path is dat een map op de schijf van één node.",
  PersistentVolume:
    "Een PersistentVolume is echte opslag. Met local-path is het een map op één node: een pod met dit volume kan alleen op die node draaien.",
  StorageClass: "Een StorageClass zegt hoe Kubernetes volumes maakt. In k3s is local-path de standaard: een map op de node.",
  ConfigMap:
    "Een ConfigMap bewaart instellingen als sleutels en waarden; een pod leest ze als omgevingsvariabelen of als bestanden.",
  Secret:
    "Een Secret is zoals een ConfigMap, maar voor wachtwoorden en sleutels. ClusterForge toont nooit de waarden, alleen de sleutels en hoe lang ze zijn.",
  Namespace:
    "Een namespace is een map voor objecten. Namen zijn uniek per namespace, en je kunt rechten en quota per namespace zetten.",
};

// serviceTypeHelp legt de soorten Services uit.
export const serviceTypeHelp: Record<string, string> = {
  ClusterIP: "Alleen bereikbaar binnen het cluster, op een vast intern adres en een naam in DNS.",
  NodePort: "Ook bereikbaar op een vaste poort (30000 tot 32767) van elke node.",
  LoadBalancer: "Krijgt een adres buiten het cluster. In k3s doet ServiceLB dat met de adressen van de nodes.",
  ExternalName: "Een naam in DNS die naar iets buiten het cluster wijst.",
};
