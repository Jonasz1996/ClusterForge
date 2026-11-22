-- +goose Up
-- Kopie van de back-ups die Proxmox kent (vzdump-bestanden en Proxmox
-- Backup Server), zoals proxmox_resources dat is voor /cluster/resources.
CREATE TABLE proxmox_backups (
    connection_id  uuid NOT NULL REFERENCES proxmox_connections (id) ON DELETE CASCADE,
    -- Volume-id zoals Proxmox het geeft, bijvoorbeeld
    -- pbs:backup/vm/101/2026-10-04T01:00:03Z.
    volid          text NOT NULL,
    storage        text NOT NULL,
    -- De host waarlangs de lijst gelezen is.
    pve_node       text NOT NULL,
    vmid           integer NOT NULL,
    -- qemu of lxc; leeg als Proxmox het niet zegt.
    guest_type     text NOT NULL DEFAULT '',
    ctime          timestamptz NOT NULL,
    size_bytes     bigint NOT NULL DEFAULT 0,
    format         text NOT NULL DEFAULT '',
    notes          text NOT NULL DEFAULT '',
    protected      boolean NOT NULL DEFAULT false,
    -- De verificatie van Proxmox Backup Server zelf: '', ok of failed.
    verify_state   text NOT NULL DEFAULT '',
    synced_at      timestamptz NOT NULL,
    PRIMARY KEY (connection_id, volid)
);
CREATE INDEX proxmox_backups_vmid_idx ON proxmox_backups (connection_id, vmid, ctime DESC);

ALTER TABLE proxmox_connections
    -- Laatste poging om de back-ups te lezen, en de fout daarvan.
    ADD COLUMN backup_checked_at     timestamptz,
    ADD COLUMN backup_error          text NOT NULL DEFAULT '',
    -- VM's die in geen enkele back-upjob zitten; NULL als dat niet te lezen was.
    ADD COLUMN not_backed_up         jsonb;

-- Back-upbeleid per cluster. Zonder rij gelden de standaarden.
CREATE TABLE backup_policies (
    cluster_id     uuid PRIMARY KEY REFERENCES clusters (id) ON DELETE CASCADE,
    -- Hoe oud de nieuwste back-up van een node mag zijn.
    max_age_hours  integer NOT NULL DEFAULT 30 CHECK (max_age_hours BETWEEN 1 AND 720),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     uuid REFERENCES users (id) ON DELETE SET NULL
);

-- VM's zonder node waarvan de back-up toch bewaakt wordt, zoals ClusterForge
-- zelf.
CREATE TABLE backup_watch (
    connection_id  uuid NOT NULL REFERENCES proxmox_connections (id) ON DELETE CASCADE,
    vmid           integer NOT NULL CHECK (vmid BETWEEN 100 AND 999999999),
    label          text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (connection_id, vmid)
);

-- Laatst berekende versheid per bewaakte VM. Alleen om een overgang te
-- herkennen: elke wissel geeft precies één event.
CREATE TABLE backup_status (
    connection_id     uuid NOT NULL REFERENCES proxmox_connections (id) ON DELETE CASCADE,
    vmid              integer NOT NULL,
    freshness         text NOT NULL CHECK (freshness IN ('ok', 'stale', 'missing')),
    latest_backup_at  timestamptz,
    changed_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (connection_id, vmid)
);

-- +goose Down
DROP TABLE backup_status;
DROP TABLE backup_watch;
DROP TABLE backup_policies;
ALTER TABLE proxmox_connections
    DROP COLUMN not_backed_up,
    DROP COLUMN backup_error,
    DROP COLUMN backup_checked_at;
DROP TABLE proxmox_backups;
