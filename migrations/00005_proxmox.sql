-- +goose Up
-- Een Proxmox-omgeving: één host of een cluster achter één API-adres.
CREATE TABLE proxmox_connections (
    id                uuid PRIMARY KEY,
    name              text NOT NULL,
    api_url           text NOT NULL,
    token_id          text NOT NULL,
    -- Het secret van het API-token, versleuteld met de masterkey; key_id
    -- herkent die sleutel.
    token_secret_enc  bytea NOT NULL,
    key_id            text NOT NULL,
    -- SHA-256 van het certificaat in hex; leeg betekent: gewone CA-controle.
    tls_fingerprint   text NOT NULL DEFAULT '',
    pve_version       text NOT NULL DEFAULT '',
    last_sync_at      timestamptz,
    last_error        text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX proxmox_connections_name_key ON proxmox_connections (lower(name));

-- Kopie van /cluster/resources van de laatste sync: hosts, VM's, containers
-- en storage.
CREATE TABLE proxmox_resources (
    connection_id  uuid NOT NULL REFERENCES proxmox_connections (id) ON DELETE CASCADE,
    -- id zoals Proxmox het geeft, bijvoorbeeld qemu/101 of node/pve1.
    pve_id         text NOT NULL,
    type           text NOT NULL,
    pve_node       text NOT NULL DEFAULT '',
    vmid           integer,
    name           text NOT NULL DEFAULT '',
    status         text NOT NULL DEFAULT '',
    template       boolean NOT NULL DEFAULT false,
    data           jsonb NOT NULL,
    synced_at      timestamptz NOT NULL,
    PRIMARY KEY (connection_id, pve_id)
);
CREATE INDEX proxmox_resources_vmid_idx ON proxmox_resources (connection_id, vmid);

-- Een node kan een VM of container in Proxmox zijn.
ALTER TABLE nodes
    ADD COLUMN proxmox_id uuid REFERENCES proxmox_connections (id),
    ADD COLUMN pve_vmid   integer,
    ADD CONSTRAINT nodes_proxmox_link CHECK ((proxmox_id IS NULL) = (pve_vmid IS NULL));
CREATE UNIQUE INDEX nodes_proxmox_vm_key ON nodes (proxmox_id, pve_vmid);

CREATE TYPE job_status AS ENUM ('queued', 'running', 'succeeded', 'failed', 'canceled');

-- Taken die de server uitvoert, zoals een VM starten of migreren.
CREATE TABLE jobs (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind              text NOT NULL,
    title             text NOT NULL,
    status            job_status NOT NULL DEFAULT 'queued',
    params            jsonb NOT NULL DEFAULT '{}',
    cluster_id        uuid REFERENCES clusters (id) ON DELETE SET NULL,
    node_id           uuid REFERENCES nodes (id) ON DELETE SET NULL,
    proxmox_id        uuid REFERENCES proxmox_connections (id) ON DELETE SET NULL,
    requested_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    error             text NOT NULL DEFAULT '',
    cancel_requested  boolean NOT NULL DEFAULT false,
    attempts          integer NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now(),
    started_at        timestamptz,
    finished_at       timestamptz,
    -- Een lopende taak werkt dit elke paar seconden bij; staat het stil, dan
    -- is de server gestopt en wordt de taak hervat.
    heartbeat_at      timestamptz
);
CREATE INDEX jobs_queue_idx ON jobs (created_at) WHERE status = 'queued';
CREATE INDEX jobs_created_idx ON jobs (created_at DESC);
CREATE INDEX jobs_node_idx ON jobs (node_id, created_at DESC);

-- Stappen van een taak. state bewaart wat nodig is om een onderbroken stap te
-- hervatten, zoals het id van de Proxmox-taak.
CREATE TABLE job_steps (
    job_id       uuid NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    seq          integer NOT NULL,
    name         text NOT NULL,
    status       job_status NOT NULL,
    started_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz,
    state        jsonb NOT NULL DEFAULT '{}',
    log          text[] NOT NULL DEFAULT '{}',
    error        text NOT NULL DEFAULT '',
    PRIMARY KEY (job_id, seq)
);

-- +goose Down
DROP TABLE job_steps;
DROP TABLE jobs;
DROP TYPE job_status;
DROP INDEX nodes_proxmox_vm_key;
ALTER TABLE nodes DROP CONSTRAINT nodes_proxmox_link, DROP COLUMN pve_vmid, DROP COLUMN proxmox_id;
DROP TABLE proxmox_resources;
DROP TABLE proxmox_connections;
