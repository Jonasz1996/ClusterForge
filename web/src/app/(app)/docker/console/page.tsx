"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useCallback, useRef, useState } from "react";
import { TermView, type ConsoleState } from "@/components/pve/Console";
import { Alert, Badge, Button, Field, Input, PageHeader, Select } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { openDockerConsole, type DockerConsoleOpened, type DockerShell } from "@/lib/docker";
import { useIsAdmin } from "@/lib/inventory";
import { consoleSocketURL } from "@/lib/pve";

// Een shell in een container (fase 28 M5), zoals Console in Portainer: docker
// exec -it via de agent op de node. Opent in een eigen tabblad; ClusterForge
// bewaart niets van wat je typt, alleen dat je hem opende.

export default function Page() {
  return (
    <Suspense>
      <DockerConsole />
    </Suspense>
  );
}

type Ask = { shell: DockerShell; user: string };

function DockerConsole() {
  const sp = useSearchParams();
  const isAdmin = useIsAdmin();
  const node = sp.get("node") ?? "";
  const id = sp.get("id") ?? "";
  const [ask, setAsk] = useState<Ask | null>(null);
  const [form, setForm] = useState<Ask>({ shell: "auto", user: "" });
  const [code, setCode] = useState("");
  const [needCode, setNeedCode] = useState<string | null>(null);
  const [opened, setOpened] = useState<DockerConsoleOpened | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [state, setState] = useState<ConsoleState>({ phase: "connecting" });
  const [busy, setBusy] = useState(false);
  const area = useRef<HTMLDivElement>(null);

  const open = useCallback(
    async (a: Ask, confirm?: string) => {
      setBusy(true);
      setError(null);
      try {
        const o = await openDockerConsole(node, {
          container: id,
          shell: a.shell,
          user: a.user.trim() || undefined,
          // Eén veld voor beide: cijfers zijn een code, de rest een wachtwoord.
          ...(confirm ? (/^\d{6,8}$/.test(confirm) ? { totp_code: confirm } : { password: confirm }) : {}),
        });
        setOpened(o);
        setNeedCode(null);
        setCode("");
        setState({ phase: "connecting" });
        document.title = `${o.title} · ClusterForge`;
      } catch (e) {
        if (e instanceof ApiError && e.code === "needs_confirmation") setNeedCode(e.message);
        else setError(e instanceof Error ? e.message : String(e));
      } finally {
        setBusy(false);
      }
    },
    [node, id],
  );

  const onState = useCallback((s: ConsoleState) => setState(s), []);

  if (!isAdmin) return <Alert kind="info">Een console is alleen voor beheerders.</Alert>;
  if (!node || !id) return <Alert>Onvolledige link: open de console vanaf een container.</Alert>;

  const back = `/docker/container?node=${node}&id=${id}`;
  const badge = error ? (
    <Badge tone="red">Mislukt</Badge>
  ) : !opened ? null : state.phase === "open" ? (
    <Badge tone="green">Verbonden</Badge>
  ) : state.phase === "closed" ? (
    <Badge tone="slate">Gesloten</Badge>
  ) : (
    <Badge tone="amber">Verbinden…</Badge>
  );

  const restart = () => {
    setOpened(null);
    setError(null);
    setNeedCode(null);
    setAsk(null);
  };

  return (
    <div className="flex h-[calc(100vh-7rem)] min-h-96 flex-col gap-3">
      <PageHeader
        title={
          <span className="flex flex-wrap items-center gap-2">
            {opened?.title ?? "Console in een container"} {badge}
          </span>
        }
        description={
          <>
            docker exec in de container, via de agent op de node. Wat je typt, komt niet in ClusterForge; alleen het openen staat in het logboek.{" "}
            <Link className="text-brand-600 hover:underline" href={back}>
              Terug naar de container
            </Link>
          </>
        }
        actions={
          opened && (
            <div className="flex flex-wrap gap-2">
              <Button variant="secondary" onClick={() => void area.current?.requestFullscreen?.()}>
                Volledig scherm
              </Button>
              {state.phase === "closed" && <Button onClick={restart}>Nieuwe shell</Button>}
            </div>
          )
        }
      />
      {error && (
        <Alert>
          {error}{" "}
          <button type="button" className="underline" onClick={restart}>
            Opnieuw
          </button>
        </Alert>
      )}
      {!ask && (
        <form
          className="flex max-w-xl flex-wrap items-end gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            setAsk({ ...form });
            void open(form);
          }}
        >
          <Field label="Shell" htmlFor="shell">
            <Select id="shell" value={form.shell} onChange={(e) => setForm({ ...form, shell: e.target.value as DockerShell })}>
              <option value="auto">Vanzelf (bash, anders sh)</option>
              <option value="bash">bash</option>
              <option value="sh">sh</option>
            </Select>
          </Field>
          <Field label="Gebruiker (optioneel)" htmlFor="user">
            <Input id="user" value={form.user} onChange={(e) => setForm({ ...form, user: e.target.value })} placeholder="zoals het image zegt" />
          </Field>
          <Button type="submit">Verbinden</Button>
        </form>
      )}
      {needCode && ask && (
        <form
          className="max-w-md space-y-3 rounded-lg border border-amber-300 bg-amber-50 p-4 dark:border-amber-800 dark:bg-slate-900"
          onSubmit={(e) => {
            e.preventDefault();
            if (code.trim()) void open(ask, code.trim());
          }}
        >
          <p className="text-sm">{needCode}</p>
          <Input autoFocus aria-label="Code of wachtwoord" type="password" autoComplete="one-time-code" value={code} onChange={(e) => setCode(e.target.value)} />
          <div className="flex gap-2">
            <Button type="submit" disabled={busy || !code.trim()}>
              {busy ? "Bezig…" : "Openen"}
            </Button>
            <Button type="button" variant="secondary" onClick={restart}>
              Annuleren
            </Button>
          </div>
        </form>
      )}
      {state.phase === "closed" && state.reason && <Alert kind="info">{state.reason}</Alert>}
      {opened && (
        <div ref={area} className="min-h-0 flex-1 overflow-hidden rounded-lg border border-slate-800 bg-slate-950">
          <TermView key={opened.session} url={consoleSocketURL(opened.session)} onState={onState} />
        </div>
      )}
    </div>
  );
}
