-- name: GetAssistantSettings :one
SELECT * FROM assistant_settings LIMIT 1;

-- name: CreateAssistantSettings :one
INSERT INTO assistant_settings (id, enabled, api_key_enc, key_id, model, monthly_budget_usd, viewer_access, updated_by)
VALUES (@id, @enabled, @api_key_enc, @key_id, @model, @monthly_budget_usd, @viewer_access, @updated_by)
RETURNING *;

-- name: UpdateAssistantSettings :one
UPDATE assistant_settings
SET enabled = @enabled, api_key_enc = @api_key_enc, key_id = @key_id, model = @model,
    monthly_budget_usd = @monthly_budget_usd, viewer_access = @viewer_access, updated_by = @updated_by,
    updated_at = now()
WHERE id = @id
RETURNING *;

-- name: CreateConversation :one
INSERT INTO assistant_conversations (user_id, title, subject_type, subject_id, incident_id)
VALUES (@user_id, @title, @subject_type, @subject_id, @incident_id)
RETURNING *;

-- name: GetConversation :one
SELECT c.*, u.username
FROM assistant_conversations c
JOIN users u ON u.id = c.user_id
WHERE c.id = $1;

-- name: ListConversations :many
-- De gesprekken van een gebruiker, of van iedereen voor een admin, met wat
-- ze kostten.
SELECT c.*, u.username,
       coalesce((SELECT sum(m.cost_microusd) FROM assistant_messages m WHERE m.conversation_id = c.id), 0)::bigint AS cost_microusd
FROM assistant_conversations c
JOIN users u ON u.id = c.user_id
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
