"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Job } from "./proxmox";
import { call } from "./replace";

// Fase 48 (4.2): een mislukte uitrol opruimen, of opnieuw met andere
// instellingen. Buiten de OpenAPI; de types volgen internal/deploy/abandon.go.

export type FailedNode = {
  id: string;
  hostname: string;
  address?: string;
  role: string;
  lifecycle: string;
  host?: string;
  vmid?: number;
  existing: boolean;
  gone: boolean;
};

// DeployRequest is de aanvraag zoals de wizard hem stuurt, zonder geheimen.
export type DeployRequest = {
  template: string;
  version?: string;
  cluster: { name: string; slug: string; environment: string };
  params: Record<string, unknown>;
  existing_nodes?: { node_id: string; role: string }[];
  target?: {
    proxmox_id: string;
    image_vmid?: number;
    image_type?: string;
    storage?: string;
    bridge?: string;
    vlan?: number | null;
    network?: string;
    first_ip?: string;
    gateway?: string;
    dns?: string[] | null;
    ssh_keys?: string;
    placement?: {
      role: string;
      index: number;
      host?: string;
      storage?: string;
      vmid?: number;
      address?: string;
      cpu?: number;
      memory_mib?: number;
      disk_gib?: number;
    }[] | null;
    create?: string;
    ostemplate?: string;
    privileged?: boolean;
  };
  server_url?: string;
};

export type FailedDeploy = {
  job_id: string;
  status: string;
  error: string;
  cluster_id: string;
  cluster_name: string;
  slug: string;
  vip?: string;
  cluster_exists: boolean;
  nodes: FailedNode[];
  can_clean: boolean;
  why?: string;
  cleanup_job_id?: string;
  request: DeployRequest;
  secrets: string[];
};

export function useFailedDeploy(jobId: string, enabled = true) {
  return useQuery({
    queryKey: ["deploy-failed", jobId],
    queryFn: () => call<FailedDeploy>("GET", `/deploy/failed/${jobId}`),
    enabled: !!jobId && enabled,
    retry: false,
  });
}

export function useCleanupFailedDeploy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (b: { jobId: string; confirm: string; guests: boolean }) =>
      call<Job>("POST", `/deploy/failed/${b.jobId}/cleanup`, { confirm: b.confirm, guests: b.guests }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["deploy-failed"] });
      void qc.invalidateQueries({ queryKey: ["jobs"] });
      void qc.invalidateQueries({ queryKey: ["clusters"] });
      void qc.invalidateQueries({ queryKey: ["nodes"] });
    },
  });
}
