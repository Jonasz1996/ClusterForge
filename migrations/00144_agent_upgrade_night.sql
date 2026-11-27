-- +goose Up
-- Testlijst 2 (23/11), punt 2.10: vijf agentupdates op één dag verbraken
-- telkens de verbinding. Vanzelf bijwerken gebeurt standaard alleen 's
-- nachts (02:00-05:00, Brussel); dan in één keer naar de nieuwste agent.
-- De knop Bijwerken werkt altijd meteen.
ALTER TABLE agent_upgrade_settings ADD COLUMN night_only boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE agent_upgrade_settings DROP COLUMN night_only;
