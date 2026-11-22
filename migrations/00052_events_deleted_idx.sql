-- +goose NO TRANSACTION
-- +goose Up
-- Het logboek zoekt bij elke pagina de verwijderde clusters en nodes op;
-- zonder index las dat de hele tabel events, die nooit kleiner wordt.
-- CONCURRENTLY blokkeert het schrijven van events niet tijdens het maken.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_deleted_idx ON events (ts DESC)
    WHERE action IN ('cluster.deleted', 'node.deleted');

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS events_deleted_idx;
