-- +goose Up
-- Het sandbox-register: de enige lijst van tijdelijke VM's die ClusterForge
-- zelf terugzette om een back-up te controleren. Starten en verwijderen
-- kan alleen voor een VM met een rij hier. state:
--   reserved        VMID gekozen, terugzetten loopt of moet nog
--   present         de VM bestaat in Proxmox
--   destroyed       verwijderd
--   destroy_failed  verwijderen lukte niet; de opruimer probeert opnieuw
--   none            de VM is er nooit gekomen, of het VMID hoort niet (meer)
--                   bij een sandbox; er valt niets op te ruimen
CREATE TABLE backup_sandboxes (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id   uuid NOT NULL REFERENCES proxmox_connections (id) ON DELETE CASCADE,
    vmid            integer NOT NULL,
    source_vmid     integer NOT NULL CHECK (source_vmid <> vmid),
    -- De node van de bron-VM: zolang de sandbox bestaat, komt een tweede
    -- agent met diens sleutel niet binnen.
    source_node_id  uuid REFERENCES nodes (id) ON DELETE SET NULL,
    run_id          uuid REFERENCES test_runs (id) ON DELETE SET NULL,
    volid           text NOT NULL,
    state           text NOT NULL DEFAULT 'reserved'
                    CHECK (state IN ('reserved', 'present', 'destroyed', 'destroy_failed', 'none')),
    host            text NOT NULL,
    storage         text NOT NULL,
    error           text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    destroyed_at    timestamptz
);
CREATE UNIQUE INDEX backup_sandboxes_live ON backup_sandboxes (connection_id, vmid)
    WHERE state IN ('reserved', 'present', 'destroy_failed');
CREATE INDEX backup_sandboxes_source ON backup_sandboxes (source_node_id)
    WHERE state IN ('reserved', 'present', 'destroy_failed');

-- De laatste controle per node staat in test_runs zelf, niet nog eens in
-- backup_status: één bron.
CREATE INDEX test_runs_kind ON test_runs (kind, created_at DESC);
CREATE INDEX test_runs_node ON test_runs (node_id, created_at DESC);

-- +goose Down
DROP INDEX test_runs_node;
DROP INDEX test_runs_kind;
DROP TABLE backup_sandboxes;
