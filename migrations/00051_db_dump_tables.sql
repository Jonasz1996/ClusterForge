-- +goose Up
-- De scorekaart Database (fase 5, mijlpaal 15). Na een goede dump van een
-- database van MariaDB Galera kijkt ClusterForge welke tabellen geen
-- primaire sleutel hebben of niet in InnoDB staan: Galera repliceert die
-- traag of helemaal niet. NULL is onbekend, zoals bij PostgreSQL of een
-- dump van voor deze migratie.
ALTER TABLE db_dumps
    ADD COLUMN no_primary_key text[],
    ADD COLUMN not_innodb text[];

-- +goose Down
ALTER TABLE db_dumps
    DROP COLUMN not_innodb,
    DROP COLUMN no_primary_key;
