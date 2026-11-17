"use client";

import { useEffect, useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { Empty, formatDuration, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Modal } from "@/components/Modal";
import { usePbsRunner } from "@/components/pbs/bits";
import { Badge, Button, cx } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { pbsTaskFailed, pbsTaskLabel, pbsWorkerID, usePbsGet, type PbsTask } from "@/lib/pbs";

const timeFmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "short", timeStyle: "medium" });

export function PbsTaskStatus({ t }: { t: PbsTask }) {
  if (t.endtime === undefined || t.endtime === null) return <Badge tone="blue">Loopt</Badge>;
  if (t.status === "OK") return <Badge tone="green">OK</Badge>;
  if (t.status?.startsWith("WARNINGS")) return <Badge tone="amber">{t.status.replace("WARNINGS:", "Waarschuwingen:")}</Badge>;
  return <Badge tone="red">{t.status || "Mislukt"}</Badge>;
}

// PbsTaskTable toont taken van PBS; een klik opent het log.
export function PbsTaskTable({ id, tasks, compact = false }: { id: string; tasks: PbsTask[]; compact?: boolean }) {
  const [open, setOpen] = useState<PbsTask | null>(null);
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000));
  useEffect(() => {
    const t = setInterval(() => setNow(Math.floor(Date.now() / 1000)), 5_000);
    return () => clearInterval(t);
  }, []);
  if (tasks.length === 0) return <Empty>Geen taken.</Empty>;
  return (
    <>
      <div className="relative -mx-4 overflow-x-auto sm:mx-0">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Begin</th>
              <th className={thClass}>Taak</th>
              {!compact && <th className={cx(thClass, "hidden md:table-cell")}>Door</th>}
              <th className={thClass}>Duur</th>
              <th className={thClass}>Uitkomst</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {tasks.map((t) => (
              <tr
                key={t.upid}
                className={cx("cursor-pointer hover:bg-slate-50 dark:hover:bg-slate-900", pbsTaskFailed(t) && "bg-red-50/40 dark:bg-red-950/20")}
                onClick={() => setOpen(t)}
              >
                <td className={cx(tdClass, "whitespace-nowrap text-slate-500 tabular-nums")}>{timeFmt.format(new Date(t.starttime * 1000))}</td>
                <td className={tdClass}>
                  <button type="button" className="text-left font-medium hover:underline" onClick={() => setOpen(t)}>
                    {pbsTaskLabel(t.worker_type)}
                    {t.worker_id ? <span className="ml-1 font-mono text-xs text-slate-500">{pbsWorkerID(t.worker_id)}</span> : null}
                  </button>
                </td>
                {!compact && <td className={cx(tdClass, "hidden text-slate-500 md:table-cell")}>{t.user}</td>}
                <td className={cx(tdClass, "whitespace-nowrap tabular-nums")}>{formatDuration(Math.max(0, (t.endtime ?? now) - t.starttime))}</td>
                <td className={tdClass}>
                  <PbsTaskStatus t={t} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {open && <PbsTaskLog id={id} task={open} onClose={() => setOpen(null)} />}
    </>
  );
}

export function PbsTaskLog({ id, task, onClose }: { id: string; task: PbsTask; onClose: () => void }) {
  const running = task.endtime === undefined || task.endtime === null;
  const base = `/nodes/localhost/tasks/${task.upid}`;
  const log = usePbsGet<{ n: number; t: string }[]>(id, base + "/log", { params: { limit: "5000" }, refetch: running ? 2_000 : false });
  const status = usePbsGet<{ status: string; exitstatus?: string }>(id, base + "/status", { refetch: running ? 2_000 : false });
  const isAdmin = useIsAdmin();
  const { run, pending, banner } = usePbsRunner(id);
  const live = status.data ? status.data.status === "running" : running;
  return (
    <Modal title={`${pbsTaskLabel(task.worker_type)}${task.worker_id ? " " + pbsWorkerID(task.worker_id) : ""}`} onClose={onClose} wide>
      <div className="space-y-3">
        {banner}
        <div className="flex flex-wrap items-center gap-2 text-sm text-slate-500">
          <span>{timeFmt.format(new Date(task.starttime * 1000))}</span>
          <span>· {task.user}</span>
          {status.data && !live && (
            <Badge tone={status.data.exitstatus === "OK" ? "green" : status.data.exitstatus?.startsWith("WARNINGS") ? "amber" : "red"}>
              {status.data.exitstatus}
            </Badge>
          )}
          {live && <Badge tone="blue">Loopt</Badge>}
        </div>
        <QueryState q={log}>
          <pre className="max-h-[60vh] overflow-auto rounded-md bg-slate-950 p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap text-slate-100">
            {(log.data ?? []).map((l) => l.t).join("\n") || "Geen uitvoer."}
          </pre>
        </QueryState>
        <div className="flex justify-between gap-2">
          <div>
            {isAdmin && live && (
              <Button
                variant="secondary-danger"
                disabled={pending}
                onClick={async () => {
                  if (await askConfirm("Deze PBS-taak stoppen?", { danger: true, confirmLabel: "Taak stoppen" })) void run({ method: "delete", path: base });
                }}
              >
                Taak stoppen
              </Button>
            )}
          </div>
          <Button variant="secondary" onClick={onClose}>
            Sluiten
          </Button>
        </div>
      </div>
    </Modal>
  );
}
