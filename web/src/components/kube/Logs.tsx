"use client";

import { useEffect, useRef, useState } from "react";
import { KubeError } from "@/components/kube/Kube";
import { Alert, Button, Select } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { kubectl, useKubeLogs, type KubeContainer } from "@/lib/kube";

const lineChoices = [100, 500, 1000, 5000];

// Logs toont de laatste regels log van een container, zoals kubectl logs,
// met de vorige container na een herstart.
export function Logs({
  clusterId,
  namespace,
  pod,
  containers,
}: {
  clusterId: string;
  namespace: string;
  pod: string;
  containers: KubeContainer[];
}) {
  // Standaard de eerste gewone container die herstartte, anders de eerste.
  const main = containers.filter((c) => !c.init);
  const first = main.find((c) => c.restarts > 0) ?? main[0] ?? containers[0];
  const [container, setContainer] = useState(first?.name ?? "");
  const [previous, setPrevious] = useState(false);
  const [lines, setLines] = useState(500);
  const [timestamps, setTimestamps] = useState(false);
  const [follow, setFollow] = useState(true);
  const logs = useKubeLogs(clusterId, namespace, pod, { container, previous, lines, timestamps }, follow && !previous);
  const current = containers.find((c) => c.name === container);

  // Volgen: blijf onderaan zolang je niet zelf omhoog scrolde.
  const box = useRef<HTMLPreElement>(null);
  const atBottom = useRef(true);
  const text = logs.data?.text;
  useEffect(() => {
    const el = box.current;
    if (el && atBottom.current) el.scrollTop = el.scrollHeight;
  }, [text]);

  const cmd = kubectl(
    `logs ${pod} -n ${namespace}${containers.length > 1 ? ` -c ${container}` : ""}${previous ? " --previous" : ""} --tail=${lines}${
      timestamps ? " --timestamps" : ""
    }${follow && !previous ? " -f" : ""}`,
  );
  const noPrevious = logs.error instanceof ApiError && logs.error.code === "not_found";

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-sm">
        {containers.length > 1 && (
          <Select className="w-auto" aria-label="Container" value={container} onChange={(e) => setContainer(e.target.value)}>
            {containers.map((c) => (
              <option key={c.name} value={c.name}>
                {c.name}
                {c.init ? " (init)" : ""}
              </option>
            ))}
          </Select>
        )}
        <Select className="w-auto" aria-label="Aantal regels" value={lines} onChange={(e) => setLines(Number(e.target.value))}>
          {lineChoices.map((n) => (
            <option key={n} value={n}>
              laatste {n} regels
            </option>
          ))}
        </Select>
        <label className="inline-flex cursor-pointer items-center gap-2">
          <input type="checkbox" className="size-4" checked={previous} onChange={(e) => setPrevious(e.target.checked)} />
          Vorige container
        </label>
        <label className="inline-flex cursor-pointer items-center gap-2">
          <input type="checkbox" className="size-4" checked={timestamps} onChange={(e) => setTimestamps(e.target.checked)} />
          Tijdstippen
        </label>
        <label className="inline-flex cursor-pointer items-center gap-2">
          <input
            type="checkbox"
            className="size-4"
            checked={follow && !previous}
            disabled={previous}
            onChange={(e) => setFollow(e.target.checked)}
          />
          Volgen
        </label>
        <Button type="button" variant="ghost" className="px-2 py-1 text-xs" onClick={() => logs.refetch()}>
          Vernieuwen
        </Button>
      </div>

      {current && current.restarts > 0 && !previous && (
        <p className="text-sm text-amber-800 dark:text-amber-300">
          Deze container herstartte {current.restarts} keer
          {current.last_reason ? ` (de vorige stopte met ${current.last_reason})` : ""}. De reden staat meestal onderaan de log
          van de vorige container.{" "}
          <button type="button" className="font-medium underline" onClick={() => setPrevious(true)}>
            Toon de vorige log
          </button>
        </p>
      )}

      {noPrevious ? (
        <Alert kind="info">{(logs.error as ApiError).message}</Alert>
      ) : (
        logs.isError && <KubeError error={logs.error} />
      )}
      {logs.data && (
        <>
          <pre
            ref={box}
            onScroll={(e) => {
              const el = e.currentTarget;
              atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
            }}
            className="max-h-[28rem] min-h-24 overflow-auto rounded-md bg-slate-900 p-3 font-mono text-xs leading-relaxed whitespace-pre text-slate-100 dark:bg-black"
          >
            {logs.data.text || <span className="text-slate-400">(nog niets gelogd)</span>}
          </pre>
          {logs.data.truncated && (
            <p className="text-xs text-amber-700 dark:text-amber-300">
              De log is groter dan 2 MB; de oudste regels ontbreken. Kies minder regels.
            </p>
          )}
        </>
      )}
      {logs.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      <p className="text-xs text-slate-500">
        Zelf: <code className="break-all">{cmd}</code>
      </p>
    </div>
  );
}
