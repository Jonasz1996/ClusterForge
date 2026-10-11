-- +goose Up
-- De configuratie van elke VM en container uit Proxmox, voor de wat-als:
-- op welke storage de schijven staan en aan welke bridge de netwerkkaarten
-- hangen. De sync leest ze elke 10 minuten. De HA-toestand komt niet hier
-- maar uit /cluster/resources, want die verandert vaker.
CREATE TABLE proxmox_guest_config (
    connection_id  uuid NOT NULL REFERENCES proxmox_connections (id) ON DELETE CASCADE,
    vmid           integer NOT NULL,
    -- [{"key": "scsi0", "storage": "local-lvm", "size_bytes": 34359738368}],
    -- of met "path" voor een map van de host in een container.
    disks          jsonb NOT NULL DEFAULT '[]',
    -- [{"key": "net0", "bridge": "vmbr0", "tag": 20, "mac": "bc:24:11:00:00:01"}]
    nets           jsonb NOT NULL DEFAULT '[]',
    onboot         boolean NOT NULL DEFAULT false,
    -- Adressen uit de configuratie (ip= en ipconfigN) en, voor een
    -- draaiende container, wat Proxmox op zijn interfaces ziet.
    addresses      text[] NOT NULL DEFAULT '{}',
    -- De fout van de laatste keer lezen; dan blijven de vorige gegevens.
    error          text NOT NULL DEFAULT '',
    read_at        timestamptz NOT NULL,
    PRIMARY KEY (connection_id, vmid)
);

-- Waar ClusterForge zelf draait, als een beheerder het invulde. Zonder rij
-- zoekt ClusterForge het zelf aan zijn MAC- en IP-adressen.
CREATE TABLE self_location (
    singleton      boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    connection_id  uuid NOT NULL REFERENCES proxmox_connections (id) ON DELETE CASCADE,
    vmid           integer NOT NULL CHECK (vmid BETWEEN 100 AND 999999999),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     uuid REFERENCES users (id) ON DELETE SET NULL
);

-- +goose Down
DROP TABLE self_location;
DROP TABLE proxmox_guest_config;
