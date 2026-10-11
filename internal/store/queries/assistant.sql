-- name: GetAssistantSettings :one
SELECT * FROM assistant_settings LIMIT 1;

-- name: CreateAssistantSettings :one
INSERT INTO assistant_settings (id, enabled, api_key_enc, key_id, model, monthly_budget_usd, viewer_access, auto_analyze, updated_by)
VALUES (@id, @enabled, @api_key_enc, @key_id, @model, @monthly_budget_usd, @viewer_access, @auto_analyze, @updated_by)
RETURNING *;

-- name: UpdateAssistantSettings :one
UPDATE assistant_settings
SET enabled = @enabled, api_key_enc = @api_key_enc, key_id = @key_id, model = @model,
    monthly_budget_usd = @monthly_budget_usd, viewer_access = @viewer_access, auto_analyze = @auto_analyze, updated_by = @updated_by,
    updated_at = now()
WHERE id = @id
RETURNING *;

-- name: CreateConversation :one
INSERT INTO assistant_conversations (user_id, title, subject_type, subject_id, incident_id, kind)
VALUES (@user_id, @title, @subject_type, @subject_id, @incident_id, @kind)
RETURNING *;

-- name: GetConversation :one
-- Een gesprek zonder gebruiker is van ClusterForge zelf; de naam is dan leeg.
SELECT c.*, coalesce(u.username, '')::text AS username
FROM assistant_conversations c
LEFT JOIN users u ON u.id = c.user_id
WHERE c.id = $1;

-- name: ListConversations :many
-- De gesprekken van een gebruiker, of van iedereen voor een admin, met wat
-- ze kostten.
SELECT c.*, coalesce(u.username, '')::text AS username,
       coalesce((SELECT sum(m.cost_microusd) FROM assistant_messages m WHERE m.conversation_id = c.id), 0)::bigint AS cost_microusd
FROM assistant_conversations c
LEFT JOIN users u ON u.id = c.user_id
WHERE @everyone::boolean OR c.user_id = @user_id
ORDER BY c.updated_at DESC
LIMIT @lim;

-- name: DeleteConversation :execrows
DELETE FROM assistant_conversations WHERE id = $1;

-- name: TouchConversation :exec
UPDATE assistant_conversations
SET updated_at = now(), title = CASE WHEN title = '' THEN @title ELSE title END
WHERE id = @id;

-- name: ListConversationMessages :many
SELECT * FROM assistant_messages WHERE conversation_id = $1 ORDER BY seq;

-- name: InsertAssistantMessage :one
INSERT INTO assistant_messages (conversation_id, seq, role, content, text, model, input_tokens, output_tokens,
                                cache_read_tokens, cache_write_tokens, cost_microusd, stop_reason, user_id)
VALUES (@conversation_id,
        (SELECT coalesce(max(seq), 0) + 1 FROM assistant_messages WHERE conversation_id = @conversation_id),
        @role, @content, @text, @model, @input_tokens, @output_tokens, @cache_read_tokens, @cache_write_tokens,
        @cost_microusd, @stop_reason, @user_id)
RETURNING *;

-- name: AssistantCostSince :one
SELECT coalesce(sum(cost_microusd), 0)::bigint FROM assistant_messages WHERE created_at >= @since;

-- name: AssistantUsageByUser :many
SELECT m.user_id, coalesce(u.username, '')::text AS username,
       count(*) FILTER (WHERE m.role = 'question')::bigint AS questions,
       coalesce(sum(m.cost_microusd), 0)::bigint AS cost_microusd
FROM assistant_messages m
LEFT JOIN users u ON u.id = m.user_id
WHERE m.created_at >= @since
GROUP BY m.user_id, u.username
ORDER BY cost_microusd DESC;

-- name: CountQuestionsSince :one
SELECT count(*) FROM assistant_messages
WHERE user_id = @user_id AND role = 'question' AND created_at >= @since;

-- name: PruneConversations :execrows
DELETE FROM assistant_conversations WHERE updated_at < @before;

-- name: EventSince :one
SELECT EXISTS (SELECT 1 FROM events WHERE action = @action AND ts >= @since);

-- name: GetIncidentIDByNumber :one
SELECT id FROM incidents WHERE number = @number;

-- name: CountSystemConversationsSince :one
-- Hoeveel opdrachten ClusterForge zelf gaf, zoals automatische analyses.
SELECT count(*) FROM assistant_conversations WHERE user_id IS NULL AND kind = @kind AND created_at >= @since;

-- name: ListAutoAnalyzeCandidates :many
-- Nieuwe incidenten die de assistent automatisch analyseert: ernst hoog of
-- kritiek, niet door een test, met een prod-cluster erin, de analyse van
-- What Broke al klaar, en nog nooit automatisch geprobeerd.
SELECT i.id, i.number FROM incidents i
WHERE i.severity IN ('high', 'critical') AND NOT i.caused_by_test
  AND i.detected_at >= @since AND i.detected_at <= @until
  AND i.ai_at IS NULL AND i.analysis IS NOT NULL
  AND EXISTS (SELECT 1 FROM incident_subjects s JOIN clusters c ON c.id = s.cluster_id
              WHERE s.incident_id = i.id AND c.environment = 'prod')
  AND NOT EXISTS (SELECT 1 FROM assistant_conversations a WHERE a.incident_id = i.id AND a.user_id IS NULL)
ORDER BY i.detected_at;

-- name: SetIncidentAI :exec
UPDATE incidents
SET ai_analysis = @ai_analysis, ai_model = @ai_model, ai_at = @ai_at, ai_by = @ai_by, ai_automatic = @ai_automatic, ai_scope = @ai_scope,
    ai_conversation_id = @ai_conversation_id, ai_cost_microusd = @ai_cost_microusd
WHERE id = @id;
