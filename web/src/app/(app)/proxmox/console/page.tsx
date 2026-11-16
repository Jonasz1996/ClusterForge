"use client";

import { useSearchParams } from "next/navigation";
import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import { Alert, Badge, Button, PageHeader } from "@/components/ui";
import { TermView, VncView, type ConsoleState, type VncHandle } from "@/components/pve/Console";
import { useIsAdmin } from "@/lib/inventory";
import { consoleSocketURL, openPveConsole, type ConsoleKind, type PveConsoleOpened } from "@/lib/pve";

// De console van een gast of host (fase 26 M7), zoals de knop Console in de
// Proxmox-GUI: opent in een eigen tabblad. Het verkeer loopt via de agent
// op de host; ClusterForge bewaart er niets van.

const kinds: ConsoleKind[] = ["vnc", "serial", "ct", "host"];

export default function ConsolePage() {
  return (
    <Suspense>
      <ConsoleScreen />
    </Suspense>
  );
}

function ConsoleScreen() {
  const sp = useSearchParams();
  const isAdmin = useIsAdmin();
  const dc = sp.get("dc") ?? "";
  const node = sp.get("node") ?? "";
  const kind = kinds.find((k) => k === sp.get("kind"));
  const vmid = Number(sp.get("vmid") ?? 0) || undefined;
  const [attempt, setAttempt] = useState(0);
  const [opened, setOpened] = useState<PveConsoleOpened | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [state, setState] = useState<ConsoleState>({ phase: "connecting" });
  const vnc = useRef<VncHandle>(null);
  const area = useRef<HTMLDivElement>(null);
  const valid = !!dc && !!node && !!kind && (kind === "host" || !!vmid);

  useEffect(() => {
    if (!valid || !isAdmin) return;
    let off = false;
    openPveConsole(dc, { node, kind: kind!, vmid })
      .then((o) => {
        if (off) return;
        setOpened(o);
        setError(null);
        setState({ phase: "connecting" });
        document.title = `${o.title} · ClusterForge`;
      })
      .catch((e: unknown) => {
        if (off) return;
        setOpened(null);
        setError(e instanceof Error ? e.message : String(e));
      });
    return () => {
      off = true;
    };
  }, [attempt, valid, isAdmin, dc, node, kind, vmid]);

  const onState = useCallback((s: ConsoleState) => setState(s), []);
  const reconnect = () => {
    setOpened(null);
    setError(null);
    setState({ phase: "connecting" });
    setAttempt((a) => a + 1);
  };

  if (!isAdmin) return <Alert kind="info">Een console is alleen voor beheerders.</Alert>;
  if (!valid) return <Alert>Onvolledige link: kies een console vanaf de pagina van een gast of host.</Alert>;

  const title = opened?.title ?? (kind === "host" ? `Shell op ${node}` : `${kind === "ct" ? "CT" : "VM"} ${vmid} op ${node}`);
  const badge = error ? (
    <Badge tone="red">Mislukt</Badge>
  ) : state.phase === "open" ? (
    <Badge tone="green">Verbonden</Badge>
  ) : state.phase === "closed" ? (
    <Badge tone="slate">Gesloten</Badge>
  ) : (
    <Badge tone="amber">Verbinden…</Badge>
  );

  return (
    <div className="flex h-[calc(100vh-7rem)] min-h-96 flex-col gap-3">
      <PageHeader
        title={
          <span className="flex flex-wrap items-center gap-2">
            {title} {badge}
          </span>
        }
        description={
          kind === "serial"
            ? "Seriële poort (qm terminal). Ctrl+O verlaat de terminal."
            : kind === "vnc"
              ? "Scherm van de VM. Toetsen gaan naar de VM zolang het scherm de focus heeft."
              : kind === "ct"
                ? "Rootshell in de container (pct enter)."
                : "Rootshell op de Proxmox-host."
        }
        actions={
          <div className="flex flex-wrap gap-2">
            {kind === "vnc" && state.phase === "open" && (
              <Button variant="secondary" onClick={() => vnc.current?.ctrlAltDel()}>
                Ctrl+Alt+Del
              </Button>
            )}
            <Button variant="secondary" onClick={() => void area.current?.requestFullscreen?.()}>
              Volledig scherm
            </Button>
            {(error || state.phase === "closed") && <Button onClick={reconnect}>Opnieuw verbinden</Button>}
          </div>
        }
      />
      {error && <Alert>{error}</Alert>}
      {state.phase === "closed" && state.reason && <Alert kind="info">{state.reason}</Alert>}
      <div ref={area} className="min-h-0 flex-1 overflow-hidden rounded-lg border border-slate-800 bg-slate-950">
        {opened &&
          (opened.kind === "vnc" ? (
            <VncView key={opened.session} ref={vnc} url={consoleSocketURL(opened.session)} password={opened.password ?? ""} onState={onState} />
          ) : (
            <TermView key={opened.session} url={consoleSocketURL(opened.session)} onState={onState} />
          ))}
      </div>
    </div>
  );
}
