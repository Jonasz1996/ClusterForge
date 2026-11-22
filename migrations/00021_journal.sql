-- +goose Up
-- Het wijzigingsjournaal: wat er op de nodes en in Proxmox veranderde, ook
-- buiten ClusterForge om. Anders dan het logboek (events) zegt het wat er op
-- de machines gebeurde, niet wat ClusterForge en zijn gebruikers deden, en
-- het wordt na 180 dagen opgeruimd. Nooit bestandsinhoud of een hash.
CREATE TABLE node_changes (
    id          bigserial PRIMARY KEY,
    node_id     uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    -- Het cluster op het moment zelf.
    cluster_id  uuid REFERENCES clusters (id) ON DELETE SET NULL,
    -- De tijd op de node, gecorrigeerd voor de klokafwijking.
    ts          timestamptz NOT NULL,
    observed_at timestamptz NOT NULL DEFAULT now(),
    source      text NOT NULL CHECK (source IN ('agent', 'server', 'proxmox', 'clusterforge')),
    kind        text NOT NULL,
    subject     text NOT NULL DEFAULT '',
    action      text NOT NULL,
    summary     text NOT NULL,
    -- Van, naar, door en een paar vaste velden; zie internal/journal.
    detail      jsonb NOT NULL DEFAULT '{}'::jsonb,
    job_id      uuid,
    CONSTRAINT node_changes_unique UNIQUE (node_id, kind, subject, ts, action)
);
CREATE INDEX node_changes_node_idx ON node_changes (node_id, ts DESC, id DESC);
CREATE INDEX node_changes_cluster_idx ON node_changes (cluster_id, ts DESC, id DESC) WHERE cluster_id IS NOT NULL;
CREATE INDEX node_changes_ts_idx ON node_changes (ts);

-- Een nieuwe regel is een live update voor de webinterface, één per
-- statement, zodat een portie van 500 regels één melding geeft.
-- +goose StatementBegin
CREATE FUNCTION notify_node_changes() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('cf_events', json_build_object('journal', true)::text);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER node_changes_notify AFTER INSERT ON node_changes
    FOR EACH STATEMENT EXECUTE FUNCTION notify_node_changes();

-- Taken uit /cluster/tasks van Proxmox: wie een VM stopte, migreerde of er
-- een back-up van maakte. De laatste 30 dagen.
CREATE TABLE proxmox_tasks (
    connection_id uuid NOT NULL REFERENCES proxmox_connections (id) ON DELETE CASCADE,
    upid          text NOT NULL,
    pve_node      text NOT NULL,
    type          text NOT NULL,
    vmid          integer,
    username      text NOT NULL DEFAULT '',
    -- Leeg zolang de taak loopt; daarna OK, WARNINGS: n of de fout.
    exitstatus    text NOT NULL DEFAULT '',
    started_at    timestamptz NOT NULL,
    ended_at      timestamptz,
    PRIMARY KEY (connection_id, upid)
);
CREATE INDEX proxmox_tasks_vmid_idx ON proxmox_tasks (connection_id, vmid, started_at DESC);
CREATE INDEX proxmox_tasks_started_idx ON proxmox_tasks (started_at);

-- Verbindingen die de agent zag. Van een inkomende verbinding staat de
-- afzender er alleen als dat een bekende node is.
CREATE TABLE observed_connections (
    node_id    uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    unit       text NOT NULL DEFAULT '',
    direction  text NOT NULL CHECK (direction IN ('out', 'in')),
    peer_addr  text NOT NULL DEFAULT '',
    peer_port  integer NOT NULL DEFAULT 0,
    local_port integer NOT NULL DEFAULT 0,
    first_seen timestamptz NOT NULL,
    last_seen  timestamptz NOT NULL,
    samples    bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, unit, direction, peer_addr, peer_port, local_port)
);
CREATE INDEX observed_connections_seen_idx ON observed_connections (last_seen);

-- De klokafwijking van de node uit de laatste heartbeat: de tijd in de
-- envelope min de tijd van ontvangst. Positief is een voorlopende klok.
ALTER TABLE node_status ADD COLUMN clock_offset_ms integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE node_status DROP COLUMN clock_offset_ms;
DROP TABLE observed_connections;
DROP TABLE proxmox_tasks;
DROP TRIGGER node_changes_notify ON node_changes;
DROP FUNCTION notify_node_changes();
DROP TABLE node_changes;
