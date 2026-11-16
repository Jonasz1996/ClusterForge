-- +goose Up
-- Beveiliging (fase 27): eigen scopes, de pakketlijst per node, de
-- kwetsbaarheidsfeeds (Debian Security Tracker, CISA KEV) en de bevindingen.
-- Fase 26 houdt 00073-00079 vrij; fase 27 begint bij 00080.

-- Netwerken en adressen die van de beheerder zijn. Alleen daarbinnen mag
-- ClusterForge later scannen.
CREATE TABLE security_scopes (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 60),
    description text NOT NULL DEFAULT '',
    cidrs       text[] NOT NULL CHECK (cardinality(cidrs) > 0),
    exclude     text[] NOT NULL DEFAULT '{}',
    -- public: er zit een publiek adres in; dat vraagt een bevestiging met
    -- een notitie (waarom dit van jou is).
    public      boolean NOT NULL DEFAULT false,
    public_note text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  uuid REFERENCES users (id) ON DELETE SET NULL
);

-- De laatste pakketlijst van elke node (agentprotocol 40).
CREATE TABLE security_inventory (
    node_id      uuid PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    collected_at timestamptz,
    os_id        text NOT NULL DEFAULT '',
    codename     text NOT NULL DEFAULT '',
    kernel       text NOT NULL DEFAULT '',
    dpkg         boolean NOT NULL DEFAULT false,
    packages     jsonb NOT NULL DEFAULT '[]',
    error        text NOT NULL DEFAULT '',
    error_at     timestamptz
);

-- De stand van elke feed.
CREATE TABLE security_feeds (
    name        text PRIMARY KEY,
    fetched_at  timestamptz,
    ok_at       timestamptz,
    error       text NOT NULL DEFAULT '',
    entries     integer NOT NULL DEFAULT 0,
    etag        text NOT NULL DEFAULT ''
);

-- Debian Security Tracker: per release en bronpakket de CVE's. Alleen de
-- releases die op de nodes draaien (plus bookworm en trixie).
CREATE TABLE security_debian (
    release       text NOT NULL,
    package       text NOT NULL,
    cve           text NOT NULL,
    status        text NOT NULL,
    fixed_version text NOT NULL DEFAULT '',
    urgency       text NOT NULL DEFAULT '',
    PRIMARY KEY (release, package, cve)
);

CREATE TABLE security_cves (
    cve         text PRIMARY KEY,
    description text NOT NULL DEFAULT '',
    scope       text NOT NULL DEFAULT ''
);

-- CISA Known Exploited Vulnerabilities: actief misbruikt.
CREATE TABLE security_kev (
    cve        text PRIMARY KEY,
    vendor     text NOT NULL DEFAULT '',
    product    text NOT NULL DEFAULT '',
    name       text NOT NULL DEFAULT '',
    date_added date,
    ransomware boolean NOT NULL DEFAULT false
);

-- Bevindingen van alle soorten (cve nu; later poort, verharding, tls,
-- paneel). subject is de node-id of een adres; key maakt hem uniek binnen
-- de soort.
CREATE TABLE security_findings (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind           text NOT NULL,
    subject        text NOT NULL,
    key            text NOT NULL,
    node_id        uuid REFERENCES nodes (id) ON DELETE CASCADE,
    severity       text NOT NULL CHECK (severity IN ('kritiek', 'hoog', 'middel', 'laag', 'info')),
    title          text NOT NULL,
    summary        text NOT NULL DEFAULT '',
    fix            text NOT NULL DEFAULT '',
    detail         jsonb NOT NULL DEFAULT '{}',
    status         text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'aanvaard', 'opgelost')),
    first_seen     timestamptz NOT NULL DEFAULT now(),
    last_seen      timestamptz NOT NULL DEFAULT now(),
    resolved_at    timestamptz,
    accepted_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    accepted_at    timestamptz,
    accepted_until timestamptz,
    accepted_note  text NOT NULL DEFAULT '',
    UNIQUE (kind, subject, key)
);
CREATE INDEX security_findings_open ON security_findings (status, severity) WHERE status <> 'opgelost';
CREATE INDEX security_findings_node ON security_findings (node_id);

-- +goose Down
DROP TABLE security_findings;
DROP TABLE security_kev;
DROP TABLE security_cves;
DROP TABLE security_debian;
DROP TABLE security_feeds;
DROP TABLE security_inventory;
DROP TABLE security_scopes;
