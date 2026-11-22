-- +goose Up
-- De instellingen van de assistent. Er is hoogstens één rij. De API-sleutel
-- is versleuteld met de masterkey (AAD assistant:<id>) en komt nooit terug
-- uit de API.
CREATE TABLE assistant_settings (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    enabled             boolean NOT NULL DEFAULT false,
    api_key_enc         bytea,
    key_id              text NOT NULL DEFAULT '',
    model               text NOT NULL DEFAULT 'claude-opus-5-5',
    monthly_budget_usd  integer NOT NULL DEFAULT 20,
    viewer_access       boolean NOT NULL DEFAULT false,
    updated_by          uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX assistant_settings_single ON assistant_settings ((true));

-- Een gesprek van één gebruiker. subject is het onderwerp van de vraagknop
-- (cluster, node of incident), als het gesprek daar begon. Bewaard 365 dagen.
CREATE TABLE assistant_conversations (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title         text NOT NULL DEFAULT '',
    subject_type  text NOT NULL DEFAULT '' CHECK (subject_type IN ('', 'cluster', 'node', 'incident')),
    subject_id    uuid,
    incident_id   uuid REFERENCES incidents (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX assistant_conversations_user ON assistant_conversations (user_id, updated_at DESC);

-- De berichten van een gesprek, in de volgorde waarin ze naar de API gingen:
-- een vraag, antwoorden van het model en de resultaten van de tools. content
-- zijn de blokken van de API, precies zoals ze verstuurd of ontvangen zijn,
-- zodat een volgende vraag dezelfde geschiedenis meestuurt. text is wat de
-- webinterface toont: de vraag of de tekst van het antwoord. De kosten staan
-- in miljoensten van een dollar.
CREATE TABLE assistant_messages (
    id                  bigserial PRIMARY KEY,
    conversation_id     uuid NOT NULL REFERENCES assistant_conversations (id) ON DELETE CASCADE,
    seq                 integer NOT NULL,
    role                text NOT NULL CHECK (role IN ('question', 'answer', 'tool_results')),
    content             jsonb NOT NULL,
    text                text NOT NULL DEFAULT '',
    model               text NOT NULL DEFAULT '',
    input_tokens        integer NOT NULL DEFAULT 0,
    output_tokens       integer NOT NULL DEFAULT 0,
    cache_read_tokens   integer NOT NULL DEFAULT 0,
    cache_write_tokens  integer NOT NULL DEFAULT 0,
    cost_microusd       bigint NOT NULL DEFAULT 0,
    stop_reason         text NOT NULL DEFAULT '',
    user_id             uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, seq)
);
-- Voor het verbruik per maand en de limiet per uur.
CREATE INDEX assistant_messages_created ON assistant_messages (created_at);

-- +goose Down
DROP TABLE assistant_messages;
DROP TABLE assistant_conversations;
DROP TABLE assistant_settings;
