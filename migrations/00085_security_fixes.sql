-- +goose Up
-- Fase 27 M6: veilige oplossingen met één klik. Eén rij per klik per node;
-- SSH-wijzigingen wachten op bevestiging (anders zet de node ze terug).
CREATE TABLE security_fix_runs (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id        uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    rules          text[] NOT NULL,
    outcomes       jsonb NOT NULL DEFAULT '[]',
    error          text NOT NULL DEFAULT '',
    ssh_pending    boolean NOT NULL DEFAULT false,
    rollback_at    timestamptz,
    confirmed_at   timestamptz,
    rolled_back_at timestamptz,
    created_by     uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX security_fix_runs_node ON security_fix_runs (node_id, created_at DESC);

-- +goose Down
DROP TABLE security_fix_runs;
