-- +goose Up
-- Het adres waarmee nieuwe nodes uit Git ClusterForge bereiken: bij omhoog
-- schalen en bij een nieuw cluster schrijft de taak het in hun
-- aanmeldbestand. Leeg betekent nog niet ingevuld.
ALTER TABLE git_repos ADD COLUMN server_url text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE git_repos DROP COLUMN server_url;
