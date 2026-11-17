-- +goose Up
-- Fase 40 M3: regeladvies. Voorstellen die je negeerde (hun sleutel).
ALTER TABLE opnsense_auto ADD COLUMN advice_ignored text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE opnsense_auto DROP COLUMN advice_ignored;
