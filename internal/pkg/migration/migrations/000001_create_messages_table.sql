-- +goose Up
CREATE TABLE IF NOT EXISTS messages (
                                        id SERIAL PRIMARY KEY,
                                        recipient_phone TEXT NOT NULL CHECK (recipient_phone != ''),
    content TEXT NOT NULL CHECK (length(content) > 0 AND length(content) <= 160),
    sent_status BOOLEAN NOT NULL DEFAULT FALSE,
    sent_at TIMESTAMP
    );

CREATE INDEX IF NOT EXISTS idx_messages_unsent ON messages (id) WHERE sent_status = FALSE;

-- +goose Down
DROP INDEX IF EXISTS idx_messages_unsent;
DROP TABLE IF EXISTS messages;
