-- +goose Up
-- Fase 49: de uitzonderingen die Jonas op 28/11 gaf. Aan te passen op de
-- pagina Publicatie → Uitzonderingen.
INSERT INTO publish_policies (name, mode, paths, note) VALUES
    ('authentik', 'never', '{}', 'Authentik zelf'),
    ('guest-portal', 'never', '{}', ''),
    ('rustdesk-api', 'never', '{}', ''),
    ('cssthema', 'never', '{}', 'css voor het thema moet open zijn'),
    ('media', 'own_login', '{}', ''),
    ('vaultx', 'own_login', '{}', ''),
    ('epgiptv', 'except_paths', '{/guide.xml,/channels.xml}', 'IPTV-apps halen de gids zonder login'),
    ('matomo', 'except_paths', '{/matomo.js,/matomo.php}', 'tracking van andere sites'),
    ('vaultx-api', 'except_paths', '{/api}', 'FastAPI van Vault-X als aparte host (om te testen); vaultx zelf is de nginx met eigen login')
ON CONFLICT (name) DO NOTHING;

-- +goose Down
DELETE FROM publish_policies WHERE name IN ('authentik', 'guest-portal', 'rustdesk-api', 'cssthema', 'media', 'vaultx', 'epgiptv', 'matomo', 'vaultx-api');
