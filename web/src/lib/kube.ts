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
};

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
