"use client";

import "@xterm/xterm/css/xterm.css";
import { useEffect, useImperativeHandle, useRef, type Ref } from "react";

// De twee soorten console (fase 26 M7): het scherm van een VM met noVNC, en
// een terminal (CT, seriële poort, host) met xterm. Beide praten met de
// websocket van ClusterForge; die gaat via de agent naar de host.

export type ConsoleState = { phase: "connecting" | "open" | "closed"; reason?: string };

export type VncHandle = { ctrlAltDel: () => void; focus: () => void };

type RFBType = import("@novnc/novnc/lib/rfb").default;

export function VncView({ url, password, onState, ref }: { url: string; password: string; onState: (s: ConsoleState) => void; ref?: Ref<VncHandle> }) {
  const box = useRef<HTMLDivElement>(null);
  const rfb = useRef<RFBType | null>(null);
  const report = useRef(onState);
  useEffect(() => {
    report.current = onState;
  }, [onState]);
  useImperativeHandle(ref, () => ({ ctrlAltDel: () => rfb.current?.sendCtrlAltDel(), focus: () => rfb.current?.focus() }), []);

  useEffect(() => {
    let cancelled = false;
    let r: RFBType | null = null;
    void import("@novnc/novnc/lib/rfb").then((mod) => {
      if (cancelled || !box.current) return;
      const RFB = mod.default;
      r = new RFB(box.current, url, { credentials: { password }, wsProtocols: ["binary"] });
      r.scaleViewport = true;
      r.resizeSession = false;
      r.background = "#020617";
      r.addEventListener("connect", () => {
        report.current({ phase: "open" });
        r?.focus();
      });
      r.addEventListener("credentialsrequired", () => r?.sendCredentials({ password }));
      r.addEventListener("securityfailure", (e) => {
        const reason = (e as CustomEvent<{ reason?: string }>).detail?.reason;
        report.current({ phase: "closed", reason: reason ? `VNC weigert: ${reason}` : "VNC weigert het wachtwoord; het vervalt na 30 seconden. Verbind opnieuw." });
      });
      r.addEventListener("disconnect", (e) => {
        const clean = (e as CustomEvent<{ clean: boolean }>).detail?.clean;
        report.current({ phase: "closed", reason: clean ? undefined : "De verbinding viel weg." });
      });
      rfb.current = r;
    });
    return () => {
      cancelled = true;
      r?.disconnect();
      rfb.current = null;
    };
  }, [url, password]);

  return <div ref={box} className="h-full w-full overflow-hidden bg-slate-950" />;
}

export function TermView({ url, onState }: { url: string; onState: (s: ConsoleState) => void }) {
  const box = useRef<HTMLDivElement>(null);
  const report = useRef(onState);
  useEffect(() => {
    report.current = onState;
  }, [onState]);

  useEffect(() => {
    let cancelled = false;
    const cleanup: (() => void)[] = [];
    void Promise.all([import("@xterm/xterm"), import("@xterm/addon-fit")]).then(([{ Terminal }, { FitAddon }]) => {
      if (cancelled || !box.current) return;
      const term = new Terminal({
        cursorBlink: true,
        fontSize: 13,
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
        theme: { background: "#020617" },
        scrollback: 5000,
      });
      const fit = new FitAddon();
      term.loadAddon(fit);
      term.open(box.current);
      fit.fit();
      cleanup.push(() => term.dispose());

      const ws = new WebSocket(url, ["binary"]);
      ws.binaryType = "arraybuffer";
      const enc = new TextEncoder();
      const send = (data: string | Uint8Array) => {
        if (ws.readyState === WebSocket.OPEN) ws.send(data);
      };
      const resize = () => send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      ws.onopen = () => {
        report.current({ phase: "open" });
        resize();
        term.focus();
      };
      ws.onmessage = (e) => term.write(new Uint8Array(e.data as ArrayBuffer));
      ws.onclose = (e) => {
        if (cancelled) return;
        term.write("\r\n\x1b[90m[verbinding gesloten]\x1b[0m\r\n");
        report.current({ phase: "closed", reason: e.reason || (e.wasClean ? undefined : "De verbinding viel weg.") });
      };
      cleanup.push(() => ws.close());
      const d1 = term.onData((d) => send(enc.encode(d)));
      const d2 = term.onBinary((d) => send(Uint8Array.from(d, (c) => c.charCodeAt(0) & 0xff)));
      const d3 = term.onResize(resize);
      cleanup.push(() => [d1, d2, d3].forEach((d) => d.dispose()));
      const ro = new ResizeObserver(() => fit.fit());
      ro.observe(box.current);
      cleanup.push(() => ro.disconnect());
    });
    return () => {
      cancelled = true;
      cleanup.reverse().forEach((f) => f());
    };
  }, [url]);

  return <div ref={box} className="h-full w-full overflow-hidden bg-slate-950 p-1" />;
}
