-- +goose Up
-- Fase 42b: algemeen sjabloon voor Advanced van de proxy hosts, met de
-- css per host. Leeg sjabloon = het standaardsjabloon van ClusterForge.
CREATE TABLE publish_theme (
    id             boolean PRIMARY KEY DEFAULT true CHECK (id),
    template_auth  text NOT NULL DEFAULT '',
    template_plain text NOT NULL DEFAULT '',
    css_default    text NOT NULL DEFAULT '',
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE publish_host_css (
    host_id    integer PRIMARY KEY,
    css        text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE publish_host_css;
DROP TABLE publish_theme;
