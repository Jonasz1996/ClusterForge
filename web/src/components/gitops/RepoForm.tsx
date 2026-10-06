"use client";

import { useState, type FormEvent } from "react";
import { Alert, Button, Field, Input } from "@/components/ui";
import { useProbeGitRepo, type GitRepo, type GitRepoInput } from "@/lib/gitops";
import { CommitLine } from "./bits";

// RepoForm koppelt een repository of wijzigt de koppeling. Een leeg token
// laat het opgeslagen token staan; het komt nooit terug uit de server.
export function RepoForm({
  initial,
  onSubmit,
  onCancel,
}: {
  initial?: GitRepo;
  onSubmit: (v: GitRepoInput) => Promise<void>;
  onCancel?: () => void;
}) {
  const [repository, setRepository] = useState(initial?.repository ?? "");
  const [branch, setBranch] = useState(initial?.branch ?? "main");
  const [path, setPath] = useState(initial?.path ?? "clusters");
  const [token, setToken] = useState("");
  const [apiUrl, setApiUrl] = useState(initial?.api_url ?? "");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const probe = useProbeGitRepo();
  const input = (): GitRepoInput => ({
    repository,
    branch,
    path,
    token,
    api_url: apiUrl,
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setPending(true);
    try {
      await onSubmit(input());
    } catch (err) {
      setError(err instanceof Error ? err.message : "Opslaan mislukt");
    } finally {
      setPending(false);
    }
  };

  return (
    <form onSubmit={submit} className="space-y-4">
      {error && <Alert>{error}</Alert>}
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Repository" htmlFor="git-repo" hint="eigenaar/naam, of de link naar de repository op GitHub.">
          <Input
            id="git-repo"
            value={repository}
            onChange={(e) => setRepository(e.target.value)}
            required
            placeholder="Jonasz1996/clusterforge-config"
          />
        </Field>
        <Field
          label="Token"
          htmlFor="git-token"
          hint={initial ? "Leeg laten om het huidige te houden." : "Een fine-grained token met alleen Contents: read-only."}
        >
          <Input
            id="git-token"
            type="password"
            autoComplete="off"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            required={!initial}
            className="font-mono"
          />
        </Field>
        <Field label="Branch" htmlFor="git-branch">
          <Input id="git-branch" value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="main" />
        </Field>
        <Field label="Map" htmlFor="git-path" hint="Per cluster een map met de slug als naam, met daarin cluster.yaml.">
          <Input id="git-path" value={path} onChange={(e) => setPath(e.target.value)} placeholder="clusters" />
        </Field>
      </div>
      <details className="text-sm">
        <summary className="cursor-pointer text-slate-600 dark:text-slate-400">GitHub Enterprise</summary>
        <div className="mt-3 max-w-md">
          <Field label="API-adres" htmlFor="git-api" hint="Leeg voor github.com; bij GitHub Enterprise https://host/api/v3.">
            <Input id="git-api" value={apiUrl} onChange={(e) => setApiUrl(e.target.value)} placeholder="https://api.github.com" />
          </Field>
        </div>
      </details>
      {probe.isError && <Alert>{probe.error.message}</Alert>}
      {probe.data && (
        <div className="space-y-2 rounded-md border border-slate-200 p-3 text-sm dark:border-slate-700">
          <p>
            De verbinding werkt. Laatste commit op {branch || "main"}: <CommitLine c={probe.data.head} />
          </p>
          <p className="text-slate-600 dark:text-slate-400">
            {probe.data.files.length === 0
              ? `Nog geen clusterbestanden onder ${path || "clusters"}/.`
              : `${probe.data.files.length} ${probe.data.files.length === 1 ? "clusterbestand" : "clusterbestanden"}: ${probe.data.files.join(", ")}`}
          </p>
        </div>
      )}
      <div className="flex flex-wrap justify-end gap-2">
        {onCancel && (
          <Button type="button" variant="secondary" onClick={onCancel}>
            Annuleren
          </Button>
        )}
        <Button type="button" variant="secondary" disabled={repository.trim() === "" || probe.isPending} onClick={() => probe.mutate(input())}>
          {probe.isPending ? "Testen…" : "Testen"}
        </Button>
        <Button type="submit" disabled={pending}>
          {pending ? "Verbinding testen…" : "Opslaan"}
        </Button>
      </div>
    </form>
  );
}

// TokenHelp legt uit hoe je het token op GitHub maakt.
export function TokenHelp() {
  return (
    <details className="mb-4 rounded-md bg-slate-50 p-3 text-sm dark:bg-slate-800/50">
      <summary className="cursor-pointer font-medium">Zo maak je het token op GitHub</summary>
      <ol className="mt-2 list-decimal space-y-1 pl-5 text-slate-600 dark:text-slate-400">
        <li>Ga naar Settings → Developer settings → Personal access tokens → Fine-grained tokens en kies Generate new token.</li>
        <li>Kies bij Repository access &ldquo;Only select repositories&rdquo; en alleen de repository met je clusters.</li>
        <li>Zet bij Permissions alleen Contents op Read-only. ClusterForge schrijft nooit naar Git.</li>
        <li>Kopieer het token en plak het hieronder; het wordt versleuteld bewaard.</li>
      </ol>
      <p className="mt-2 text-slate-600 dark:text-slate-400">
        De server moet <code className="font-mono">api.github.com</code> op poort 443 kunnen bereiken.
      </p>
    </details>
  );
}
