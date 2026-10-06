"use client";

import { useState } from "react";
import { Modal } from "@/components/Modal";
import { Alert, Button } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useLinkCluster } from "@/lib/gitops";
import { Diff } from "./bits";

// LinkButton koppelt een cluster aan zijn bestand in Git. Is het bestand
// niet gelijk aan de export, dan toont het de diff.
export function LinkButton({ clusterId, path, small = false }: { clusterId: string; path: string; small?: boolean }) {
  const link = useLinkCluster();
  const [mismatch, setMismatch] = useState<ApiError | null>(null);
  const [error, setError] = useState<string | null>(null);
  return (
    <>
      <Button
        variant="secondary"
        className={small ? "px-2 py-1 text-xs" : undefined}
        disabled={link.isPending}
        onClick={() => {
          setError(null);
          link.mutate(clusterId, {
            onError: (err) => {
              if (err instanceof ApiError && err.code === "git_mismatch") setMismatch(err);
              else setError(err.message);
            },
          });
        }}
      >
        {link.isPending ? "Koppelen…" : "Koppelen"}
      </Button>
      {error && (
        <Modal title="Koppelen lukt niet" onClose={() => setError(null)}>
          <Alert>{error}</Alert>
          <div className="mt-4 flex justify-end">
            <Button variant="secondary" onClick={() => setError(null)}>
              Sluiten
            </Button>
          </div>
        </Modal>
      )}
      {mismatch && (
        <Modal title={`${path} is niet gelijk aan de export`} onClose={() => setMismatch(null)} wide>
          <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">{mismatch.message}.</p>
          {mismatch.diff && <Diff text={mismatch.diff} />}
          <div className="mt-4 flex justify-end">
            <Button variant="secondary" onClick={() => setMismatch(null)}>
              Sluiten
            </Button>
          </div>
        </Modal>
      )}
    </>
  );
}
