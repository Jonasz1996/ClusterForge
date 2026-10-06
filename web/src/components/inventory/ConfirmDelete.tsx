"use client";

import { UsedBy } from "@/components/deps/ImpactList";
import { Modal } from "@/components/Modal";
import { Alert, Button } from "@/components/ui";
import { ApiError } from "@/lib/api/client";

// ConfirmDelete vraagt bevestiging voor het verwijderen van een cluster of
// een node, met de diensten van buiten die ervan afhangen.
export function ConfirmDelete({
  title,
  text,
  clusterId,
  nodeId,
  what,
  pending,
  error,
  onConfirm,
  onClose,
}: {
  title: string;
  text: string;
  clusterId?: string;
  nodeId?: string;
  what: string;
  pending: boolean;
  error: unknown;
  onConfirm: () => void;
  onClose: () => void;
}) {
  return (
    <Modal title={title} onClose={onClose}>
      <div className="space-y-4 text-sm">
        <p>{text}</p>
        <UsedBy clusterId={clusterId} nodeId={nodeId} what={what} />
        {!!error && <Alert>{error instanceof ApiError ? error.message : "Verwijderen mislukt"}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="button" variant="danger" disabled={pending} onClick={onConfirm}>
            Verwijderen
          </Button>
        </div>
      </div>
    </Modal>
  );
}
