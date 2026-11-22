-- +goose Up
-- Wie gebruikt welke database (fase 5, mijlpaal 2): de verbindingen die
-- de agent op een databaseserver ziet, per database, gebruiker en adres.
-- Een rij blijft dertig dagen na de laatste keer staan, zodat ook een
-- cronjob die af en toe verbindt meetelt.
CREATE TABLE db_clients (
    node_id      uuid NOT NULL,
    engine       text NOT NULL,
    db_name      text NOT NULL,
    db_user      text NOT NULL,
    -- Het adres van de client; leeg voor de node zelf (socket of loopback).
    address      text NOT NULL,
    -- Hoeveel verbindingen er bij de laatste keer lezen waren; 0 als de
    -- client toen niet verbonden was.
    connections  integer NOT NULL DEFAULT 0,
    first_seen   timestamptz NOT NULL,
    last_seen    timestamptz NOT NULL,
    PRIMARY KEY (node_id, engine, db_name, db_user, address),
    FOREIGN KEY (node_id, engine) REFERENCES db_servers (node_id, engine) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE db_clients;
