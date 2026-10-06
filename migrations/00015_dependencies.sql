-- +goose Up
-- Diensten en wie van wie afhangt. Een dienst hoort bij een cluster, bij een
-- losse node of is extern (adres en poort). Instanties worden niet bewaard:
-- ze volgen uit de actieve nodes en de units in hun heartbeat. kind is een
-- vaste lijst in internal/deps, zoals de clustertypes, zodat een nieuwe
-- soort geen migratie vraagt.
CREATE TABLE services (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id   uuid REFERENCES clusters (id) ON DELETE CASCADE,
    node_id      uuid REFERENCES nodes (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (name ~ '^[A-Za-z0-9@._:/-]{1,63}$'),
    kind         text NOT NULL,
    unit         text NOT NULL DEFAULT '',
    port         integer CHECK (port BETWEEN 1 AND 65535),
    address      text NOT NULL DEFAULT '',
    description  text NOT NULL DEFAULT '',
    source       text NOT NULL CHECK (source IN ('template', 'manual', 'discovered')),
    -- Een genegeerde rij blijft als grafsteen staan, zodat de resolver hem
    -- niet opnieuw voorstelt.
    state        text NOT NULL CHECK (state IN ('confirmed', 'suggested', 'ignored')),
    last_seen_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (cluster_id IS NULL OR node_id IS NULL),
    -- Alleen een externe dienst heeft een adres, en dan ook een poort.
    CHECK ((cluster_id IS NULL AND node_id IS NULL) = (address <> '')),
    CHECK (address = '' OR port IS NOT NULL)
);
CREATE UNIQUE INDEX services_name_key ON services (cluster_id, node_id, lower(name)) NULLS NOT DISTINCT;
CREATE UNIQUE INDEX services_external_key ON services (lower(address), port) WHERE address <> '';
CREATE INDEX services_node_idx ON services (node_id) WHERE node_id IS NOT NULL;

-- Een pijl van afnemer (from) naar leverancier (to): "nginx hangt af van
-- keepalived". Wie hem maakte, staat in het event.
CREATE TABLE service_dependencies (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    from_service_id uuid NOT NULL REFERENCES services (id) ON DELETE CASCADE,
    to_service_id   uuid NOT NULL REFERENCES services (id) ON DELETE CASCADE,
    strength        text NOT NULL CHECK (strength IN ('hard', 'soft')),
    source          text NOT NULL CHECK (source IN ('template', 'manual', 'discovered')),
    state           text NOT NULL DEFAULT 'confirmed' CHECK (state IN ('confirmed', 'suggested', 'ignored')),
    note            text NOT NULL DEFAULT '' CHECK (length(note) <= 500),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CHECK (from_service_id <> to_service_id),
    UNIQUE (from_service_id, to_service_id)
);
-- Impact zoekt omgekeerd: wie hangt af van deze dienst?
CREATE INDEX service_dependencies_to_idx ON service_dependencies (to_service_id);

-- Bestaande keepalived-nginx-clusters blijven op hun templateversie en
-- krijgen de diensten van 1.1.0 hier één keer.
INSERT INTO services (cluster_id, name, kind, unit, port, source, state)
SELECT c.id, s.name, s.kind, s.unit, s.port, 'template', 'confirmed'
FROM clusters c
CROSS JOIN (VALUES ('nginx', 'web', 'nginx', 80), ('keepalived', 'vip', 'keepalived', NULL::integer)) AS s (name, kind, unit, port)
WHERE c.template_name = 'keepalived-nginx';

INSERT INTO service_dependencies (from_service_id, to_service_id, strength, source)
SELECT w.id, v.id, 'hard', 'template'
FROM services w
JOIN services v ON v.cluster_id = w.cluster_id AND v.name = 'keepalived' AND v.source = 'template'
WHERE w.name = 'nginx' AND w.source = 'template';

-- +goose Down
DROP TABLE service_dependencies;
DROP TABLE services;
