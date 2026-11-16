-- +goose Up
-- Fase 27 M3: netwerkscans binnen de eigen scopes. Een scan hoort bij één
-- scope en draait op een scanner: een node met nmap (de scannercontainer
-- of een eigen scanner-VM). Elke uitvoering bewaart wat nmap vond.
CREATE TABLE security_scans (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 60),
    scope_id    uuid NOT NULL REFERENCES security_scopes (id) ON DELETE CASCADE,
    -- scanner_id NULL: de eerste scanner die online is.
    scanner_id  uuid REFERENCES nodes (id) ON DELETE SET NULL,
    profile     text NOT NULL DEFAULT 'standaard' CHECK (profile IN ('snel', 'standaard', 'volledig')),
    max_rate    integer NOT NULL DEFAULT 300 CHECK (max_rate BETWEEN 10 AND 5000),
    schedule    text NOT NULL DEFAULT 'wekelijks' CHECK (schedule IN ('uit', 'dagelijks', 'wekelijks')),
    weekday     integer NOT NULL DEFAULT 0 CHECK (weekday BETWEEN 0 AND 6),
    hour        integer NOT NULL DEFAULT 2 CHECK (hour BETWEEN 0 AND 23),
    next_run    timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  uuid REFERENCES users (id) ON DELETE SET NULL
);

CREATE TABLE security_scan_runs (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    scan_id      uuid NOT NULL REFERENCES security_scans (id) ON DELETE CASCADE,
    scanner_id   uuid REFERENCES nodes (id) ON DELETE SET NULL,
    scanner_name text NOT NULL DEFAULT '',
    trigger      text NOT NULL CHECK (trigger IN ('schema', 'hand')),
    status       text NOT NULL DEFAULT 'bezig' CHECK (status IN ('bezig', 'klaar', 'mislukt')),
    error        text NOT NULL DEFAULT '',
    started_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz,
    targets      text[] NOT NULL DEFAULT '{}',
    hosts_up     integer NOT NULL DEFAULT 0,
    open_ports   integer NOT NULL DEFAULT 0,
    findings     integer NOT NULL DEFAULT 0,
    result       jsonb
);
CREATE INDEX security_scan_runs_scan ON security_scan_runs (scan_id, started_at DESC);

-- +goose Down
DROP TABLE security_scan_runs;
DROP TABLE security_scans;
