-- +goose Up
-- Scopes op een API-token (least-privilege, fase: pentest-integratie). Leeg =
-- alle lees-routes (zoals voorheen, backward compatible voor bestaande
-- dashboard-tokens). Niet-leeg = het token mag alleen de genoemde
-- capabilities lezen: 'pentest' = enkel de pentest-inventaris
-- (/api/v1/pentest/nodes), 'dashboard' = enkel de dashboard-API. Zo kan het
-- orchestrator-token tot alleen de inventaris beperkt worden.
ALTER TABLE api_tokens ADD COLUMN scopes text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE api_tokens DROP COLUMN scopes;
