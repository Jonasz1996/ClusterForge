"use client";

import { useState, type FormEvent } from "react";
import { Alert, Button, Field, Input, Select, Textarea } from "@/components/ui";
import {
  clusterTypes,
  environments,
  parseTags,
  useUsers,
  type ClusterDetail,
  type ClusterInput,
  type ClusterType,
  type Environment,
} from "@/lib/inventory";

// slugify maakt een voorstel voor de slug op basis van de naam.
function slugify(s: string) {
  return s
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[̀-ͯ]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 63);
}

export function ClusterForm({
  initial,
  submitLabel,
  onSubmit,
  onCancel,
}: {
  initial?: ClusterDetail;
  submitLabel: string;
  onSubmit: (v: ClusterInput) => Promise<unknown>;
  onCancel?: () => void;
}) {
  const users = useUsers();
  const [name, setName] = useState(initial?.name ?? "");
  const [slug, setSlug] = useState(initial?.slug ?? "");
  const [slugTouched, setSlugTouched] = useState(initial !== undefined);
  const [type, setType] = useState<ClusterType>(initial?.type ?? "keepalived");
  const [environment, setEnvironment] = useState<Environment>(initial?.environment ?? "lab");
  const [description, setDescription] = useState(initial?.description ?? "");
  const [gitRepoUrl, setGitRepoUrl] = useState(initial?.git_repo_url ?? "");
  const [tags, setTags] = useState(initial?.tags.join(", ") ?? "");
  const [owners, setOwners] = useState<string[]>(initial?.owners.map((o) => o.id) ?? []);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      await onSubmit({
        name,
        slug,
        type,
        environment,
        description,
        git_repo_url: gitRepoUrl,
        tags: parseTags(tags),
        owner_ids: owners,
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Opslaan mislukt");
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      {error && <Alert>{error}</Alert>}
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Naam" htmlFor="c-name">
          <Input
            id="c-name"
            required
            maxLength={128}
            value={name}
            onChange={(e) => {
              setName(e.target.value);
              if (!slugTouched) setSlug(slugify(e.target.value));
            }}
          />
        </Field>
        <Field label="Slug" htmlFor="c-slug" hint="Korte naam voor URL's en de CLI">
          <Input
            id="c-slug"
            required
            pattern="[a-z0-9][a-z0-9\-]{0,62}"
            value={slug}
            onChange={(e) => {
              setSlug(e.target.value);
              setSlugTouched(true);
            }}
          />
        </Field>
        <Field label="Type" htmlFor="c-type">
          <Select id="c-type" value={type} onChange={(e) => setType(e.target.value as ClusterType)}>
            {clusterTypes.map((t) => (
              <option key={t.value} value={t.value}>
                {t.label}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Omgeving" htmlFor="c-env">
          <Select id="c-env" value={environment} onChange={(e) => setEnvironment(e.target.value as Environment)}>
            {environments.map((t) => (
              <option key={t.value} value={t.value}>
                {t.label}
              </option>
            ))}
          </Select>
        </Field>
      </div>
      <Field label="Beschrijving" htmlFor="c-desc">
        <Textarea id="c-desc" rows={2} maxLength={2000} value={description} onChange={(e) => setDescription(e.target.value)} />
      </Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Git-repository" htmlFor="c-git" hint="Optioneel; voor GitOps in fase 2">
          <Input
            id="c-git"
            placeholder="git@github.com:org/repo.git"
            value={gitRepoUrl}
            onChange={(e) => setGitRepoUrl(e.target.value)}
          />
        </Field>
        <Field label="Tags" htmlFor="c-tags" hint="Gescheiden door komma's of spaties">
          <Input id="c-tags" placeholder="web, dc1" value={tags} onChange={(e) => setTags(e.target.value)} />
        </Field>
      </div>
      <fieldset>
        <legend className="mb-1 text-sm font-medium text-slate-700 dark:text-slate-300">Owners</legend>
        <div className="flex flex-wrap gap-x-4 gap-y-2">
          {users.data?.map((u) => (
            <label key={u.id} className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                className="size-4 rounded border-slate-300 accent-brand-600"
                checked={owners.includes(u.id)}
                onChange={(e) => setOwners(e.target.checked ? [...owners, u.id] : owners.filter((x) => x !== u.id))}
              />
              {u.username}
            </label>
          ))}
        </div>
      </fieldset>
      <div className="flex gap-2">
        <Button type="submit" disabled={busy}>
          {busy ? "Opslaan…" : submitLabel}
        </Button>
        {onCancel && (
          <Button type="button" variant="secondary" onClick={onCancel}>
            Annuleren
          </Button>
        )}
      </div>
    </form>
  );
}
