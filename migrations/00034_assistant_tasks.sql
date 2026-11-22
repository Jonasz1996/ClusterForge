-- +goose Up
-- Automatisch analyseren van nieuwe prod-incidenten; standaard uit.
ALTER TABLE assistant_settings ADD COLUMN auto_analyze boolean NOT NULL DEFAULT false;

-- Een gesprek is een vraag van een gebruiker (chat), of een vaste opdracht:
-- een analyse van een incident, de uitleg van een bevinding of SPOF, of een
-- samenvatting van de documentatie. Een gesprek zonder gebruiker is van
-- ClusterForge zelf: een automatische analyse.
ALTER TABLE assistant_conversations
    ALTER COLUMN user_id DROP NOT NULL,
    ADD COLUMN kind text NOT NULL DEFAULT 'chat' CHECK (kind IN ('chat', 'analysis', 'explain', 'summary'));
CREATE INDEX assistant_conversations_system ON assistant_conversations (created_at) WHERE user_id IS NULL;

-- De analyse van de assistent bij een incident, met wie ze vroeg of dat ze
-- automatisch was, het gesprek met de tools en de kosten. ai_scope is met
-- wiens rechten de tools lazen: een analyse met de rechten van een
-- beheerder ziet een viewer niet.
ALTER TABLE incidents
    ADD COLUMN ai_analysis        text,
    ADD COLUMN ai_model           text NOT NULL DEFAULT '',
    ADD COLUMN ai_at              timestamptz,
    ADD COLUMN ai_by              uuid REFERENCES users (id) ON DELETE SET NULL,
    ADD COLUMN ai_automatic       boolean NOT NULL DEFAULT false,
    ADD COLUMN ai_scope           text NOT NULL DEFAULT '' CHECK (ai_scope IN ('', 'admin', 'viewer')),
    ADD COLUMN ai_conversation_id uuid REFERENCES assistant_conversations (id) ON DELETE SET NULL,
    ADD COLUMN ai_cost_microusd   bigint NOT NULL DEFAULT 0;

-- De samenvatting 'In het kort' van de documentatie van een cluster, op
-- verzoek geschreven door de assistent. Ze staat bovenaan het hoofdstuk
-- Architectuur tot iemand een nieuwe vraagt of haar weghaalt.
CREATE TABLE doc_summaries (
    cluster_id      uuid PRIMARY KEY REFERENCES clusters (id) ON DELETE CASCADE,
    text            text NOT NULL CHECK (length(text) BETWEEN 1 AND 4000),
    model           text NOT NULL,
    created_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    conversation_id uuid REFERENCES assistant_conversations (id) ON DELETE SET NULL
);

-- +goose Down
DROP TABLE doc_summaries;
ALTER TABLE incidents
    DROP COLUMN ai_analysis, DROP COLUMN ai_model, DROP COLUMN ai_at, DROP COLUMN ai_by, DROP COLUMN ai_automatic, DROP COLUMN ai_scope,
    DROP COLUMN ai_conversation_id, DROP COLUMN ai_cost_microusd;
DROP INDEX assistant_conversations_system;
DELETE FROM assistant_conversations WHERE user_id IS NULL;
ALTER TABLE assistant_conversations DROP COLUMN kind, ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE assistant_settings DROP COLUMN auto_analyze;
