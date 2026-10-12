"use client";

import { useState, type FormEvent } from "react";
import { prodConfirmed, ProdConfirm, useProdTotp } from "@/components/ProdConfirm";
import { Empty, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { CodeBlock, KubeError, Term } from "@/components/kube/Kube";
import { Alert, Badge, Button, Card, Field, Input, Select } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { auditTimeFmt } from "@/lib/audit";
import { useMe } from "@/lib/auth";
import { useCluster, useIsAdmin } from "@/lib/inventory";
import {
  useIssueKubeAccess,
  useKubeAccess,
  useRevokeKubeAccess,
  type KubeAccess,
  type KubeAccessIssued,
  type KubeOverview,
} from "@/lib/kube";

const stateBadge: Record<KubeAccess["state"], { tone: "green" | "slate" | "amber"; label: string }> = {
  active: { tone: "green", label: "Actief" },
  expired: { tone: "slate", label: "Verlopen" },
  revoked: { tone: "slate", label: "Ingetrokken" },
  revoking: { tone: "amber", label: "Wordt ingetrokken" },
};

const dateFmt = new Intl.DateTimeFormat("nl-BE", {
  day: "numeric",
  month: "long",
  year: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});

// AccessTab geeft een kubeconfig voor kubectl, k9s, Headlamp of Lens op je
// eigen pc, en toont de kubeconfigs die er al zijn.
export function AccessTab({ id, o }: { id: string; o: KubeOverview }) {
  const [issued, setIssued] = useState<KubeAccessIssued | null>(null);
  const cluster = useCluster(id);
  const slug = cluster.data?.slug ?? "cluster";
  const server = issued?.server ?? (o.vip ? `https://${o.vip}:6443` : null);
  return (
    <div className="space-y-6">
      <div className="grid gap-6 lg:grid-cols-2 [&>*]:min-w-0">
        <IssueCard id={id} server={server} onIssued={setIssued} />
        <HowTo slug={slug} />
      </div>
      {issued && <IssuedCard issued={issued} onClose={() => setIssued(null)} />}
      <AccessList id={id} />
    </div>
  );
}

function IssueCard({ id, server, onIssued }: { id: string; server: string | null; onIssued: (i: KubeAccessIssued) => void }) {
  const isAdmin = useIsAdmin();
  const me = useMe();
  const totp = useProdTotp();
  const cluster = useCluster(id);
  const issue = useIssueKubeAccess(id);
  const [name, setName] = useState("laptop");
  const [role, setRole] = useState<"admin" | "view">("admin");
  const [days, setDays] = useState("7");
  const [secret, setSecret] = useState("");
  const [slug, setSlug] = useState("");
  const effectiveRole = isAdmin ? role : "view";
  const prod = cluster.data?.environment === "prod" && effectiveRole === "admin";
  const clusterSlug = cluster.data?.slug ?? "";

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    try {
      const out = await issue.mutateAsync({
        name: name.trim(),
        role: effectiveRole,
        days: parseInt(days, 10),
        ...(totp ? { totp_code: secret } : { password: secret }),
        confirm: prod ? slug : undefined,
      });
      setSecret("");
      setSlug("");
      onIssued(out);
    } catch {
      // De fout staat onder het formulier.
    }
  };

  return (
    <Card title="Kubeconfig maken">
      <form onSubmit={submit} className="space-y-4 text-sm">
        <p className="text-slate-600 dark:text-slate-300">
          Met een kubeconfig praten kubectl, k9s, Headlamp en Lens op je eigen pc rechtstreeks met de Kubernetes-API
          {server ? (
            <>
              {" "}
              op <code>{server}</code>
            </>
          ) : null}
          . Elke kubeconfig krijgt een eigen{" "}
          <Term help="Een account voor een programma in plaats van een mens. Kubernetes kent geen gebruikers; een ServiceAccount met een token wel.">
            ServiceAccount
          </Term>{" "}
          in de namespace <code>clusterforge</code>, met een token dat vanzelf verloopt.
        </p>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Naam" htmlFor="access-name" hint="Waarvoor hij is, zodat je hem later terugvindt.">
            <Input id="access-name" required maxLength={40} value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          <Field label="Geldig" htmlFor="access-days">
            <Select id="access-days" value={days} onChange={(e) => setDays(e.target.value)}>
              <option value="1">1 dag</option>
              <option value="7">7 dagen</option>
              <option value="30">30 dagen</option>
            </Select>
          </Field>
          <Field
            label="Rechten"
            htmlFor="access-role"
            hint={
              effectiveRole === "admin"
                ? "cluster-admin: alles, ook Secrets lezen en nodes wijzigen."
                : "view: alles lezen behalve Secrets; wijzigen kan niet."
            }
          >
            {isAdmin ? (
              <Select id="access-role" value={role} onChange={(e) => setRole(e.target.value as "admin" | "view")}>
                <option value="admin">Beheerder</option>
                <option value="view">Alleen lezen</option>
              </Select>
            ) : (
              <Input id="access-role" value="Alleen lezen" disabled />
            )}
          </Field>
          <Field
            label={totp ? "Code uit de app" : "Je wachtwoord"}
            htmlFor="access-secret"
            hint="Ter bevestiging: het token werkt ook zonder ClusterForge."
          >
            <Input
              id="access-secret"
              type={totp ? "text" : "password"}
              inputMode={totp ? "numeric" : undefined}
              autoComplete={totp ? "one-time-code" : "current-password"}
              required
              value={secret}
              onChange={(e) => setSecret(totp ? e.target.value.replace(/\D/g, "") : e.target.value)}
              className={totp ? "font-mono tracking-widest" : undefined}
            />
          </Field>
        </div>
        {prod && <ProdConfirm slug={clusterSlug} value={slug} onChange={setSlug} />}
        {issue.error ? <IssueError error={issue.error} /> : null}
        <div className="flex justify-end">
          <Button
            type="submit"
            disabled={issue.isPending || !me.data || (prod && !prodConfirmed(clusterSlug, slug, totp))}
            variant={prod ? "danger" : "primary"}
          >
            {issue.isPending ? "Bezig…" : "Kubeconfig maken"}
          </Button>
        </div>
      </form>
    </Card>
  );
}

function IssueError({ error }: { error: unknown }) {
  if (error instanceof ApiError && error.code === "invalid_totp") return <Alert>Deze code klopt niet of is al gebruikt.</Alert>;
  if (error instanceof ApiError && error.code === "invalid_credentials") return <Alert>Het wachtwoord klopt niet.</Alert>;
  return <KubeError error={error} />;
}

// download bewaart de kubeconfig als bestand, zonder dat hij ergens anders
// heen gaat.
function download(issued: KubeAccessIssued) {
  const url = URL.createObjectURL(new Blob([issued.kubeconfig], { type: "application/yaml" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = issued.filename;
  a.click();
  URL.revokeObjectURL(url);
}

function IssuedCard({ issued, onClose }: { issued: KubeAccessIssued; onClose: () => void }) {
  const [shown, setShown] = useState(false);
  return (
    <Card title={`Kubeconfig ${issued.access.name} is klaar`}>
      <div className="space-y-3 text-sm">
        <Alert kind="success">
          Download hem nu: ClusterForge bewaart het token niet en kan het later niet meer tonen. Wie het bestand heeft, kan tot{" "}
          {dateFmt.format(new Date(issued.access.expires_at))} {issued.access.role === "admin" ? "alles" : "alles lezen"} op dit
          cluster. Bewaar het zoals een wachtwoord.
        </Alert>
        <div className="flex flex-wrap gap-2">
          <Button type="button" onClick={() => download(issued)}>
            {issued.filename} downloaden
          </Button>
          <Button type="button" variant="secondary" onClick={() => setShown(!shown)}>
            {shown ? "Inhoud verbergen" : "Inhoud tonen"}
          </Button>
          <Button type="button" variant="secondary" onClick={onClose}>
            Sluiten
          </Button>
        </div>
        {shown && <CodeBlock text={issued.kubeconfig} className="max-h-80" />}
      </div>
    </Card>
  );
}

// HowTo zegt hoe je de kubeconfig gebruikt, op Linux of macOS en op
// Windows.
function HowTo({ slug }: { slug: string }) {
  const file = `${slug}.kubeconfig`;
  return (
    <Card title="Zo gebruik je hem">
      <div className="space-y-3 text-sm text-slate-700 dark:text-slate-300">
        <p>
          Installeer kubectl via{" "}
          <a
            href="https://kubernetes.io/docs/tasks/tools/"
            target="_blank"
            rel="noreferrer"
            className="text-brand-700 underline dark:text-sky-300"
          >
            kubernetes.io
          </a>
          . Zet het bestand daarna op een vaste plek en zeg kubectl waar het staat. Op Linux of macOS:
        </p>
        <CodeBlock
          text={[
            "mkdir -p ~/.kube",
            `mv ~/Downloads/${file} ~/.kube/`,
            `chmod 600 ~/.kube/${file}`,
            `export KUBECONFIG=~/.kube/${file}`,
            "kubectl get nodes",
          ].join("\n")}
        />
        <p>In PowerShell op Windows:</p>
        <CodeBlock text={[`$env:KUBECONFIG = "$HOME\\Downloads\\${file}"`, "kubectl get nodes"].join("\n")} />
        <p>
          <span className="font-medium">k9s</span> leest dezelfde <code>KUBECONFIG</code>, of start het met{" "}
          <code>k9s --kubeconfig ~/.kube/{file}</code>. In <span className="font-medium">Headlamp</span> en{" "}
          <span className="font-medium">Lens</span> voeg je het bestand toe als cluster. Je pc moet het VIP op poort 6443 kunnen
          bereiken.
        </p>
      </div>
    </Card>
  );
}

function AccessList({ id }: { id: string }) {
  const isAdmin = useIsAdmin();
  const list = useKubeAccess(id);
  const revoke = useRevokeKubeAccess(id);
  const items = list.data ?? [];
  return (
    <Card title={isAdmin ? "Uitgegeven kubeconfigs" : "Jouw kubeconfigs"}>
      <div className="space-y-3">
        {list.error ? <KubeError error={list.error} /> : null}
        {revoke.error ? <KubeError error={revoke.error} /> : null}
        {list.data && items.length === 0 ? (
          <Empty>Nog geen kubeconfigs. Verlopen en ingetrokken kubeconfigs blijven hier 30 dagen staan.</Empty>
        ) : (
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Naam</th>
                  {isAdmin && <th className={thClass}>Gebruiker</th>}
                  <th className={thClass}>Rechten</th>
                  <th className={thClass}>Gemaakt</th>
                  <th className={thClass}>Verloopt</th>
                  <th className={thClass}>Status</th>
                  <th className={thClass} />
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {items.map((a) => (
                  <tr key={a.id}>
                    <td className={tdClass}>
                      <div className="font-medium">{a.name}</div>
                      <div className="font-mono text-xs whitespace-nowrap text-slate-500">{a.account}</div>
                    </td>
                    {isAdmin && <td className={tdClass}>{a.user}</td>}
                    <td className={tdClass}>{a.role === "admin" ? "Beheerder" : "Alleen lezen"}</td>
                    <td className={`${tdClass} whitespace-nowrap`}>{auditTimeFmt.format(new Date(a.created_at))}</td>
                    <td className={`${tdClass} whitespace-nowrap`}>{auditTimeFmt.format(new Date(a.expires_at))}</td>
                    <td className={tdClass}>
                      <Badge tone={stateBadge[a.state].tone}>{stateBadge[a.state].label}</Badge>
                      <AccessNote a={a} />
                    </td>
                    <td className={`${tdClass} text-right`}>
                      {a.state === "active" && (
                        <Button
                          type="button"
                          variant="secondary"
                          className="px-2 py-1 text-xs"
                          disabled={revoke.isPending}
                          onClick={() => {
                            if (
                              window.confirm(
                                `Kubeconfig ${a.name} van ${a.user} intrekken? Het token werkt daarna meteen niet meer.`,
                              )
                            )
                              revoke.mutate(a.id);
                          }}
                        >
                          Intrekken
                        </Button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </Card>
  );
}

function AccessNote({ a }: { a: KubeAccess }) {
  const parts: string[] = [];
  if (a.revoked_at) {
    const who = a.revoked_by ? `Door ${a.revoked_by}` : "Door ClusterForge";
    parts.push(`${who} op ${auditTimeFmt.format(new Date(a.revoked_at))}${a.revoke_reason ? `: ${a.revoke_reason}` : ""}.`);
  }
  if (a.state === "revoking") {
    parts.push(
      `Het ServiceAccount staat er nog${a.remove_error ? `: ${a.remove_error}` : ""}. ClusterForge probeert het elke 30 seconden opnieuw.`,
    );
  }
  if (parts.length === 0) return null;
  return <p className="mt-1 max-w-xs text-xs text-slate-500">{parts.join(" ")}</p>;
}
