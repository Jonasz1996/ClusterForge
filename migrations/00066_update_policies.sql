-- +goose Up
-- Automatisch bijwerken (fase 15): een beleid per omgeving, en per cluster
-- eventueel een eigen beleid of uit.
CREATE TABLE update_policies (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    environment     environment UNIQUE,
    cluster_id      uuid UNIQUE REFERENCES clusters (id) ON DELETE CASCADE,
    -- enabled false op een cluster: nooit vanzelf, ook als de omgeving aan
    -- staat.
    enabled         boolean NOT NULL DEFAULT false,
    apt             boolean NOT NULL DEFAULT true,
    apt_full        boolean NOT NULL DEFAULT true,
    apt_autoremove  boolean NOT NULL DEFAULT false,
    docker          boolean NOT NULL DEFAULT true,
    docker_prune    boolean NOT NULL DEFAULT false,
    reboot          boolean NOT NULL DEFAULT false,
    -- update_window zoals "zo 03:00-05:00" (planner.ParseWindow).
    update_window   text NOT NULL DEFAULT 'zo 03:00-05:00',
    updated_at      timestamptz NOT NULL DEFAULT now(),
    updated_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    CHECK ((environment IS NULL) <> (cluster_id IS NULL))
);

-- Wanneer een cluster laatst vanzelf aan de beurt was, zodat een venster
-- één keer telt.
CREATE TABLE update_auto_runs (
    cluster_id  uuid PRIMARY KEY REFERENCES clusters (id) ON DELETE CASCADE,
    started_at  timestamptz NOT NULL,
    job_id      uuid REFERENCES jobs (id) ON DELETE SET NULL,
    -- skipped zegt waarom het venster niets startte; leeg als er een taak
    -- kwam of niets te doen was.
    note        text NOT NULL DEFAULT ''
);

-- Het rapport van de laatste Docker-update per node en project: wat er
-- veranderde, de test, de logs en het voorstel voor een fix.
CREATE TABLE docker_update_reports (
    node_id         uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    project         text NOT NULL,
    job_id          uuid REFERENCES jobs (id) ON DELETE SET NULL,
    result          text NOT NULL,
    report          jsonb NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    conversation_id uuid REFERENCES assistant_conversations (id) ON DELETE SET NULL,
    PRIMARY KEY (node_id, project)
);

-- +goose Down
DROP TABLE docker_update_reports;
DROP TABLE update_auto_runs;
DROP TABLE update_policies;
