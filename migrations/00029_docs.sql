-- +goose Up
-- Documentatie. Elke versie van een document staat hier, maar alleen als de
-- inhoud veranderde, zodat de geschiedenis alleen echte wijzigingen toont.
-- De nieuwste 50 versies per document blijven bewaard.
CREATE TABLE doc_snapshots (
    id           bigserial PRIMARY KEY,
    scope        text NOT NULL CHECK (scope IN ('overview', 'cluster')),
    cluster_id   uuid REFERENCES clusters (id) ON DELETE CASCADE,
    kind         text NOT NULL,
    content      text NOT NULL,
    sha256       text NOT NULL,
    generated_at timestamptz NOT NULL DEFAULT now(),
    -- cause is waarom deze versie geschreven werd: een event (event:<id>),
    -- de nacht, de start van de server of een beheerder.
    cause        text NOT NULL DEFAULT '',
    CHECK ((scope = 'cluster') = (cluster_id IS NOT NULL))
);
CREATE INDEX doc_snapshots_doc_idx ON doc_snapshots (scope, kind, cluster_id, id DESC);

-- +goose Down
DROP TABLE doc_snapshots;
