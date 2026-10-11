"use client";

import type { ReactNode } from "react";
import { upgradeCommand } from "@/components/drift/Drift";
import { CopyBlock } from "@/components/inventory/InstallAgent";
import { Alert, Badge, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { formatBytes } from "@/components/inventory/bits";
import { formatCPU, percent, useKubeLearning, type KubeUsage } from "@/lib/kube";

// sentence maakt van een foutmelding van de server een zin.
function sentence(msg: string) {
  const s = msg.charAt(0).toUpperCase() + msg.slice(1);
  return /[.!?]$/.test(s) ? s : `${s}.`;
}

// KubeError zegt wat er mis is met de brug naar de API, en wat eraan te
// doen is. Een te oude agent krijgt het commando om hem bij te werken.
export function KubeError({ error }: { error: unknown }) {
  if (error instanceof ApiError && error.code === "agent_too_old") {
    return (
      <div className="space-y-2 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200">
        <p>
          <span className="font-medium">Agent te oud voor Kubernetes.</span> {sentence(error.message)} Voer dit uit op de server;
          de aanmelding blijft staan.
        </p>
        <CopyBlock text={upgradeCommand()} />
      </div>
    );
  }
  if (error instanceof ApiError && error.code === "kube_unavailable") {
    return (
      <Alert>
        <span className="font-medium">De Kubernetes-API is niet bereikbaar.</span> {sentence(error.message)} ClusterForge vraagt
        de API via cf-agent op een server van het cluster; kijk of k3s daar draait met <code>sudo systemctl status k3s</code>.
      </Alert>
    );
  }
  if (error instanceof ApiError && error.code === "kube_api") {
    return (
      <Alert>
        <span className="font-medium">De Kubernetes-API gaf een fout.</span> {sentence(error.message)}
      </Alert>
    );
  }
  return <Alert>{error instanceof Error ? error.message : "Laden mislukt"}</Alert>;
}

// Learn is een uitlegblok van de leermodus: wat je ziet, en hoe je
// hetzelfde met kubectl doet. Zonder leermodus toont het niets.
export function Learn({
  title = "Wat is dit?",
  children,
  commands,
}: {
  title?: string;
  children: ReactNode;
  commands?: string[];
}) {
  const learning = useKubeLearning();
  if (!learning.on) return null;
  return (
    <aside className="space-y-2 rounded-md border border-sky-200 bg-sky-50 px-4 py-3 text-sm text-sky-950 dark:border-sky-900 dark:bg-sky-950/40 dark:text-sky-100">
      <p className="font-medium">{title}</p>
      <div className="space-y-2 leading-relaxed">{children}</div>
      {commands && commands.length > 0 && (
        <div className="space-y-1">
          <p className="text-xs text-sky-800 dark:text-sky-300">Zelf proberen op een server van het cluster:</p>
          <CopyBlock text={commands.join("\n")} />
        </div>
      )}
    </aside>
  );
}

// LearnToggle zet de leermodus aan of uit; de keuze blijft bewaard.
export function LearnToggle() {
  const learning = useKubeLearning();
  return (
    <label className="inline-flex cursor-pointer items-center gap-2 text-sm text-slate-700 dark:text-slate-300">
      <input
        type="checkbox"
        className="size-4 accent-sky-600"
        checked={learning.on}
        onChange={(e) => learning.set(e.target.checked)}
      />
      Leermodus
    </label>
  );
}

// Term geeft een Kubernetes-woord een uitleg bij het aanwijzen, in de
// leermodus met een stippellijn eronder.
export function Term({ children, help }: { children: ReactNode; help: string }) {
  const learning = useKubeLearning();
  return (
    <span title={help} className={cx(learning.on && "cursor-help underline decoration-dotted underline-offset-2")}>
      {children}
    </span>
  );
}

// UsageBar toont het gebruik tegenover wat er toegewezen kan worden.
export function UsageBar({
  label,
  used,
  total,
  format,
}: {
  label: string;
  used: number | null;
  total: number;
  format: (n: number) => string;
}) {
  const p = used === null ? 0 : percent(used, total);
  return (
    <div className="space-y-1">
      <div className="flex justify-between gap-2 text-xs text-slate-600 dark:text-slate-400">
        <span>{label}</span>
        <span>{used === null ? `${format(total)} beschikbaar` : `${format(used)} van ${format(total)} (${p}%)`}</span>
      </div>
      <div className="h-2 overflow-hidden rounded bg-slate-100 dark:bg-slate-800">
        <div
          className={cx("h-full", p >= 90 ? "bg-red-500" : p >= 75 ? "bg-amber-500" : "bg-emerald-500")}
          style={{ width: `${used === null ? 0 : Math.max(p, 1)}%` }}
        />
      </div>
    </div>
  );
}

export function Usage({ allocatable, used }: { allocatable: KubeUsage; used: KubeUsage | null }) {
  return (
    <div className="space-y-3">
      <UsageBar label="CPU" used={used?.cpu_millis ?? null} total={allocatable.cpu_millis} format={formatCPU} />
      <UsageBar label="Geheugen" used={used?.memory_bytes ?? null} total={allocatable.memory_bytes} format={formatBytes} />
    </div>
  );
}

export function ReadyBadge({ ready, total }: { ready: number; total: number }) {
  return (
    <Badge tone={total === 0 ? "slate" : ready === total ? "green" : ready === 0 ? "red" : "amber"}>
      {ready}/{total}
    </Badge>
  );
}

// Stat is een getal met een label, voor de bovenkant van het overzicht.
export function Stat({
  label,
  value,
  note,
  tone,
}: {
  label: ReactNode;
  value: ReactNode;
  note?: ReactNode;
  tone?: "red" | "amber";
}) {
  return (
    <div className="rounded-lg border border-slate-200 bg-white px-4 py-3 dark:border-slate-800 dark:bg-slate-900">
      <div className="text-xs font-medium uppercase tracking-wide text-slate-500">{label}</div>
      <div
        className={cx(
          "mt-1 text-2xl font-semibold",
          tone === "red" && "text-red-600 dark:text-red-400",
          tone === "amber" && "text-amber-600 dark:text-amber-400",
        )}
      >
        {value}
      </div>
      {note && <div className="mt-0.5 text-xs text-slate-500">{note}</div>}
    </div>
  );
}
