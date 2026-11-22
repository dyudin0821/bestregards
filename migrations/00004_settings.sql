-- +goose Up
-- Runtime switches an admin flips from the bot, e.g. free_ai_stopped = '1'.
CREATE TABLE settings (
    key        TEXT        PRIMARY KEY CHECK (length(key) <= 32),
    value      TEXT        NOT NULL CHECK (length(value) <= 64),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE settings;
