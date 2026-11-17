"use client";

import Link from "next/link";
import { useState } from "react";
import { Modal } from "@/components/Modal";
import { toast } from "@/components/Toast";
import { Alert, Badge, Button, Card, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { human, useCleanupNodeDisk, useNodeDisk, useScanNodeDisk, type DiskScan, type NodeDisk } from "@/lib/disk";

const timeFmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "short", timeStyle: "short" });

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : "Er ging iets mis.";
}

const levelText: Record<NodeDisk["level"], string> = {
  ok: "Er is genoeg ruimte.",
  warn: "De schijf raakt vol. Nog geen probleem, maar ruim op voor het er een wordt.",
  crit: "De schijf is (bijna) vol. Als ze helemaal vol raakt, stoppen databases en diensten met schrijven en kunnen ze vastlopen. Ruim nu op.",
  unknown: "Nog geen meting van de agent.",
};

function Bar({ ratio, warn, crit }: { ratio: number; warn: number; crit: number }) {
  const pct = Math.min(100, Math.round(ratio * 100));
  const color = pct >= crit ? "bg-red-500" : pct >= warn ? "bg-amber-400" : "bg-emerald-500";
  return (
    <div className="relative h-2.5 w-full overflow-hidden rounded-full bg-slate-200 dark:bg-slate-800" role="meter" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} aria-label="Bezetting">
      <div className={cx("h-full rounded-full", color)} style={{ width: `${pct}%` }} />
      <div className="absolute inset-y-0 w-px bg-amber-600/60" style={{ left: `${warn}%` }} aria-hidden />
      <div className="absolute inset-y-0 w-px bg-red-600/60" style={{ left: `${crit}%` }} aria-hidden />
    </div>
  );
}

/** Kaart Schijfruimte op de pagina van een node (fase 41). */
export function NodeDiskCard({ nodeId, isAdmin }: { nodeId: string; isAdmin: boolean }) {
  const d = useNodeDisk(nodeId);
  const scan = useScanNodeDisk();
  const [wizard, setWizard] = useState(false);
  const [jobId, setJobId] = useState<string | null>(null);
  const disk = d.data;
  const running = disk?.running_job_id ?? null;

  return (
    <div id="schijf" className="scroll-mt-20">
      <Card title="Schijfruimte">
        {d.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
        {d.isError && <Alert>{errText(d.error)}</Alert>}
        {disk && (
          <div className="space-y-4 text-sm">
            <div className="space-y-1.5">
              <div className="flex items-baseline justify-between gap-2">
                <span>
                  Volste schijf <span className="font-mono text-xs">{disk.mount || "?"}</span>
                </span>
                <span className="font-semibold">{disk.level === "unknown" ? "?" : `${Math.floor(disk.used_ratio * 100)} %`}</span>
              </div>
              {disk.level !== "unknown" && <Bar ratio={disk.used_ratio} warn={disk.warn_pct} crit={disk.crit_pct} />}
              <p className={cx("text-xs", disk.level === "crit" ? "text-red-700 dark:text-red-300" : disk.level === "warn" ? "text-amber-800 dark:text-amber-300" : "text-slate-500")}>
                {levelText[disk.level]} Let op vanaf {disk.warn_pct} %, ernstig vanaf {disk.crit_pct} % (
                <Link href="/instellingen#schijfbewaking" className="underline">
                  aanpassen
                </Link>
                ).
              </p>
            </div>

            {running && (
              <Alert kind="info">
                Bezig met meten of opruimen…{" "}
                <Link href={`/taken/detail?id=${running}`} className="underline">
                  Volg de taak
                </Link>
              </Alert>
            )}
            {jobId && !running && (
              <p className="text-xs text-slate-500">
                <Link href={`/taken/detail?id=${jobId}`} className="underline">
                  Laatste taak bekijken
                </Link>
              </p>
            )}
            {scan.isError && <Alert>{errText(scan.error)}</Alert>}

            {disk.scan && <ScanView scan={disk.scan} mount={disk.mount} />}

            {isAdmin && (
              <div className="flex flex-wrap gap-2">
                <Button
                  variant={disk.scan ? "secondary" : "primary"}
                  disabled={!disk.online || !!running || scan.isPending}
                  onClick={() =>
                    scan.mutate(nodeId, {
                      onSuccess: (r) => {
                        setJobId(r.job_id);
                        toast("Meting gestart; dit duurt meestal minder dan een minuut.");
                      },
                    })
                  }
                >
                  {disk.scan ? "Opnieuw meten" : "Kijk wat ruimte inneemt"}
                </Button>
                {disk.scan && (
                  <Button disabled={!disk.online || !!running} onClick={() => setWizard(true)}>
                    Ruimte vrijmaken…
                  </Button>
                )}
              </div>
            )}
            {isAdmin && !disk.online && <p className="text-xs text-slate-500">De agent antwoordt niet; meten en opruimen kan pas als de node weer online is.</p>}
            {!disk.scan && (
              <p className="text-xs text-slate-500">
                &ldquo;Kijk wat ruimte inneemt&rdquo; verandert niets op de node: het meet alleen. Daarna kies je zelf wat je opruimt.
              </p>
            )}
          </div>
        )}
      </Card>
      {wizard && disk?.scan && (
        <CleanupWizard
          nodeId={nodeId}
          hostname={disk.hostname}
          scan={disk.scan}
          onClose={() => setWizard(false)}
          onStarted={(id) => {
            setJobId(id);
            setWizard(false);
          }}
        />
      )}
    </div>
  );
}

function ScanView({ scan, mount }: { scan: DiskScan; mount: string }) {
  const [dirs, setDirs] = useState(false);
  const total = scan.items.reduce((s, i) => s + i.bytes, 0);
  return (
    <div className="space-y-3">
      <p className="text-xs text-slate-500">Gemeten op {timeFmt.format(new Date(scan.scanned_at))}.</p>
      {scan.cleaned_at && (
        <Alert kind="success">
          Laatste opruimbeurt op {timeFmt.format(new Date(scan.cleaned_at))}: {human(scan.freed ?? 0)} vrijgemaakt.
        </Alert>
      )}
      <ul className="space-y-1">
        {scan.mounts.map((m) => (
          <li key={m.mount} className="flex justify-between gap-2 text-xs">
            <span className="truncate font-mono">{m.mount}</span>
            <span className="whitespace-nowrap text-slate-500">
              {human(m.used)} van {human(m.size)} · {human(m.avail)} vrij
            </span>
          </li>
        ))}
      </ul>
      <p>
        Veilig op te ruimen: <strong>ongeveer {human(total)}</strong>.
        {total < 500e6 && " Dat is weinig; kijk bij de grootste mappen wat de ruimte echt inneemt."}
      </p>
      {scan.dirs.length > 0 && (
        <div>
          <button type="button" className="text-xs underline" onClick={() => setDirs((o) => !o)} aria-expanded={dirs}>
            {dirs ? "Grootste mappen verbergen" : "Grootste mappen tonen"}
          </button>
          {dirs && (
            <div className="mt-2 space-y-2">
              <ul className="space-y-0.5 text-xs">
                {scan.dirs
                  .filter((x) => x.path !== mount)
                  .slice(0, 15)
                  .map((x) => (
                    <li key={x.path} className="flex justify-between gap-2">
                      <span className="truncate font-mono">{x.path}</span>
                      <span className="whitespace-nowrap text-slate-500">{human(x.bytes)}</span>
                    </li>
                  ))}
              </ul>
              <p className="text-xs text-slate-500">
                Is het een map van een app, zoals AI-modellen in <span className="font-mono">/usr/share/ollama</span> of een database in{" "}
                <span className="font-mono">/var/lib</span>? Die ruimt ClusterForge niet zelf op, want dat is je data. Verwijder wat je niet meer
                gebruikt in de app zelf, of maak de schijf groter in Proxmox (
                <Link href="/proxmox/gasten" className="underline">
                  Gasten
                </Link>
                , schijf vergroten).
              </p>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function CleanupWizard({
  nodeId,
  hostname,
  scan,
  onClose,
  onStarted,
}: {
  nodeId: string;
  hostname: string;
  scan: DiskScan;
  onClose: () => void;
  onStarted: (jobId: string) => void;
}) {
  const cleanup = useCleanupNodeDisk();
  const [step, setStep] = useState<1 | 2>(1);
  const [chosen, setChosen] = useState<Set<string>>(() => new Set(scan.items.filter((i) => i.default && i.bytes > 0).map((i) => i.id)));
  const picked = scan.items.filter((i) => chosen.has(i.id));
  const total = picked.reduce((s, i) => s + i.bytes, 0);
  const toggle = (id: string) =>
    setChosen((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });

  return (
    <Modal title={`Ruimte vrijmaken op ${hostname}`} onClose={onClose} wide>
      <ol className="mb-4 flex gap-4 text-xs text-slate-500">
        <li className={cx(step === 1 && "font-semibold text-slate-900 dark:text-slate-100")}>1. Kiezen</li>
        <li className={cx(step === 2 && "font-semibold text-slate-900 dark:text-slate-100")}>2. Controleren en starten</li>
      </ol>
      {step === 1 && (
        <div className="space-y-3 text-sm">
          <p>Vink aan wat weg mag. Wat standaard aangevinkt staat, is veilig: geen enkele dienst heeft het nodig.</p>
          <ul className="space-y-2">
            {scan.items.map((i) => (
              <li key={i.id}>
                <label className={cx("flex cursor-pointer gap-3 rounded-md border p-3", chosen.has(i.id) ? "border-brand-500 bg-brand-50/50 dark:bg-slate-800" : "border-slate-200 dark:border-slate-700")}>
                  <input type="checkbox" className="mt-1" checked={chosen.has(i.id)} onChange={() => toggle(i.id)} disabled={i.bytes === 0} />
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-center gap-2">
                      <span className="font-medium">{i.title}</span>
                      <Badge tone={i.safe ? "green" : "amber"}>{i.safe ? "veilig" : "eerst nadenken"}</Badge>
                      <span className="ml-auto text-slate-500">{i.bytes === 0 ? "niets" : human(i.bytes)}</span>
                    </span>
                    <span className="mt-1 block text-xs text-slate-600 dark:text-slate-400">{i.explain}</span>
                  </span>
                </label>
              </li>
            ))}
          </ul>
          <div className="flex flex-wrap items-center justify-between gap-2 pt-2">
            <span>
              Samen ongeveer <strong>{human(total)}</strong>
            </span>
            <div className="flex gap-2">
              <Button variant="secondary" onClick={onClose}>
                Annuleren
              </Button>
              <Button disabled={picked.length === 0} onClick={() => setStep(2)}>
                Volgende
              </Button>
            </div>
          </div>
        </div>
      )}
      {step === 2 && (
        <div className="space-y-3 text-sm">
          <p>
            <strong>Wat gebeurt er?</strong> ClusterForge meet eerst, ruimt daarna op {hostname} op wat je koos en meet opnieuw, zodat je ziet
            hoeveel er vrijkwam. Je diensten blijven gewoon draaien. Het duurt meestal één tot enkele minuten.
          </p>
          <ul className="list-disc space-y-0.5 pl-5">
            {picked.map((i) => (
              <li key={i.id}>
                {i.title} <span className="text-slate-500">({human(i.bytes)})</span>
              </li>
            ))}
          </ul>
          <p className="text-xs text-slate-600 dark:text-slate-400">
            <strong>Kun je het terugdraaien?</strong> Nee, wat weg is, is weg. Maar niets hiervan is je eigen data: logboeken, downloads en
            caches komen vanzelf terug als ze nodig zijn.
          </p>
          {cleanup.isError && <Alert>{errText(cleanup.error)}</Alert>}
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="secondary" onClick={() => setStep(1)}>
              Terug
            </Button>
            <Button
              disabled={cleanup.isPending}
              onClick={() =>
                cleanup.mutate(
                  { id: nodeId, items: picked.map((i) => i.id) },
                  {
                    onSuccess: (r) => {
                      toast("Opruimen gestart. Het resultaat verschijnt op deze kaart.");
                      onStarted(r.job_id);
                    },
                  },
                )
              }
            >
              {cleanup.isPending ? "Starten…" : `Ruim ${human(total)} op`}
            </Button>
          </div>
        </div>
      )}
    </Modal>
  );
}
