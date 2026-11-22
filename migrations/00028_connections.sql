-- +goose Up
-- De verbindingen per uur, naast de totalen in observed_connections, zodat
-- het bewijs voor een voorgestelde afhankelijkheid "in de afgelopen 24 uur"
-- kan zeggen. samples is hoe vaak de agent de verbinding zag: hij kijkt
-- elke minuut. Rijen ouder dan twee dagen worden opgeruimd.
CREATE TABLE observed_connection_hours (
    node_id    uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    unit       text NOT NULL DEFAULT '',
    direction  text NOT NULL CHECK (direction IN ('out', 'in')),
    peer_addr  text NOT NULL DEFAULT '',
    peer_port  integer NOT NULL DEFAULT 0,
    local_port integer NOT NULL DEFAULT 0,
    hour       timestamptz NOT NULL,
    samples    bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, unit, direction, peer_addr, peer_port, local_port, hour)
);
CREATE INDEX observed_connection_hours_hour_idx ON observed_connection_hours (hour);

-- +goose Down
DROP TABLE observed_connection_hours;
