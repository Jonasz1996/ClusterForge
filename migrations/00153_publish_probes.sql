-- +goose Up
-- Testlijst 3: ClusterForge bewaart de laatste antwoorden van doelen die het
-- zelf bekeek (probes) naast de lezingen van NPM en Authentik.
ALTER TABLE publish_reads DROP CONSTRAINT publish_reads_kind_check;
ALTER TABLE publish_reads ADD CONSTRAINT publish_reads_kind_check CHECK (kind IN ('npm', 'authentik', 'probes'));

-- +goose Down
DELETE FROM publish_reads WHERE kind = 'probes';
ALTER TABLE publish_reads DROP CONSTRAINT publish_reads_kind_check;
ALTER TABLE publish_reads ADD CONSTRAINT publish_reads_kind_check CHECK (kind IN ('npm', 'authentik'));
