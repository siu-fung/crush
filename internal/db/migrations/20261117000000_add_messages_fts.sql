-- +goose Up
-- +goose StatementBegin

-- Full-text index over the searchable text of every message. The table is
-- kept in sync by the triggers below; message_search_text extracts the
-- human-readable payload from the serialized message parts.
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    message_id UNINDEXED,
    session_id UNINDEXED,
    role UNINDEXED,
    content,
    tokenize = 'unicode61 remove_diacritics 2'
);

CREATE VIEW IF NOT EXISTS message_search_text AS
SELECT
    m.id AS message_id,
    m.session_id AS session_id,
    m.role AS role,
    COALESCE((
        SELECT group_concat(part_text, ' ')
        FROM (
            SELECT
                CASE json_extract(part.value, '$.type')
                    WHEN 'text' THEN json_extract(part.value, '$.data.text')
                    WHEN 'reasoning' THEN json_extract(part.value, '$.data.thinking')
                    WHEN 'tool_call' THEN trim(
                        COALESCE(json_extract(part.value, '$.data.name'), '') || ' ' ||
                        COALESCE(json_extract(part.value, '$.data.input'), '')
                    )
                    WHEN 'tool_result' THEN trim(
                        COALESCE(json_extract(part.value, '$.data.name'), '') || ' ' ||
                        COALESCE(json_extract(part.value, '$.data.content'), '')
                    )
                    WHEN 'shell_command' THEN trim(
                        COALESCE(json_extract(part.value, '$.data.command'), '') || ' ' ||
                        COALESCE(json_extract(part.value, '$.data.output'), '')
                    )
                    WHEN 'sub_agent_report' THEN trim(
                        COALESCE(json_extract(part.value, '$.data.label'), '') || ' ' ||
                        COALESCE(json_extract(part.value, '$.data.output'), '')
                    )
                    WHEN 'finish' THEN json_extract(part.value, '$.data.message')
                    ELSE NULL
                END AS part_text
            FROM json_each(
                CASE WHEN json_valid(m.parts) THEN m.parts ELSE '[]' END
            ) AS part
            ORDER BY part.key
        )
        WHERE part_text IS NOT NULL AND part_text <> ''
    ), '') AS content
FROM messages m;

CREATE TRIGGER IF NOT EXISTS messages_fts_ai AFTER INSERT ON messages
BEGIN
    INSERT INTO messages_fts (message_id, session_id, role, content)
    SELECT message_id, session_id, role, content
    FROM message_search_text
    WHERE message_id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_ad AFTER DELETE ON messages
BEGIN
    DELETE FROM messages_fts WHERE message_id = OLD.id;
END;

-- Streaming assistant updates arrive every few milliseconds; reindexing on
-- every one of them would burn CPU for no benefit. Index on insert, on
-- user-authored updates, and whenever the message reaches a terminal state.
CREATE TRIGGER IF NOT EXISTS messages_fts_au AFTER UPDATE ON messages
WHEN NEW.role = 'user' OR NEW.finished_at IS NOT NULL
BEGIN
    DELETE FROM messages_fts WHERE message_id = OLD.id;
    INSERT INTO messages_fts (message_id, session_id, role, content)
    SELECT message_id, session_id, role, content
    FROM message_search_text
    WHERE message_id = NEW.id;
END;

-- Index messages that already existed when this migration ran.
DELETE FROM messages_fts;
INSERT INTO messages_fts (message_id, session_id, role, content)
SELECT message_id, session_id, role, content
FROM message_search_text;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS messages_fts_au;
DROP TRIGGER IF EXISTS messages_fts_ad;
DROP TRIGGER IF EXISTS messages_fts_ai;
DROP VIEW IF EXISTS message_search_text;
DROP TABLE IF EXISTS messages_fts;
-- +goose StatementEnd
