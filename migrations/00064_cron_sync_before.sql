-- +goose Up
-- Een sync kan een voorbereiding hebben: een commando dat de bron vóór rsync
-- draait, zoals een consistente kopie van een SQLite-database (fase 11,
-- Nginx Proxy Manager als cluster). Leeg: geen.
ALTER TABLE cron_jobs ADD COLUMN sync_before text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE cron_jobs DROP COLUMN sync_before;
