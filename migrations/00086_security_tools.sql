-- +goose Up
-- Fase 27 M7: beveiligingstools op de nodes (ClamAV, AIDE, rkhunter,
-- chkrootkit, Lynis, auditd, Trivy). state is de laatste stand van de
-- agent (protocol.ToolState); schedule plant de run.
CREATE TABLE security_tools (
    node_id     uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    tool        text NOT NULL,
    installed   boolean NOT NULL DEFAULT false,
    version     text NOT NULL DEFAULT '',
    running     text NOT NULL DEFAULT '',
    state       jsonb NOT NULL DEFAULT '{}',
    schedule    text NOT NULL DEFAULT 'uit',
    weekday     int NOT NULL DEFAULT 0,
    hour        int NOT NULL DEFAULT 3,
    next_run_at timestamptz,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (node_id, tool)
);
CREATE TABLE security_tool_nodes (
    node_id    uuid PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    fetched_at timestamptz,
    error      text NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE security_tool_nodes;
DROP TABLE security_tools;
