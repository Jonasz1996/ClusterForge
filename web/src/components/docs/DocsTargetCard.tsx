"use client";

import { useState, type FormEvent } from "react";
import { CommitLink } from "@/components/gitops/bits";
import { ago } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, Field, Input } from "@/components/ui";
import {
  formatWhen,
  useDeleteDocsTarget,
  useDocsTarget,
  usePublishDocs,
  useSaveDocsTarget,
  useTestDocsTarget,
  type DocsPublishResult,
  type DocsTarget,
  type DocsTargetInput,
} from "@/lib/docs";

const title = "Documentatie naar GitHub";

// DocsTargetCard koppelt een eigen GitHub-repository waar ClusterForge de
// documentatie in schrijft, en toont de laatste commit. Het token komt nooit
// terug uit de server.
export function DocsTargetCard({ isAdmin }: { isAdmin: boolean }) {
  const q = useDocsTarget();
  const [editing, setEditing] = useState(false);
  const s = q.data;
  if (!s) return null;
  const t = s.target;
  if (!s.enabled) {
    return (
      <Card title={title}>
        <p className="text-sm text-slate-600 dark:text-slate-400">
          Zet <code className="font-mono">CF_MASTER_KEY</code> om de documentatie naar een eigen GitHub-repository te schrijven;
          het token wordt daarmee versleuteld bewaard.
        </p>
      </Card>
    );
  }
  if (!t || editing) {
    if (!isAdmin) {
      return (
        <Card title={title}>
          <p className="text-sm text-slate-600 dark:text-slate-400">
            Nog niet gekoppeld. Een beheerder kan ClusterForge de documentatie hier naar een eigen GitHub-repository laten
            schrijven.
          </p>
        </Card>
      );
    }
    return (
      <Card title={t ? "Documentatie naar GitHub: koppeling wijzigen" : title}>
        <div className="space-y-4">
          {!t && (
            <p className="text-sm text-slate-600 dark:text-slate-400">
              ClusterForge schrijft de documentatie tien minuten na een wijziging en elke nacht naar een eigen repository: één
              commit met alleen de bestanden die veranderden. Neem niet de repository van GitOps; GitOps schrijft nooit naar Git.
            </p>
          )}
          <TokenHelp />
          <TargetForm
            initial={t ?? undefined}
            onDone={() => setEditing(false)}
            onCancel={t ? () => setEditing(false) : undefined}
          />
        </div>
      </Card>
    );
  }
  return <TargetStatus t={t} pending={s.pending} isAdmin={isAdmin} onEdit={() => setEditing(true)} />;
}

function TargetStatus({
  t,
  pending,
  isAdmin,
  onEdit,
}: {
  t: DocsTarget;
  pending: boolean;
  isAdmin: boolean;
  onEdit: () => void;
}) {
  const publish = usePublishDocs();
  const save = useSaveDocsTarget();
  const remove = useDeleteDocsTarget();
  const [result, setResult] = useState<DocsPublishResult | null>(null);
  const commit = t.last_commit;
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>{title}</span>
          {isAdmin && (
            <span className="flex flex-wrap gap-2">
              <Button
                disabled={publish.isPending}
                onClick={() => {
                  setResult(null);
                  publish.mutate(undefined, { onSuccess: setResult });
                }}
              >
                {publish.isPending ? "Publiceren…" : "Nu publiceren"}
              </Button>
              <Button variant="secondary" onClick={onEdit}>
                Bewerken
              </Button>
              <Button
                variant="secondary"
                disabled={save.isPending}
                onClick={() =>
                  save.mutate({
                    repository: t.repository,
                    api_url: t.api_url,
                    branch: t.branch,
                    path: t.path,
                    enabled: !t.enabled,
                  })
                }
              >
                {t.enabled ? "Pauzeren" : "Weer aanzetten"}
              </Button>
              <Button
                variant="ghost"
                disabled={remove.isPending}
                onClick={() => {
                  if (window.confirm(`Koppeling met ${t.repository} verwijderen? Wat al in de repository staat, blijft daar.`))
                    remove.mutate();
                }}
              >
                Verwijderen
              </Button>
            </span>
          )}
        </span>
      }
    >
      <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-[10rem_1fr]">
        <dt className="text-slate-500">Repository</dt>
        <dd>
          <a href={t.web_url} target="_blank" rel="noreferrer" className="text-brand-700 hover:underline dark:text-sky-300">
            {t.repository}
          </a>
        </dd>
        <dt className="text-slate-500">Branch en map</dt>
        <dd>
          <a href={t.folder_url} target="_blank" rel="noreferrer" className="hover:underline">
            <code className="text-xs">{t.branch}</code> · <code className="text-xs">{t.path ? `${t.path}/` : "hoofdmap"}</code>
          </a>
        </dd>
        <dt className="text-slate-500">Publiceren</dt>
        <dd>
          {t.enabled ? (
            "Tien minuten na een wijziging en elke nacht om 06:00"
          ) : (
            <span className="inline-flex flex-wrap items-center gap-2">
              <Badge tone="amber">Gepauzeerd</Badge> ClusterForge publiceert alleen met Nu publiceren
            </span>
          )}
        </dd>
        <dt className="text-slate-500">Laatste commit</dt>
        <dd>
          {commit ? (
            <span className="inline-flex flex-wrap items-center gap-x-2">
              <CommitLink sha={commit.sha} url={commit.url} />
              <span className="text-slate-500">
                {commit.files === 1 ? "1 bestand" : `${commit.files} bestanden`} · {formatWhen(commit.at)}
              </span>
            </span>
          ) : (
            <span className="text-slate-400">Nog geen</span>
          )}
        </dd>
        <dt className="text-slate-500">Gecontroleerd</dt>
        <dd>
          {t.last_checked_at ? ago(t.last_checked_at) : "nog niet gecontroleerd"}
          {pending && !t.last_error && <span className="text-slate-500"> · er wacht een publicatie</span>}
        </dd>
      </dl>
      {t.last_error && (
        <div className="mt-4">
          <Alert>
            De laatste poging mislukte: {t.last_error}. ClusterForge probeert het om de vijf minuten opnieuw, en de documentatie
            in ClusterForge zelf blijft gewoon bij.
          </Alert>
        </div>
      )}
      {result && (
        <div className="mt-4">
          <Alert kind="success">
            {result.commit ? (
              <>
                Commit <CommitLink sha={result.commit.sha} url={result.commit.url} /> met{" "}
                {result.files.length === 1 ? "1 bestand" : `${result.files.length} bestanden`}.
              </>
            ) : (
              "Alles stond al in GitHub; er kwam geen commit."
            )}
          </Alert>
        </div>
      )}
      {[publish, save, remove].map(
        (m, i) =>
          m.isError && (
            <div key={i} className="mt-4">
              <Alert>{m.error.message}</Alert>
            </div>
          ),
      )}
    </Card>
  );
}

function TargetForm({ initial, onDone, onCancel }: { initial?: DocsTarget; onDone: () => void; onCancel?: () => void }) {
  const save = useSaveDocsTarget();
  const test = useTestDocsTarget();
  const [repository, setRepository] = useState(initial?.repository ?? "");
  const [branch, setBranch] = useState(initial?.branch ?? "main");
  const [path, setPath] = useState(initial?.path ?? "docs");
  const [token, setToken] = useState("");
  const [apiUrl, setApiUrl] = useState(initial?.api_url ?? "");
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const input = (): DocsTargetInput => ({ repository, branch, path: path.trim(), token, api_url: apiUrl, enabled });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    try {
      await save.mutateAsync(input());
      onDone();
    } catch {
      // De melding staat onder het formulier.
    }
  };

  return (
    <form onSubmit={submit} className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Repository" htmlFor="docs-repo" hint="eigenaar/naam, of de link naar de repository op GitHub.">
          <Input
            id="docs-repo"
            value={repository}
            onChange={(e) => setRepository(e.target.value)}
            required
            placeholder="Jonasz1996/clusterforge-docs"
          />
        </Field>
        <Field
          label="Token"
          htmlFor="docs-token"
          hint={initial ? "Leeg laten om het huidige te houden." : "Een fine-grained token met alleen Contents: Read and write."}
        >
          <Input
            id="docs-token"
            type="password"
            autoComplete="off"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            required={!initial}
            className="font-mono"
          />
        </Field>
        <Field
          label="Branch"
          htmlFor="docs-branch"
          hint="Bestaat ze nog niet, dan maakt ClusterForge ze met alleen de documentatie."
        >
          <Input id="docs-branch" value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="main" />
        </Field>
        <Field
          label="Map"
          htmlFor="docs-path"
          hint="Leeg is de hoofdmap van de repository; daar vervangt het overzicht README.md."
        >
          <Input id="docs-path" value={path} onChange={(e) => setPath(e.target.value)} placeholder="docs" />
        </Field>
      </div>
      <label className="flex items-start gap-2 text-sm">
        <input type="checkbox" className="mt-0.5" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        <span>
          Automatisch publiceren
          <span className="block text-xs text-slate-500">
            Tien minuten na een wijziging en elke nacht. Uit: alleen met Nu publiceren.
          </span>
        </span>
      </label>
      <details className="text-sm">
        <summary className="cursor-pointer text-slate-600 dark:text-slate-400">GitHub Enterprise</summary>
        <div className="mt-3 max-w-md">
          <Field label="API-adres" htmlFor="docs-api" hint="Leeg voor github.com; bij GitHub Enterprise https://host/api/v3.">
            <Input
              id="docs-api"
              value={apiUrl}
              onChange={(e) => setApiUrl(e.target.value)}
              placeholder="https://api.github.com"
            />
          </Field>
        </div>
      </details>
      {test.isError && <Alert>{test.error.message}</Alert>}
      {save.isError && <Alert>{save.error.message}</Alert>}
      {test.data && (
        <Alert kind="success">
          De verbinding werkt en het token mag schrijven.{" "}
          {test.data.branch_exists
            ? `Branch ${branch || "main"} bestaat; ${test.data.files === 0 ? "er staan nog geen documenten" : `er staan al ${test.data.files} documenten`} in ${path.trim() ? `${path.trim()}/` : "de hoofdmap"}.`
            : `Branch ${branch} bestaat nog niet; ClusterForge maakt ze bij de eerste publicatie.`}
        </Alert>
      )}
      <div className="flex flex-wrap justify-end gap-2">
        {onCancel && (
          <Button type="button" variant="secondary" onClick={onCancel}>
            Annuleren
          </Button>
        )}
        <Button
          type="button"
          variant="secondary"
          disabled={repository.trim() === "" || test.isPending}
          onClick={() => test.mutate(input())}
        >
          {test.isPending ? "Testen…" : "Testen"}
        </Button>
        <Button type="submit" disabled={save.isPending}>
          {save.isPending ? "Verbinding testen…" : "Opslaan"}
        </Button>
      </div>
    </form>
  );
}

// TokenHelp legt uit hoe je de repository en het token op GitHub maakt.
function TokenHelp() {
  return (
    <details className="rounded-md bg-slate-50 p-3 text-sm dark:bg-slate-800/50">
      <summary className="cursor-pointer font-medium">Zo maak je de repository en het token op GitHub</summary>
      <ol className="mt-2 list-decimal space-y-1 pl-5 text-slate-600 dark:text-slate-400">
        <li>
          Maak een private repository, bijvoorbeeld <code className="font-mono">clusterforge-docs</code>, en vink &ldquo;Add a
          README file&rdquo; aan: in een lege repository kan ClusterForge niet schrijven.
        </li>
        <li>Ga naar Settings → Developer settings → Personal access tokens → Fine-grained tokens en kies Generate new token.</li>
        <li>Kies bij Repository access &ldquo;Only select repositories&rdquo; en alleen die repository.</li>
        <li>Zet bij Permissions alleen Contents op Read and write. Meer is niet nodig.</li>
        <li>Kopieer het token en plak het hieronder; het wordt versleuteld bewaard en komt nooit terug.</li>
      </ol>
      <p className="mt-2 text-slate-600 dark:text-slate-400">
        ClusterForge schrijft alleen in de branch en map die je hier kiest, met ClusterForge als auteur. Een bestand dat je zelf
        in de map zet, blijft staan.
      </p>
    </details>
  );
}
